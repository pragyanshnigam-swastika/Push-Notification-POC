package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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

	requestBody := map[string]interface{}{
		"to":                  "/topics/" + req.Topic,
		"registration_tokens": req.DeviceTokens,
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	httpReq, err := http.NewRequest(http.MethodPost, "https://iid.googleapis.com/iid/v1:batchAdd", bytes.NewReader(payload))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("access_token_auth", "true") // required — tells this endpoint the Bearer token is OAuth2, not a legacy server key

	resp, err := httpClient.Do(httpReq) // same authenticated client — Bearer token attached automatically
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Read the response body.
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Pretty-print JSON in the terminal.
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, responseBody, "", "  "); err == nil {
		log.Printf("Topic subscription response [%s]:\n%s \n%s",
			resp.Status,
			prettyJSON.String(),
			responseBody,
		)
	} else {
		// Response wasn't valid JSON.
		log.Printf("[Error] Topic subscription response [%s]: %s",
			resp.Status,
			responseBody,
		)
	}

	var result map[string]interface{}
	// json.NewDecoder(resp.Body).Decode(&result)
	json.NewDecoder(bytes.NewReader(responseBody)).Decode(&result)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

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

	message := map[string]interface{}{
		"message": map[string]interface{}{
			"topic": req.Topic, // note: "topic" here, not "token" — this is the only structural difference from your existing sendFCM
			"notification": map[string]string{
				"title": req.Title,
				"body":  req.Body,
			},
			"android": map[string]interface{}{
				"priority": "high", // requests immediate delivery, bypasses some Doze deferral
			},
			"data": req.Data,
		},
	}
	payload, _ := json.Marshal(message)

	url := fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", projectID)
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.WriteHeader(resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	w.Write(body)
}

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

	body := map[string]interface{}{
		"to":                  "/topics/" + req.Topic,
		"registration_tokens": req.DeviceTokens,
	}
	payload, _ := json.Marshal(body)

	httpReq, err := http.NewRequest(http.MethodPost, "https://iid.googleapis.com/iid/v1:batchRemove", bytes.NewReader(payload))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("access_token_auth", "true")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
