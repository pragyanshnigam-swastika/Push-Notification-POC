package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const notificationPayloadField = "payload"

// redisConsumer receives urgent notification requests from a Redis Stream.
// Streams give blocking reads, horizontal scaling, and recovery of work from
// a delivery-service process that has stopped.
type redisConsumer struct {
	client                  *redis.Client
	stream, group, consumer string
	batchSize               int64
	parallelism             int
	reclaimAfter            time.Duration
}

func newRedisConsumerFromEnv() (*redisConsumer, error) {
	tlsEnabled, err := boolFromEnv("REDIS_TLS", false)
	if err != nil {
		return nil, err
	}
	batchSize, err := positiveIntFromEnv("REDIS_BATCH_SIZE", 64)
	if err != nil {
		return nil, err
	}
	parallelism, err := positiveIntFromEnv("REDIS_PARALLELISM", 64)
	if err != nil {
		return nil, err
	}
	reclaimAfterSeconds, err := positiveIntFromEnv("REDIS_RECLAIM_AFTER_SECONDS", 10)
	if err != nil {
		return nil, err
	}
	if reclaimAfterSeconds <= 5 {
		return nil, fmt.Errorf("REDIS_RECLAIM_AFTER_SECONDS must be greater than the 5-second FCM timeout")
	}

	consumerName := strings.TrimSpace(os.Getenv("REDIS_CONSUMER_NAME"))
	if consumerName == "" {
		host, _ := os.Hostname()
		consumerName = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	options := &redis.Options{
		Addr:     envOrDefault("REDIS_ADDR", "localhost:6379"),
		Username: strings.TrimSpace(os.Getenv("REDIS_USERNAME")),
		Password: os.Getenv("REDIS_PASSWORD"),
	}
	if tlsEnabled {
		options.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	return &redisConsumer{
		client:   redis.NewClient(options),
		stream:   envOrDefault("REDIS_STREAM", "notification_requests"),
		group:    envOrDefault("REDIS_GROUP", "push-delivery"),
		consumer: consumerName, batchSize: int64(batchSize), parallelism: parallelism,
		reclaimAfter: time.Duration(reclaimAfterSeconds) * time.Second,
	}, nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func boolFromEnv(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false: %w", key, err)
	}
	return parsed, nil
}

func positiveIntFromEnv(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}

func (c *redisConsumer) start(ctx context.Context) error {
	if err := c.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("connect to Redis: %w", err)
	}
	if err := c.client.XGroupCreateMkStream(ctx, c.stream, c.group, "0").Err(); err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("create Redis consumer group: %w", err)
	}
	log.Printf("Redis notification consumer started: stream=%s group=%s consumer=%s parallelism=%d", c.stream, c.group, c.consumer, c.parallelism)
	go c.reclaimLoop(ctx)
	go c.consumeLoop(ctx)
	return nil
}

func (c *redisConsumer) close() error { return c.client.Close() }

func (c *redisConsumer) ready(ctx context.Context) error { return c.client.Ping(ctx).Err() }

func (c *redisConsumer) consumeLoop(ctx context.Context) {
	for ctx.Err() == nil {
		streams, err := c.client.XReadGroup(ctx, &redis.XReadGroupArgs{Group: c.group, Consumer: c.consumer, Streams: []string{c.stream, ">"}, Count: c.batchSize, Block: time.Second}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) || ctx.Err() != nil {
				continue
			}
			log.Printf("Redis read failed: %v", err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		for _, stream := range streams {
			c.processMessages(ctx, stream.Messages)
		}
	}
}

func (c *redisConsumer) reclaimLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			start := "0-0"
			for {
				messages, next, err := c.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{Stream: c.stream, Group: c.group, Consumer: c.consumer, MinIdle: c.reclaimAfter, Start: start, Count: c.batchSize}).Result()
				if err != nil {
					if ctx.Err() == nil {
						log.Printf("Redis claim failed: %v", err)
					}
					break
				}
				c.processMessages(ctx, messages)
				if len(messages) == 0 || next == start {
					break
				}
				start = next
			}
		}
	}
}

func (c *redisConsumer) processMessages(ctx context.Context, messages []redis.XMessage) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, c.parallelism)
	for _, message := range messages {
		wg.Add(1)
		sem <- struct{}{}
		go func(message redis.XMessage) {
			defer wg.Done()
			defer func() { <-sem }()
			c.processMessage(ctx, message)
		}(message)
	}
	wg.Wait()
}

func (c *redisConsumer) processMessage(ctx context.Context, message redis.XMessage) {
	payload, ok := message.Values[notificationPayloadField].(string)
	if !ok {
		log.Printf("[REDIS] id=%s invalid message: payload must be a JSON string", message.ID)
		c.ack(ctx, message.ID)
		return
	}
	var request NotificationRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil || !validRedisNotificationRequest(request) {
		log.Printf("[REDIS] id=%s invalid NotificationRequest: %v", message.ID, err)
		c.ack(ctx, message.ID)
		return
	}
	for _, token := range request.DeviceTokens {
		started := time.Now()
		messageID, err, retryable := sendFCM(token, request.Title, request.Message)
		if err != nil {
			// sendFCM currently reports every non-2xx HTTP result as retryable.
			// For Redis delivery, do not leave known permanent FCM failures in
			// the pending list forever.
			retryable = retryable && !isPermanentFCMError(err)
			log.Printf("[REDIS] id=%s token=%s success=false retryable=%v latencyMs=%d error=%v", message.ID, mask(token), retryable, time.Since(started).Milliseconds(), err)
			if retryable {
				return
			} // Leave pending; XAUTOCLAIM retries it.
			continue
		}
		log.Printf("[REDIS] id=%s token=%s success=true messageId=%s latencyMs=%d", message.ID, mask(token), messageID, time.Since(started).Milliseconds())
	}
	c.ack(ctx, message.ID)
}

func isPermanentFCMError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "INVALID_ARGUMENT") ||
		strings.Contains(message, "UNREGISTERED") ||
		strings.Contains(message, "SENDER_ID_MISMATCH")
}

func validNotificationRequest(request NotificationRequest) bool {
	if len(request.DeviceTokens) == 0 || strings.TrimSpace(request.Title) == "" || strings.TrimSpace(request.Message) == "" {
		return false
	}
	for _, token := range request.DeviceTokens {
		if strings.TrimSpace(token) == "" {
			return false
		}
	}
	return true
}

// A stream entry must represent exactly one push. Keeping this boundary at 1:1
// means a retry can never re-send another token from a partially completed batch.
func validRedisNotificationRequest(request NotificationRequest) bool {
	return len(request.DeviceTokens) == 1 && validNotificationRequest(request)
}

func (c *redisConsumer) ack(ctx context.Context, id string) {
	if err := c.client.XAck(ctx, c.stream, c.group, id).Err(); err != nil {
		log.Printf("Redis acknowledgement failed for id=%s: %v", id, err)
	}
}
