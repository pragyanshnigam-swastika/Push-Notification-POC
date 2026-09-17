package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"firebase.google.com/go/v4/messaging"
)

// This file is a separate, independent send path — it does not replace or
// call into worker-pool.go's sendFCM/sendToManyPooled. Where those hand-roll
// the raw messages:send REST call, everything here goes through the Firebase
// Admin Go SDK's SendEachForMulticast instead. See
// docs/fcm-internals-qa/01-broadcast-vs-direct-messaging.md and the SDK's
// own source: internally this is still one HTTP request per token (FCM's v1
// API has no server-side multicast at all) — the SDK just runs its own
// 50-worker pool to do the fan-out instead of this app's.

// maxMulticastTokensPerRequest mirrors maxDeviceTokensPerRequest's
// reasoning in worker-pool.go, but the number itself is Google's, not a
// choice: SendEachForMulticast hard-rejects a single call with more than
// 500 tokens (or Fids). Requests larger than that are split into multiple
// calls below via chunkTokens (pubsub.go), the same helper already used
// for the Instance ID batch endpoints.
const maxMulticastTokensPerRequest = 500

// MulticastRequest — payload for /notify/multicast. Deliberately the same
// shape as NotificationRequest (worker-pool.go) so a caller can switch
// between the two send paths without relearning the request format.
type MulticastRequest struct {
	DeviceTokens []string `json:"deviceTokens"`
	Title        string   `json:"title"`
	Message      string   `json:"message"`
}

// MulticastResult mirrors SendResult's shape (worker-pool.go) so results
// from the two paths are directly comparable.
type MulticastResult struct {
	DeviceToken string `json:"deviceToken"`
	Success     bool   `json:"success"`
	// MessageID is FCM's own identifier — the same "name" value sendFCM
	// surfaces on the direct path, since SendEachForMulticast is sending
	// one individual message per token under the hood either way.
	MessageID string `json:"messageId,omitempty"`
	Error     string `json:"error,omitempty"`
	Retryable bool   `json:"retryable"`
}

func multicastNotifyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "only POST is allowed", http.StatusMethodNotAllowed)
		return
	}

	var req MulticastRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.DeviceTokens) == 0 {
		http.Error(w, "deviceTokens must contain at least one token", http.StatusBadRequest)
		return
	}
	// Same self-imposed ceiling as /notify, for the same reason: protect
	// this process's own resources from an oversized or malformed request,
	// independent of the 500-per-call limit Google actually enforces below.
	if len(req.DeviceTokens) > maxDeviceTokensPerRequest {
		http.Error(w, fmt.Sprintf("deviceTokens must contain at most %d tokens; split into multiple requests", maxDeviceTokensPerRequest), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	start := time.Now()
	results, err := sendMulticast(r.Context(), req.DeviceTokens, req.Title, req.Message)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	totalElapsed := time.Since(start)

	successCount := 0
	for _, res := range results {
		if res.Success {
			successCount++
		}
		log.Printf("[MULTICAST AUDIT] title=%s message=%s token=%s success=%v retryable=%v messageId=%s",
			req.Title, req.Message, mask(res.DeviceToken), res.Success, res.Retryable, res.MessageID)
	}
	log.Printf("[MULTICAST BATCH] title=%s message=%s total=%d succeeded=%d totalElapsedMs=%d",
		req.Title, req.Message, len(results), successCount, totalElapsed.Milliseconds())

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":         "completed",
		"totalElapsedMs": totalElapsed.Milliseconds(),
		"results":        results,
	})
}

// sendMulticast sends to up to maxMulticastTokensPerRequest tokens per
// underlying SendEachForMulticast call, chunking anything larger — Google
// hard-rejects a single call above that limit rather than partially
// processing it, same failure mode as the Instance ID batch endpoints.
func sendMulticast(ctx context.Context, tokens []string, title, body string) ([]MulticastResult, error) {
	results := make([]MulticastResult, 0, len(tokens))

	for _, chunk := range chunkTokens(tokens, maxMulticastTokensPerRequest) {
		message := &messaging.MulticastMessage{
			Tokens: chunk,
			Notification: &messaging.Notification{
				Title: title,
				Body:  body,
			},

			// 1. Android High Priority Config
			Android: &messaging.AndroidConfig{
				Priority: "high", // Instructs FCM to wake a sleeping Android device
			},

			// 2. iOS (APNs) High Priority Config
			APNS: &messaging.APNSConfig{
				Headers: map[string]string{
					"apns-priority": "10", // "10" tells Apple APNs to deliver the message immediately
				},
			},
		}

		batch, err := messagingClient.SendEachForMulticast(ctx, message)
		if err != nil {
			// The call itself failed (network, auth, malformed request) --
			// there's no per-token breakdown to report in this case, unlike
			// a per-token failure inside a successfully-returned batch.
			return nil, fmt.Errorf("SendEachForMulticast: %w", err)
		}

		fmt.Printf("successCount=%d failureCount=%d\n", batch.SuccessCount, batch.FailureCount)
		for i, resp := range batch.Responses {
			if resp.Success {
				fmt.Printf("  [%d] success messageId=%s\n", i, resp.MessageID)
			} else {
				fmt.Printf("  [%d] failed error=%v\n", i, resp.Error)
			}
		}

		results = append(results, mapMulticastResults(chunk, batch)...)
	}

	return results, nil
}

// mapMulticastResults turns one chunk's *messaging.BatchResponse into our
// own result shape. Responses are returned in the same order as the input
// tokens (chunk[i] corresponds to batch.Responses[i]), which is what makes
// this indexing safe.
func mapMulticastResults(chunk []string, batch *messaging.BatchResponse) []MulticastResult {
	results := make([]MulticastResult, len(chunk))
	for i, resp := range batch.Responses {
		if resp.Success {
			results[i] = MulticastResult{DeviceToken: chunk[i], Success: true, MessageID: resp.MessageID}
			continue
		}
		results[i] = MulticastResult{
			DeviceToken: chunk[i],
			Success:     false,
			Error:       resp.Error.Error(),
			Retryable:   isRetryableMulticastError(resp.Error),
		}
	}
	return results
}

// isRetryableMulticastError classifies a failure the same way
// worker-pool.go's isRetryable() does (see docs/fcm-api-reference.md §5),
// using the SDK's own typed error-checking helpers instead of parsing an
// FCM error-code string ourselves.
func isRetryableMulticastError(err error) bool {
	switch {
	case messaging.IsUnregistered(err), messaging.IsInvalidArgument(err), messaging.IsSenderIDMismatch(err):
		return false
	case messaging.IsUnavailable(err), messaging.IsInternal(err), messaging.IsQuotaExceeded(err):
		return true
	default:
		return false
	}
}
