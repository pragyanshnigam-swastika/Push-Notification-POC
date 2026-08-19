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

// deadLetterSuffix names the stream a message is moved to once it has
// exhausted maxDeliveryAttempts retries, so a message that FCM keeps
// rejecting is visible for operator inspection instead of retrying forever
// or disappearing silently.
const deadLetterSuffix = ":dead"

// retryAfterKeyPrefix namespaces the short-lived Redis keys used to record
// an explicit Retry-After hint FCM returned for one message, so the next
// reclaim attempt waits at least that long even when the delivery-count
// exponential backoff would otherwise allow an earlier retry. The key's
// own TTL is set to the hint's duration, so "key still exists" IS "still
// within the mandated wait" — no separate expiry bookkeeping needed.
const retryAfterKeyPrefix = "retryafter:"

// redisConsumer receives urgent notification requests from a Redis Stream.
// Streams give blocking reads, horizontal scaling, and recovery of work from
// a delivery-service process that has stopped.
type redisConsumer struct {
	client                  *redis.Client
	stream, group, consumer string
	batchSize               int64
	parallelism             int
	// reclaimAfter is the base delay before a message's first retry
	// (attempt 2); reclaimMax caps how large exponential backoff is allowed
	// to grow for a message that keeps failing.
	reclaimAfter time.Duration
	reclaimMax   time.Duration
	// maxDeliveryAttempts bounds how many times a message is retried before
	// it's moved to the dead-letter stream instead of retried again.
	maxDeliveryAttempts int64
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
	reclaimMaxSeconds, err := positiveIntFromEnv("REDIS_RECLAIM_MAX_SECONDS", 300)
	if err != nil {
		return nil, err
	}
	if reclaimMaxSeconds < reclaimAfterSeconds {
		return nil, fmt.Errorf("REDIS_RECLAIM_MAX_SECONDS must be >= REDIS_RECLAIM_AFTER_SECONDS")
	}
	maxDeliveryAttempts, err := positiveIntFromEnv("REDIS_MAX_DELIVERY_ATTEMPTS", 5)
	if err != nil {
		return nil, err
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
		reclaimAfter:        time.Duration(reclaimAfterSeconds) * time.Second,
		reclaimMax:          time.Duration(reclaimMaxSeconds) * time.Second,
		maxDeliveryAttempts: int64(maxDeliveryAttempts),
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
	log.Printf("Redis notification consumer started: stream=%s group=%s consumer=%s parallelism=%d reclaimAfter=%s reclaimMax=%s maxDeliveryAttempts=%d",
		c.stream, c.group, c.consumer, c.parallelism, c.reclaimAfter, c.reclaimMax, c.maxDeliveryAttempts)
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
			c.reclaimDue(ctx)
		}
	}
}

// reclaimDue scans every currently-pending entry via XPENDING — read-only;
// unlike XCLAIM/XAUTOCLAIM it does not reset a message's idle time or bump
// its delivery count — and decides, per message, whether it has actually
// waited long enough to be retried:
//
//  1. If it's already been delivered maxDeliveryAttempts times, it goes to
//     the dead-letter stream instead of being retried again.
//  2. If the last attempt returned an explicit Retry-After hint that
//     hasn't elapsed yet (tracked via retryAfterKeyPrefix), it's skipped
//     this round regardless of what the backoff formula below would allow.
//  3. Otherwise it's due once its idle time reaches an exponentially
//     growing threshold based on how many times it's been delivered
//     already (backoffWithJitter), capped at reclaimMax.
//
// Only entries that clear all of this get XCLAIMed (which *does* reset
// idle time and bump delivery count, as expected) and actually retried;
// everything else is left untouched so its idle time keeps accruing
// naturally for the next pass, a second or so later.
func (c *redisConsumer) reclaimDue(ctx context.Context) {
	start := "-"
	for {
		pending, err := c.client.XPendingExt(ctx, &redis.XPendingExtArgs{
			Stream: c.stream, Group: c.group, Start: start, End: "+", Count: c.batchSize,
		}).Result()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("Redis pending scan failed: %v", err)
			}
			return
		}
		if len(pending) == 0 {
			return
		}

		var due []string
		for _, p := range pending {
			if p.RetryCount >= c.maxDeliveryAttempts {
				c.deadLetter(ctx, p.ID)
				continue
			}
			if c.hasActiveRetryAfterHint(ctx, p.ID) {
				continue // FCM explicitly asked us to wait longer than our own backoff would
			}
			if p.Idle >= backoffWithJitter(int(p.RetryCount), c.reclaimAfter, c.reclaimMax) {
				due = append(due, p.ID)
			}
		}

		if len(due) > 0 {
			messages, err := c.client.XClaim(ctx, &redis.XClaimArgs{
				Stream: c.stream, Group: c.group, Consumer: c.consumer, MinIdle: 0, Messages: due,
			}).Result()
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("Redis claim failed: %v", err)
				}
			} else {
				c.processMessages(ctx, messages)
			}
		}

		if int64(len(pending)) < c.batchSize {
			return // consumed everything currently pending in this pass
		}
		start = "(" + pending[len(pending)-1].ID // exclusive-start cursor for the next page
	}
}

