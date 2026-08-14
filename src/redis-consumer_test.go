package main

import (
	"errors"
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

func TestIsPermanentFCMError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"invalid token", errors.New("FCM returned HTTP 400: INVALID_ARGUMENT"), true},
		{"unregistered token", errors.New("UNREGISTERED"), true},
		{"temporary FCM outage", errors.New("FCM returned HTTP 503: UNAVAILABLE"), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isPermanentFCMError(test.err); got != test.want {
				t.Fatalf("isPermanentFCMError() = %v, want %v", got, test.want)
			}
		})
	}
}
