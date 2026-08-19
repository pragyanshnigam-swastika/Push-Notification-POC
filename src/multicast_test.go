package main

import (
	"errors"
	"testing"

	"firebase.google.com/go/v4/messaging"
)

func TestMapMulticastResultsPreservesOrderAndMapsFields(t *testing.T) {
	chunk := []string{"tok-a", "tok-b", "tok-c"}
	batch := &messaging.BatchResponse{
		SuccessCount: 2,
		FailureCount: 1,
		Responses: []*messaging.SendResponse{
			{Success: true, MessageID: "projects/demo/messages/0:a"},
			{Success: false, Error: errors.New("fcm error (status=404, code=UNREGISTERED): gone")},
			{Success: true, MessageID: "projects/demo/messages/0:c"},
		},
	}

	results := mapMulticastResults(chunk, batch)

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[0].DeviceToken != "tok-a" || !results[0].Success || results[0].MessageID != "projects/demo/messages/0:a" {
		t.Fatalf("unexpected result[0]: %+v", results[0])
	}
	if results[1].DeviceToken != "tok-b" || results[1].Success || results[1].Error == "" {
		t.Fatalf("unexpected result[1]: %+v", results[1])
	}
	if results[2].DeviceToken != "tok-c" || !results[2].Success || results[2].MessageID != "projects/demo/messages/0:c" {
		t.Fatalf("unexpected result[2]: %+v", results[2])
	}
}

func TestIsRetryableMulticastErrorClassification(t *testing.T) {
	// These are plain generic errors, not the SDK's internal typed errors,
	// so IsX() correctly reports false for all of them -- this test only
	// verifies the default (unclassifiable-error) branch behaves safely,
	// since constructing the SDK's real typed errors requires a live
	// response from FCM (verified separately via manual/integration testing
	// against real credentials, not something this unit test can reach).
	if isRetryableMulticastError(errors.New("some unrelated error")) {
		t.Fatalf("expected an unclassifiable error to default to non-retryable")
	}
}
