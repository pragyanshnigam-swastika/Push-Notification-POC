package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// NotificationRequest accepts one or many tokens under the same field —
// keeps the API shape identical whether it's a 1-1 or 1-many call.
type NotificationRequest struct {
	DeviceTokens []string `json:"deviceTokens"`
	Title        string   `json:"title"`
	Message      string   `json:"message"`
}

// maxDeviceTokensPerRequest is a self-imposed ceiling, not an FCM limit.
// FCM's v1 messages:send API has no batch/array field at all — every token
// already becomes its own individual call via sendToManyPooled, so a huge
// deviceTokens array can't overflow anything on Google's side. It can,
// however, overflow this process's own memory and goroutine budget, since
// sendToManyPooled allocates one result slot and one queued job per token
// regardless of how many run concurrently. This cap exists solely to
// protect this service from an oversized or malformed request.
const maxDeviceTokensPerRequest = 1000

// SendResult captures the outcome for one device, including whether a
// failure was retryable — this is new, and it's what lets a caller (or a
// future retry layer) know whether trying again is even worth it.
type SendResult struct {
	DeviceToken string `json:"deviceToken"`
	Success     bool   `json:"success"`
	Error       string `json:"error,omitempty"`
	Retryable   bool   `json:"retryable,omitempty"`
	LatencyMs   int64  `json:"latencyMs"`
}

func notifyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "only POST is allowed", http.StatusMethodNotAllowed)
		return
	}

	var req NotificationRequest
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

	start := time.Now() // marks the beginning of the whole batch, for the total-time log
	results := sendToManyPooled(req.DeviceTokens, req.Title, req.Message, 50)
	totalElapsed := time.Since(start)

	successCount := 0
	for _, res := range results {
		if res.Success {
			successCount++
		}
		// Per-send audit line — this is the "cheap audit log" from the POC
		// gap list: not a database, but every send is now traceable.
		log.Printf("[AUDIT] title=%s message=%s token=%s success=%v retryable=%v latencyMs=%d",
			req.Title, req.Message, mask(res.DeviceToken), res.Success, res.Retryable, res.LatencyMs)
	}

	log.Printf("[BATCH] title=%s message=%s total=%d succeeded=%d totalElapsedMs=%d",
		req.Title, req.Message, len(results), successCount, totalElapsed.Milliseconds())

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":         "completed",
		"totalElapsedMs": totalElapsed.Milliseconds(),
		"results":        results,
	})
}

// mask shortens a device token in logs so you're not printing the full
// secret-ish token string to stdout — just enough to tell entries apart.
func mask(token string) string {
	if len(token) <= 10 {
		return token
	}
	return token[:10] + "..."
}

// job represents one token to send to, tagged with its original index
// so results can be written back to the correct slot.
type job struct {
	index int
	token string
}

// sendToManyPooled processes tokens using a FIXED-size pool of workers.
// No matter how many tokens come in — 2 or 10,000 — at most `workerCount`
// requests are ever in flight to FCM at the same time.
func sendToManyPooled(tokens []string, title, body string, workerCount int) []SendResult {
	jobs := make(chan job, len(tokens))
	results := make([]SendResult, len(tokens))
	var wg sync.WaitGroup

	// Don't spin up more workers than there are jobs — no point running
	// 50 workers for a 2-token request.
	if workerCount > len(tokens) {
		workerCount = len(tokens)
	}

	log.Println("workerCount =", workerCount)

	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				sendStart := time.Now()
				// log.Printf("[START] token=%s | sendStart=%v", mask(j.token), sendStart)
				err, retryable := sendFCM(j.token, title, body)
				elapsed := time.Since(sendStart)
				// log.Printf("[END] token=%s | elapsed=%v", mask(j.token), elapsed)

				if err != nil {
					results[j.index] = SendResult{
						DeviceToken: j.token,
						Success:     false,
						Error:       err.Error(),
						Retryable:   retryable,
						LatencyMs:   elapsed.Milliseconds(),
					}
				} else {
					results[j.index] = SendResult{
						DeviceToken: j.token,
						Success:     true,
						LatencyMs:   elapsed.Milliseconds(),
					}
				}
			}
		}()
	}

	for i, t := range tokens {
		jobs <- job{index: i, token: t}
	}
	close(jobs)

	wg.Wait()
	return results
}

func pnlSign(pnl interface{}) string {
	if f, ok := pnl.(float64); ok && f < 0 {
		return "down"
	}
	return "up"
}

// fcmErrorResponse mirrors the JSON body FCM sends back on failure, e.g.:
// { "error": { "status": "UNREGISTERED", "message": "..." } }
type fcmErrorResponse struct {
	Error struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"error"`
}

// sendFCM sends the actual push and returns (error, retryable).
// retryable=false means: don't bother trying again, the failure is permanent
// (e.g. the token is dead). retryable=true means a transient issue —
// worth retrying with backoff in a future iteration.
func sendFCM(deviceToken, title, body string) (error, bool) {
	message := map[string]interface{}{
		"message": map[string]interface{}{
			"token": deviceToken,
			"notification": map[string]string{
				"title": title,
				"body":  body,
			},
			"android": map[string]interface{}{
				"priority": "high", // requests immediate delivery, bypasses some Doze deferral
			},
		},
	}

	payload, err := json.Marshal(message)
	if err != nil {
		return err, false // a marshal error is never going to succeed on retry
	}
	// log.Printf("Payload for deviceToken [%v] =", mask(deviceToken), payload)

	url := fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", projectID)
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		// Network-level failure (timeout, connection refused) — almost
		// always worth retrying.
		return err, true
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err, true
	}

	// Pretty-print response for debugging.
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, respBody, "", "  "); err == nil {
		log.Printf("FCM Response [%s]:\n%s \n%s", resp.Status, prettyJSON.String(), respBody)
	} else {
		log.Printf("[Error] FCM Response [%s]: %s", resp.Status, respBody)
	}

	// FCM returned an HTTP error.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("FCM returned HTTP %d: %s", resp.StatusCode, respBody), true
	}

	if resp.StatusCode == http.StatusOK {
		return nil, false
	}

	var fcmErr fcmErrorResponse
	_ = json.NewDecoder(bytes.NewReader(respBody)).Decode(&fcmErr) // best-effort; ignore decode failure

	retryable := isRetryable(fcmErr.Error.Status, resp.StatusCode)
	return fmt.Errorf("fcm error (status=%d, code=%s): %s", resp.StatusCode, fcmErr.Error.Status, fcmErr.Error.Message), retryable
}

// isRetryable classifies FCM's error codes into "try again later" vs
// "this will never succeed, stop trying."
func isRetryable(fcmStatus string, httpStatus int) bool {
	switch fcmStatus {
	case "UNREGISTERED", "INVALID_ARGUMENT", "SENDER_ID_MISMATCH":
		// Dead/invalid token or malformed request — retrying changes nothing.
		return false
	case "UNAVAILABLE", "INTERNAL":
		// FCM having a transient problem on its end — worth retrying.
		return true
	case "QUOTA_EXCEEDED":
		// Rate limited — retryable, but should back off, not retry immediately.
		return true
	default:
		// Unknown code — fall back to HTTP status as a rough signal.
		return httpStatus >= 500
	}
}
