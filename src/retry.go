package main

import (
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// parseRetryAfter reads the Retry-After header per RFC 9110 §10.2.3, which
// allows either a delta-seconds integer or an HTTP-date. FCM's own scaling
// guidance says to honor this header on 429s (defaulting to 60s if it's
// absent) — see docs/fcm-api-reference.md §4. Returns 0 if the header is
// missing or doesn't parse as either form, letting the caller fall back to
// its own backoff calculation instead of guessing at a default here.
func parseRetryAfter(h http.Header) time.Duration {
	value := h.Get("Retry-After")
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}

// backoffWithJitter computes the delay before retry attempt N using
// exponential backoff (base * 2^(attempt-1)) plus up to ~20% random
// jitter, capped at max. This is the shape Google's own FCM scaling
// guidance recommends — "1s, 2s, 4s, 8s, 16s, 32s..." — specifically to
// avoid many callers retrying at the exact same fixed interval and
// re-forming the traffic spike that caused the failure in the first place.
func backoffWithJitter(attempt int, base, max time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := base << uint(attempt-1) // base * 2^(attempt-1)
	if delay <= 0 || delay > max {   // overflow (huge attempt) or over the cap
		delay = max
	}
	jitter := time.Duration(rand.Int63n(int64(delay)/5 + 1)) // up to +20%
	return delay + jitter
}

// fcmAttempt is the shape shared by sendFCM (token target) and
// sendFCMToTopic (topic target): a message ID on success, or an error plus
// whether it's retryable and any Retry-After hint FCM returned.
type fcmAttempt func() (messageID string, err error, retryable bool, retryAfter time.Duration)

// sendWithBoundedRetry retries a synchronous FCM call up to maxAttempts
// times total, but only while it stays cheap: it never sleeps longer than
// maxDelay between attempts (using FCM's own Retry-After when present and
// smaller than that cap), and it gives up immediately once an attempt is
// classified non-retryable.
//
// This bound is a deliberate, documented trade-off, not an oversight: FCM's
// own guidance allows Retry-After values far larger than makes sense to
// block an HTTP caller on (tens of seconds is common under real
// throttling). Fully honoring that synchronously would turn "send a push"
// into "hang for a minute" for whoever called /notify or /notify/topic —
// worse for them than getting a fast retryable=true response back and
// deciding for themselves. Patient, long-horizon backoff belongs to the
// asynchronous Redis path (see redis-consumer.go's reclaimDue), which has
// no caller waiting on it. See docs/fcm-api-reference.md's retry section
// and the gap analysis for the reasoning behind this split.
func sendWithBoundedRetry(attempt fcmAttempt, maxAttempts int, maxDelay time.Duration) (string, error, bool, int) {
	for a := 1; ; a++ {
		messageID, err, retryable, retryAfter := attempt()
		if err == nil || !retryable || a >= maxAttempts {
			return messageID, err, retryable, a
		}
		delay := backoffWithJitter(a, 250*time.Millisecond, maxDelay)
		if retryAfter > 0 && retryAfter < delay {
			delay = retryAfter
		}
		time.Sleep(delay)
	}
}
