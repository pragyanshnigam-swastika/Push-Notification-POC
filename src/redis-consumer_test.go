package main

import (
	"testing"
)

func TestValidNotificationRequest(t *testing.T) {
	tests := []struct {
		name    string
		request NotificationRequest
		want    bool
	}{
		{"valid 1:1 request", NotificationRequest{DeviceTokens: []string{"token"}, Title: "Alert", Message: "Filled"}, true},
		{"missing token", NotificationRequest{Title: "Alert", Message: "Filled"}, false},
		{"blank token", NotificationRequest{DeviceTokens: []string{" "}, Title: "Alert", Message: "Filled"}, false},
		{"blank title", NotificationRequest{DeviceTokens: []string{"token"}, Title: " ", Message: "Filled"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validNotificationRequest(test.request); got != test.want {
				t.Fatalf("validNotificationRequest() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestValidRedisNotificationRequestRequiresExactlyOneToken(t *testing.T) {
	request := NotificationRequest{DeviceTokens: []string{"one", "two"}, Title: "Alert", Message: "Filled"}
	if validRedisNotificationRequest(request) {
		t.Fatal("a Redis stream entry with more than one token must be rejected")
	}
}

// isRetryable is the single, authoritative classification used by both the
// direct/notify path and the Redis path (see worker-pool.go's doSendFCM) —
// this used to be duplicated/compensated for by a separate
// isPermanentFCMError string-match in this file, removed now that
// isRetryable is reliably reached for every FCM error response.
func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name       string
		fcmStatus  string
		httpStatus int
		want       bool
	}{
		{"invalid argument", "INVALID_ARGUMENT", 400, false},
		{"unregistered token", "UNREGISTERED", 404, false},
		{"sender id mismatch", "SENDER_ID_MISMATCH", 403, false},
		{"temporary FCM outage", "UNAVAILABLE", 503, true},
		{"internal FCM error", "INTERNAL", 500, true},
		{"quota exceeded", "QUOTA_EXCEEDED", 429, true},
		{"unknown code, server error status falls back retryable", "", 500, true},
		{"unknown code, client error status falls back non-retryable", "", 400, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isRetryable(test.fcmStatus, test.httpStatus); got != test.want {
				t.Fatalf("isRetryable(%q, %d) = %v, want %v", test.fcmStatus, test.httpStatus, got, test.want)
			}
		})
	}
}