// hasActiveRetryAfterHint reports whether processMessage recorded a
// Retry-After hint for this message that hasn't expired yet.
func (c *redisConsumer) hasActiveRetryAfterHint(ctx context.Context, id string) bool {
	n, err := c.client.Exists(ctx, retryAfterKeyPrefix+id).Result()
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("Redis retry-after lookup failed for id=%s: %v", id, err)
		}
		return false // fail open — fall back to the delivery-count backoff rather than getting stuck
	}
	return n > 0
}

// deadLetter moves a message that has exhausted maxDeliveryAttempts out of
// the active stream's pending list and into <stream>:dead, preserving its
// original payload and ID so an operator can inspect what kept failing
// instead of it either retrying forever or vanishing silently.
func (c *redisConsumer) deadLetter(ctx context.Context, id string) {
	entries, err := c.client.XRange(ctx, c.stream, id, id).Result()
	if err != nil || len(entries) == 0 {
		log.Printf("[REDIS] id=%s could not read entry to dead-letter (err=%v); acknowledging anyway so it doesn't retry forever", id, err)
		c.ack(ctx, id)
		return
	}
	deadEntry := map[string]interface{}{
		"originalId":             id,
		notificationPayloadField: entries[0].Values[notificationPayloadField],
	}
	if err := c.client.XAdd(ctx, &redis.XAddArgs{Stream: c.stream + deadLetterSuffix, Values: deadEntry}).Err(); err != nil {
		log.Printf("[REDIS] id=%s failed to write dead-letter entry, leaving pending: %v", id, err)
		return // don't ack a message we failed to preserve anywhere
	}
	log.Printf("[REDIS] id=%s exhausted %d delivery attempts, moved to dead-letter stream %s", id, c.maxDeliveryAttempts, c.stream+deadLetterSuffix)
	c.client.Del(ctx, retryAfterKeyPrefix+id)
	c.ack(ctx, id)
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
		messageID, err, retryable, retryAfter := sendFCM(token, request.Title, request.Message)
		if err != nil {
			// sendFCM's retryable classification is authoritative here — it
			// runs every non-2xx response through isRetryable() using FCM's
			// own error code (see worker-pool.go's doSendFCM), so there's no
			// need for a second, string-matching pass over the error text.
			log.Printf("[REDIS] id=%s token=%s success=false retryable=%v retryAfter=%s latencyMs=%d error=%v",
				message.ID, mask(token), retryable, retryAfter, time.Since(started).Milliseconds(), err)
			if retryable {
				if retryAfter > 0 {
					// Record FCM's explicit hint so reclaimDue waits at
					// least this long, even if the delivery-count backoff
					// would otherwise allow an earlier retry.
					if setErr := c.client.Set(ctx, retryAfterKeyPrefix+message.ID, "1", retryAfter).Err(); setErr != nil {
						log.Printf("[REDIS] id=%s failed to record Retry-After hint: %v", message.ID, setErr)
					}
				}
				return
			} // Leave pending; reclaimDue retries it once its backoff elapses.
			continue
		}
		log.Printf("[REDIS] id=%s token=%s success=true messageId=%s latencyMs=%d", message.ID, mask(token), messageID, time.Since(started).Milliseconds())
	}
	c.ack(ctx, message.ID)
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
