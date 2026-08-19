# Retry and failure handling: FCM's side, and what this app implements (1:1 and topics)

## Bottom line

FCM retries **delivery to a device** internally, automatically, for both
1:1 and topic sends — that part needs no code at all. FCM does **not**
retry **the API call** if it fails or times out — that has always been the
caller's job, for both message types. This app now does that job properly:
a bounded, fast retry on the synchronous HTTP paths, and true exponential
backoff with a dead-letter safety net on the asynchronous Redis path.

---

## 1. What FCM does automatically — identical for 1:1 and topics

Once FCM has **accepted** a message (a `200` from `messages:send`), it
manages delivery to the device(s) itself, using the same rules regardless
of whether the target was a `token` or a `topic`:

- **TTL (time-to-live):** default 4 weeks — FCM keeps trying to deliver to
  an offline device until the message expires or is superseded.
  ([Set the lifespan of a message](https://firebase.google.com/docs/cloud-messaging/customize-messages/setting-message-lifespan))
- **Priority:** `high` (what this app always sets, both paths) gets FCM's
  most aggressive delivery attempts, including waking a device out of
  Doze.
  ([Set and manage Android message priority](https://firebase.google.com/docs/cloud-messaging/android-message-priority))
- **Collapsible/non-collapsible rules:** govern whether a repeated message
  replaces a pending one, capped at 100 queued non-collapsible messages
  per device.
  ([Non-collapsible and collapsible messages](https://firebase.google.com/docs/cloud-messaging/customize-messages/collapsible-message-types))

This is **fully reliable and needs zero code from us** — it's the same
mechanism either way. Topics don't get special treatment; a topic publish
just runs this logic against every current subscriber at once instead of
one device.

**What FCM never does automatically, for either message type:** retry the
`messages:send` HTTP call itself if it fails outright (429, 5xx, network
error). That has always been on the caller — and until this change, this
app only did it properly on one of its three send paths.

---

## 2. What this app implements today

### 1:1 — `/notify` (HTTP, synchronous)

`sendToManyPooled` (`worker-pool.go`) now wraps every per-token `sendFCM`
call in `sendWithBoundedRetry` (`retry.go`):

- Up to **3 attempts** total (`syncMaxAttempts`).
- Exponential backoff with jitter between attempts, **capped at 2 seconds**
  (`syncMaxRetryDelay`) — using FCM's own `Retry-After` header when present
  and smaller than that cap.
- Stops immediately, no further attempts, the moment a response is
  classified non-retryable (e.g. `UNREGISTERED`).
- The final `SendResult` now reports `attempts` alongside `success` and
  `retryable`, so a caller can see whether a failure already survived
  retries or is reporting on the first try.

**Why capped at 2 seconds and not a full backoff:** this is a synchronous
HTTP handler with a caller waiting on a response. FCM's own guidance
allows `Retry-After` values of tens of seconds under real throttling —
honoring that fully here would turn "send a push" into "hang for a
minute," which is worse for the caller than a fast `retryable: true` they
can act on themselves. This is a deliberate, documented trade-off, not an
oversight — see `retry.go`'s comments on `sendWithBoundedRetry`.

### 1:M — `/notify/topic` (HTTP, synchronous)

Previously, this handler had **no retry and no error classification at
all** — it proxied FCM's raw response straight through, so a transient
`503` on a topic publish became the caller's problem verbatim. It now uses
the exact same `sendWithBoundedRetry` mechanism as `/notify`, via a new
`sendFCMToTopic` function that shares the same underlying HTTP call and
classification logic (`doSendFCM`). The response shape changed to match
`/notify`'s (`success`, `attempts`, `messageId`/`error`+`retryable`)
instead of forwarding Google's raw body.

### 1:1 — the Redis consumer path (asynchronous)

This is where genuine, patient exponential backoff belongs, because
nothing is synchronously waiting on it. `redis-consumer.go`'s `reclaimDue`
(replacing the old fixed-interval `XAUTOCLAIM` loop) now:

1. Scans pending entries read-only via `XPENDING` (doesn't disturb their
   idle time or delivery count).
2. Computes each message's next-eligible-retry time as
   `base * 2^(deliveries-1)` with jitter, capped at
   `REDIS_RECLAIM_MAX_SECONDS` — real exponential backoff, not a fixed
   interval.
3. **Also honors FCM's `Retry-After` hint directly**: if the last attempt
   returned one, it's recorded as a short-lived Redis key with that exact
   TTL, and the message won't be reclaimed while that key still exists —
   even if the delivery-count backoff alone would have allowed an earlier
   retry.
4. Only messages that clear both checks get `XCLAIM`ed and actually
   retried.
5. A message that's been delivered `REDIS_MAX_DELIVERY_ATTEMPTS` times
   (default 5) without succeeding is moved to a `<stream>:dead` stream
   instead of retried again — visible for operator inspection rather than
   retrying forever or vanishing silently.

This was verified against a real Redis instance (not just reasoned about):
confirmed that `XPENDING` doesn't disturb state while `XCLAIM`/`XAUTOCLAIM`
do, confirmed the backoff correctly delays a retry until its window
elapses, confirmed a `Retry-After` hint correctly overrides a shorter
count-based backoff, and confirmed dead-lettering after exhausting
attempts correctly stops retries and preserves the original payload.

### The one thing that remains true for both, permanently — not a gap, a hard limit

**No per-device visibility into a topic's fanout, ever.** If 1 of 10,000
topic subscribers has a dead token, FCM does not tell the sender which one
— there is no error, no callback, nothing to retry differently for that
specific device. This is not something retry logic can fix, on either
side — it's a structural property of how topics work, covered in full in
[broadcast vs. direct messaging](./01-broadcast-vs-direct-messaging.md).
The retry/backoff work above governs the **one outer publish call**; it
was never able to, and still can't, reach into a topic's individual
subscriber outcomes.

---

## 3. Statement for the team

> FCM reliably retries delivering an **accepted** message to a device on
> its own, for both direct sends and topic broadcasts — that requires no
> code from us and has never been a gap. What FCM does **not** do is retry
> the API call if that call itself fails or times out (rate limiting,
> transient server errors, network issues) — that has always been, and
> remains, this service's responsibility, identically for 1:1 and 1:M.
> As of this change, all three send paths (`/notify`, `/notify/topic`, and
> the Redis-based 1:1 queue) handle that correctly: the two synchronous
> HTTP paths retry fast and briefly since a caller is waiting; the
> asynchronous Redis path retries patiently with true exponential backoff,
> honors FCM's own requested wait time when it gives one, and gives up
> cleanly into an inspectable dead-letter stream rather than retrying
> forever. The one thing no amount of our own code can change: once a
> message is published to a **topic**, we get exactly one acknowledgment
> for the whole broadcast and no way to know which individual subscribers
> received it — that's a structural limit of topics, not a retry-handling
> gap.

## Sources

- [Set the lifespan of a message](https://firebase.google.com/docs/cloud-messaging/customize-messages/setting-message-lifespan)
- [Set and manage Android message priority](https://firebase.google.com/docs/cloud-messaging/android-message-priority)
- [Non-collapsible and collapsible messages](https://firebase.google.com/docs/cloud-messaging/customize-messages/collapsible-message-types)
- [FCM Error Codes reference](https://firebase.google.com/docs/reference/fcm/rest/v1/ErrorCode)
- [Best practices when sending FCM messages at scale](https://firebase.google.com/docs/cloud-messaging/scale-fcm)
- [Best practices for FCM registration management](https://firebase.google.com/docs/cloud-messaging/manage-tokens)
- [Topic Messaging](https://firebase.google.com/docs/cloud-messaging/topic-messaging)
