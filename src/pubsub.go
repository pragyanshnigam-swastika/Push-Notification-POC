package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"
)

const (
	iidBatchAddURL    = "https://iid.googleapis.com/iid/v1:batchAdd"
	iidBatchRemoveURL = "https://iid.googleapis.com/iid/v1:batchRemove"

	// iidMaxTokensPerBatch is a hard limit Google enforces on the Instance
	// ID batchAdd/batchRemove endpoints: a single call with more
	// registration tokens than this is rejected outright. Unlike
	// maxDeviceTokensPerRequest in worker-pool.go, this number isn't a
	// choice — it must match Google's actual cap (see
	// docs/fcm-api-reference.md §3/§6), so requests larger than this are
	// split into multiple batchAdd/batchRemove calls below.
	iidMaxTokensPerBatch = 1000
)

// TopicSubscribeRequest — payload for subscribing tokens to a topic.
type TopicSubscribeRequest struct {
	Topic        string   `json:"topic"`
	DeviceTokens []string `json:"deviceTokens"`
}

// TopicNotifyRequest — payload for sending a message to an existing topic.
type TopicNotifyRequest struct {
	Topic string                 `json:"topic"`
	Title string                 `json:"title"`
	Body  string                 `json:"body"`
	Data  map[string]interface{} `json:"data"`
}

// TopicUnsubscribeRequest — same shape as TopicSubscribeRequest.
type TopicUnsubscribeRequest struct {
	Topic        string   `json:"topic"`
	DeviceTokens []string `json:"deviceTokens"`
}

// batchChunkResult is one chunk's outcome against the Instance ID API.
// Reporting per-chunk instead of merging everything into one blob means a
// caller who sent more than iidMaxTokensPerBatch tokens can see exactly
// which chunk (if any) failed, rather than losing that detail.
type batchChunkResult struct {
	TokenCount int                    `json:"tokenCount"`
	StatusCode int                    `json:"statusCode,omitempty"`
	Body       map[string]interface{} `json:"body,omitempty"`
	Error      string                 `json:"error,omitempty"`
}

// chunkTokens splits tokens into groups of at most size, preserving order.
func chunkTokens(tokens []string, size int) [][]string {
	if len(tokens) == 0 {
		return nil
	}
	chunks := make([][]string, 0, (len(tokens)+size-1)/size)
	for start := 0; start < len(tokens); start += size {
		end := start + size
		if end > len(tokens) {
			end = len(tokens)
		}
		chunks = append(chunks, tokens[start:end])
	}
	return chunks
}

// sendTopicBatches calls the given Instance ID batch endpoint (batchAdd or
// batchRemove) once per chunk of at most iidMaxTokensPerBatch tokens,
// since Google rejects a single call above that limit outright rather than
// partially processing it. Shared by topicSubscribeHandler and
// topicUnsubscribeHandler — batchAdd/batchRemove differ only in URL.
func sendTopicBatches(url, topic string, tokens []string) []batchChunkResult {
	chunks := chunkTokens(tokens, iidMaxTokensPerBatch)
	results := make([]batchChunkResult, 0, len(chunks))

	for _, chunk := range chunks {
		requestBody := map[string]interface{}{
			"to":                  "/topics/" + topic,
			"registration_tokens": chunk,
		}
		payload, err := json.Marshal(requestBody)
		if err != nil {
			results = append(results, batchChunkResult{TokenCount: len(chunk), Error: err.Error()})
			continue
		}

		httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			results = append(results, batchChunkResult{TokenCount: len(chunk), Error: err.Error()})
			continue
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("access_token_auth", "true") // required — tells this endpoint the Bearer token is OAuth2, not a legacy server key

		resp, err := httpClient.Do(httpReq) // same authenticated client — Bearer token attached automatically
		if err != nil {
			results = append(results, batchChunkResult{TokenCount: len(chunk), Error: err.Error()})
			continue
		}

		responseBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			results = append(results, batchChunkResult{TokenCount: len(chunk), StatusCode: resp.StatusCode, Error: err.Error()})
			continue
		}

		var prettyJSON bytes.Buffer
		if jsonErr := json.Indent(&prettyJSON, responseBody, "", "  "); jsonErr == nil {
			log.Printf("Topic batch response [%s] tokens=%d:\n%s", resp.Status, len(chunk), prettyJSON.String())
		} else {
			log.Printf("[Error] Topic batch response [%s] tokens=%d: %s", resp.Status, len(chunk), responseBody)
		}

		var body map[string]interface{}
		_ = json.Unmarshal(responseBody, &body) // best-effort; a non-JSON body still reports via StatusCode
		results = append(results, batchChunkResult{TokenCount: len(chunk), StatusCode: resp.StatusCode, Body: body})
	}

	return results
}

