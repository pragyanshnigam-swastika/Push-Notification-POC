package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"

	"github.com/joho/godotenv"
)

var (
	httpClient *http.Client
	projectID  string
	// messagingClient backs the separate /notify/multicast path
	// (multicast.go), via the Firebase Admin Go SDK — independent of
	// httpClient, which the hand-rolled REST paths (worker-pool.go,
	// pubsub.go) use directly.
	messagingClient *messaging.Client
)

func main() {
	_ = godotenv.Load()

	ctx := context.Background()

	projectID = strings.TrimSpace(os.Getenv("PROJECT_ID"))
	if projectID == "" {
		log.Fatal("PROJECT_ID environment variable is not set")
	}

	keyData, err := loadServiceAccountCredential()
	if err != nil {
		log.Fatalf("failed to load service account credential: %v", err)
	}

	config, err := google.JWTConfigFromJSON(keyData, "https://www.googleapis.com/auth/firebase.messaging")
	if err != nil {
		log.Fatalf("failed to parse service account credentials: %v", err)
	}

	httpClient = config.Client(ctx)
	// A bounded FCM call prevents a stuck network request from holding Redis work
	// indefinitely and lets the consumer retry a temporary failure promptly.
	httpClient.Timeout = 5 * time.Second

	// The multicast path (multicast.go) goes through the Firebase Admin
	// SDK instead of a hand-rolled REST call, so it needs its own client --
	// built from the same service-account credential, with the project ID
	// pinned explicitly so it can never target a different project than
	// the rest of this app's PROJECT_ID-driven REST calls.
	firebaseApp, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: projectID}, option.WithCredentialsJSON(keyData))
	if err != nil {
		log.Fatalf("failed to initialize Firebase app: %v", err)
	}
	messagingClient, err = firebaseApp.Messaging(ctx)
	if err != nil {
		log.Fatalf("failed to initialize Firebase Messaging client: %v", err)
	}

	consumer, err := newRedisConsumerFromEnv()
	if err != nil {
		log.Fatalf("invalid Redis consumer configuration: %v", err)
	}

	mux := http.NewServeMux()
	// Worker pool handler for sending notifications to one or multiple device tokens concurrently
	mux.HandleFunc("/notify", notifyHandler)
	// Separate path using the Firebase Admin SDK's multicast helper instead
	// of this app's own worker pool -- see multicast.go.
	mux.HandleFunc("/notify/multicast", multicastNotifyHandler)
	// Pub/Sub handlers for topic subscription, notification and unsubscription
	mux.HandleFunc("/topics/subscribe", topicSubscribeHandler)
	mux.HandleFunc("/notify/topic", topicNotifyHandler)
	mux.HandleFunc("/topics/unsubscribe", topicUnsubscribeHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		readyCtx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := consumer.ready(readyCtx); err != nil {
			http.Error(w, "Redis is unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := consumer.start(ctx); err != nil {
		log.Fatalf("failed to start Redis notification consumer: %v", err)
	}
	defer consumer.close()

	server := &http.Server{Addr: ":" + port, Handler: mux}
	go func() {
		log.Printf("listening on :%s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown failed: %v", err)
	}
}

func loadServiceAccountCredential() ([]byte, error) {
	if jsonText := strings.TrimSpace(os.Getenv("FIREBASE_SERVICE_ACCOUNT_JSON")); jsonText != "" {
		return []byte(jsonText), nil
	}

	if base64Text := strings.TrimSpace(os.Getenv("FIREBASE_SERVICE_ACCOUNT_BASE64")); base64Text != "" {
		decoded, err := base64.StdEncoding.DecodeString(base64Text)
		if err != nil {
			return nil, fmt.Errorf("failed to decode FIREBASE_SERVICE_ACCOUNT_BASE64: %w", err)
		}
		return decoded, nil
	}

	if credentialsPath := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")); credentialsPath != "" {
		keyData, err := os.ReadFile(credentialsPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read GOOGLE_APPLICATION_CREDENTIALS path %s: %w", credentialsPath, err)
		}
		return keyData, nil
	}

	if _, err := os.Stat("service-account.json"); err == nil {
		keyData, err := os.ReadFile("service-account.json")
		if err != nil {
			return nil, fmt.Errorf("failed to read service-account.json: %w", err)
		}
		return keyData, nil
	}

	return nil, fmt.Errorf("no Firebase service account credential found; set FIREBASE_SERVICE_ACCOUNT_JSON, FIREBASE_SERVICE_ACCOUNT_BASE64, GOOGLE_APPLICATION_CREDENTIALS, or provide service-account.json")
}