// topicSubscribeHandler responds with {"tokenCount", "batches"} rather than
// forwarding Google's raw response — with chunking, a single request can
// now produce more than one underlying batchAdd call, so there's no longer
// one flat response to forward as-is.
func topicSubscribeHandler(w http.ResponseWriter, r *http.Request) {
	var req TopicSubscribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Topic == "" || len(req.DeviceTokens) == 0 {
		http.Error(w, "topic and deviceTokens are required", http.StatusBadRequest)
		return
	}

	batches := sendTopicBatches(iidBatchAddURL, req.Topic, req.DeviceTokens)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"tokenCount": len(req.DeviceTokens),
		"batches":    batches,
	})
}

// sendFCMToTopic sends a push to every current subscriber of a topic and
// returns (messageID, error, retryable, retryAfter) — the same shape as
// sendFCM, sharing its HTTP call and error classification via doSendFCM.
// A single call here is one publish to the topic, not one call per
// subscriber — see docs/fcm-internals-qa/01-broadcast-vs-direct-messaging.md
// for why FCM never reports per-subscriber outcomes back to the sender.
func sendFCMToTopic(topic, title, body string, data map[string]interface{}) (string, error, bool, time.Duration) {
	message := map[string]interface{}{
		"message": map[string]interface{}{
			"topic": topic, // note: "topic" here, not "token" — this is the only structural difference from sendFCM
			"notification": map[string]string{
				"title": title,
				"body":  body,
			},

			// 1. Android High Priority Config
			"android": map[string]interface{}{
				"priority": "high", // Instructs FCM to wake a sleeping Android device
			},

			// 2. iOS (APNs) High Priority Config
			"apns": map[string]interface{}{
				"headers": map[string]string{
					"apns-priority": "10", // "10" tells Apple APNs to deliver the message immediately
				},
			},

			"data": data,
		},
	}
	return doSendFCM(message)
}

// topicNotifyHandler used to proxy FCM's raw response straight through with
// no retry and no error classification at all — a transient 429/503 on a
// topic publish just became the caller's problem verbatim. It now retries
// transient failures the same bounded way /notify does (see retry.go) and
// reports a result shape consistent with /notify's, including FCM's
// message ID and how many attempts it took.
func topicNotifyHandler(w http.ResponseWriter, r *http.Request) {
	var req TopicNotifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Topic == "" {
		http.Error(w, "topic is required", http.StatusBadRequest)
		return
	}

	start := time.Now()
	messageID, err, retryable, attempts := sendWithBoundedRetry(func() (string, error, bool, time.Duration) {
		return sendFCMToTopic(req.Topic, req.Title, req.Body, req.Data)
	}, syncMaxAttempts, syncMaxRetryDelay)
	elapsed := time.Since(start)
	success := err == nil

	log.Printf("[AUDIT] topic=%s title=%s success=%v retryable=%v attempts=%d messageId=%s latencyMs=%d",
		req.Topic, req.Title, success, retryable, attempts, messageID, elapsed.Milliseconds())

	response := map[string]interface{}{
		"success":   success,
		"attempts":  attempts,
		"latencyMs": elapsed.Milliseconds(),
	}
	if success {
		response["messageId"] = messageID
	} else {
		response["error"] = err.Error()
		response["retryable"] = retryable
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// topicUnsubscribeHandler mirrors topicSubscribeHandler — same chunking
// logic and response shape, batchRemove being the only difference.
func topicUnsubscribeHandler(w http.ResponseWriter, r *http.Request) {
	var req TopicUnsubscribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Topic == "" || len(req.DeviceTokens) == 0 {
		http.Error(w, "topic and deviceTokens are required", http.StatusBadRequest)
		return
	}

	batches := sendTopicBatches(iidBatchRemoveURL, req.Topic, req.DeviceTokens)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"tokenCount": len(req.DeviceTokens),
		"batches":    batches,
	})
}
