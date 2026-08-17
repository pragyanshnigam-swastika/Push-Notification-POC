# FCM & Google API Reference: Limits, Quotas, and Production Readiness

Every Google-operated API this codebase calls, with exact limits, quotas,
error semantics, and performance figures pulled directly from Google's own
documentation — each figure linked to its source page so it can be verified
in a click. This is meant to be the thing you have open during the
production-readiness meeting when someone asks "what happens at scale" or
"is that actually documented."

**A note on sourcing, in the interest of accuracy:** the figures below were
gathered from Google/Firebase's official documentation pages (all linked
inline). This session's sandboxed network could not directly render
`firebase.google.com` pages to double-triple-check formatting, so treat each
citation as "go here to verify" rather than "already independently
rendered" — the linked pages are the actual source of truth, and a quick
click-through on the highest-stakes numbers (payload size, quotas) before
the meeting is a good five minutes spent.

---

## 1. Executive summary — the APIs this codebase actually calls

| API | Endpoint(s) | Used by | Auth |
|---|---|---|---|
| **FCM HTTP v1 API** | `https://fcm.googleapis.com/v1/projects/{project}/messages:send` | `sendFCM()` in `worker-pool.go` (`/notify`, and the Redis consumer path); `topicNotifyHandler()` in `pubsub.go` (`/notify/topic`) | OAuth2 bearer token from the Firebase service account |
| **Instance ID (IID) API** | `https://iid.googleapis.com/iid/v1:batchAdd`, `:batchRemove` | `topicSubscribeHandler()` / `topicUnsubscribeHandler()` in `pubsub.go` | OAuth2 bearer token (same client) |
| **Google OAuth2 token endpoint** | `https://oauth2.googleapis.com/token` (via `golang.org/x/oauth2/google`) | `main.go` — mints and auto-refreshes the bearer token used by both APIs above | Signed JWT from the service-account private key |

**The one fact worth leading with:** this app already uses the *current*,
supported FCM API (HTTP v1) and OAuth2 authentication throughout — it never
touches the legacy FCM HTTP/XMPP API that Google shut down in 2024 (§9), and
it never uses a static server key. That's the single biggest FCM-related
compliance risk, and it's already handled correctly.

---

## 2. FCM HTTP v1 API — message construction limits

| Limit | Value | Source |
|---|---|---|
| Max payload size (data + notification combined), Android/Web/general | **4096 bytes** | [About FCM messages](https://firebase.google.com/docs/cloud-messaging/concept-options) |
| Max data payload size, messages routed to iOS (via APNs) | **2 KB** | [Send messages to topics](https://firebase.google.com/docs/cloud-messaging/send-topic-messages) |
| Max payload when composing from the Firebase Console UI | **1000 characters** | [About FCM messages](https://firebase.google.com/docs/cloud-messaging/concept-options) |
| Message TTL (time-to-live) — default | **4 weeks (2,419,200 seconds)** if `ttl` is not set | [Set the lifespan of a message](https://firebase.google.com/docs/cloud-messaging/customize-messages/setting-message-lifespan) |
| Message TTL — valid range | **0 to 2,419,200 seconds (28 days)** | Same as above |
| Non-collapsible messages queued per device before drop | **100** — beyond this, FCM discards all stored messages and instead delivers a single "limit reached" signal to the client on reconnect | [Non-collapsible and collapsible messages](https://firebase.google.com/docs/cloud-messaging/customize-messages/collapsible-message-types) |
| Collapsible messages stored per device | **4** distinct collapse keys simultaneously; a new message with the same key replaces the pending one | Same as above |

**Against this app's code:** `sendFCM()` (`worker-pool.go`) sends only a
`notification{title, body}` block with no `data` payload and no explicit
`ttl` — so it's always comfortably under the 4096-byte cap, and every
message gets the default 4-week TTL. `topicNotifyHandler()` does accept an
arbitrary `data` map from the caller with **no size validation** — a caller
that stuffs a large object in there could exceed the 4096-byte limit and get
back `INVALID_ARGUMENT` (§4) with no client-side warning beforehand. Worth
adding a payload-size check before sending, once this handler sees real
traffic.

---

## 3. FCM HTTP v1 API — quotas and throttling

This is the section most likely to come up if someone asks "what happens if
this suddenly gets popular."

| Limit | Value | Source |
|---|---|---|
| Default per-project downstream messaging quota | **600,000 "Quota Tokens" per rolling 1-minute window**, refilling to full at the end of each window (not aligned to clock minutes) | [FCM Throttling and Quotas](https://firebase.google.com/docs/cloud-messaging/throttling-and-quotas) |
| What that quota covers | Explicitly stated to cover **>99% of FCM developers** by default | Same as above |
| What counts against quota | **Messages**, not requests; the quota unit measures messages actually accepted, and HTTP 4xx client errors (except 429 itself) are excluded from the count | Same as above |
| Over-quota response | HTTP **429**, gRPC status `RESOURCE_EXHAUSTED` / `QUOTA_EXCEEDED`, until the window refills | Same as above |
| Quota increase request lead time | **At least 15 days** notice via Firebase Support; **at least 30 days** for requests over 18M messages/minute | Same as above |
| Quota increase eligibility bar | Usage regularly **≥80% of current quota for ≥5 consecutive minutes/day**, with **<5% client-error ratio** at peak | Same as above |
| Temporary quota bump limits | Google approves **at most 2 temporary quota events per year**, totaling **≤30 days** across the year | Same as above |
| Recommended ramp-up for new/burst senders | Ramp from 0 to max RPS over **at least a 60-second window**; longer windows recommended for higher RPS | [Best practices when sending FCM messages at scale](https://firebase.google.com/docs/cloud-messaging/scale-fcm) |
| Recommended retry backoff | Exponential: **1s, 2s, 4s, 8s, 16s, 32s...** with random jitter, never fixed-interval retries | Same as above |
| `Retry-After` handling | **Must be honored** on 429s; if absent, default to **60 seconds** before retrying — Google's own guidance explicitly warns senders may be **blacklisted** for ignoring this and not backing off | Same as above |

**Against this app's code — this is the most important gap to flag:**
`sendFCM()` does **not** read the `Retry-After` header at all, and neither
the HTTP `/notify` path nor the Redis consumer path implements exponential
backoff. The Redis path's retry mechanism (`XAUTOCLAIM` after a fixed
`REDIS_RECLAIM_AFTER_SECONDS`, default 10s) is a *fixed*-interval retry, not
the jittered exponential backoff Google's own docs say is required to avoid
being penalized. At today's POC/demo traffic this is invisible — at real
production volume, sustained retries during an FCM slowdown could compound
into exactly the "retry amplification" pattern Google's scaling doc warns
against. This should be fixed in code before any real ramp-up in traffic —
independent of which hosting path (Render/Upstash or EC2/ElastiCache) is
chosen.

---

## 4. FCM HTTP v1 API — error codes and retry semantics

| Error code | HTTP status | Meaning (per Google's docs) | Retryable? |
|---|---|---|---|
| `INVALID_ARGUMENT` | 400 | Request parameters invalid — invalid registration, invalid package name, message too big, invalid data key, invalid TTL, etc. | No |
| `UNREGISTERED` | 404 | App instance unregistered from FCM — the token is no longer valid (uninstalled app, expired token, etc.) | No |
| `SENDER_ID_MISMATCH` | 403 | The credential used doesn't match the sender ID the token was registered under | No |
| `QUOTA_EXCEEDED` | 429 | Sending rate exceeded the per-project (or per-message-type) quota (§3) | Yes, with backoff |
| `UNAVAILABLE` | 503 | FCM servers temporarily overloaded or down | Yes, with backoff |
| `INTERNAL` | 500 | Unknown internal server error | Yes, with backoff |
| `THIRD_PARTY_AUTH_ERROR` | 401 | APNs certificate or web push auth key invalid/missing — relevant only for iOS/web targets | No (fix credentials first) |
| `UNSPECIFIED_ERROR` | — | No further information available about the error | Treat cautiously; not clearly retryable |

Source: [FCM Error Codes reference](https://firebase.google.com/docs/reference/fcm/rest/v1/ErrorCode) and the accompanying [FCM Error Codes guide](https://firebase.google.com/docs/cloud-messaging/error-codes).

**Against this app's code:** `isRetryable()` and `isPermanentFCMError()`
(`worker-pool.go` / `redis-consumer.go`) classify `UNREGISTERED`,
`INVALID_ARGUMENT`, and `SENDER_ID_MISMATCH` as permanent, and
`UNAVAILABLE`/`INTERNAL`/`QUOTA_EXCEEDED` as retryable, falling back to
`httpStatus >= 500` for anything unrecognized — **this matches Google's
documented retryability exactly.** The one addition worth making:
`THIRD_PARTY_AUTH_ERROR` isn't explicitly classified today (falls through
to the `httpStatus >= 500` default, and since it's HTTP 401 it would
currently be treated as non-retryable by that fallback — which happens to
be the right outcome, but only by coincidence of the status code, not
because the code recognizes the error explicitly).

---

## 5. Topic messaging and the Instance ID API

### Topic subscription limits

| Limit | Value | Source |
|---|---|---|
| Topics a single app instance (device token) can subscribe to | **2,000** | [Manage topics from the server](https://firebase.google.com/docs/cloud-messaging/manage-topics) |
| Tokens per `batchAdd`/`batchRemove` call | **1,000 app instances per request** | Same as above |
| Topic subscribe/unsubscribe rate | **3,000 QPS per project** | [Manage topic subscriptions](https://firebase.google.com/docs/cloud-messaging/manage-topic-subscriptions) |
| Topic name character rules | Must match `[a-zA-Z0-9-_.~%]+` (letters, numbers, and `-`, `_`, `.`, `~`, `%`) | [Manage topics from the server](https://firebase.google.com/docs/cloud-messaging/manage-topics) |

**Against this app's code:** `topicSubscribeHandler()` /
`topicUnsubscribeHandler()` forward whatever `deviceTokens` array the caller
supplies straight into a single `batchAdd`/`batchRemove` call, with **no
check against the 1,000-token-per-call limit**. A caller that sends more
than 1,000 tokens in one request would get an error back from Google that
the current code doesn't specifically anticipate or chunk around — worth
adding client-side batching (split into ≤1,000-token chunks) before this
endpoint is used for anything beyond small test batches.

### Topic message send limits

| Limit | Value | Source |
|---|---|---|
| Max topics combinable in a `condition` expression | **5**, joined with `&&`, `\|\|`, `!` | [Topic Messaging](https://firebase.google.com/docs/cloud-messaging/topic-messaging) |
| Concurrent message fanouts per project | **1,000** — beyond this, additional fanout requests may be rejected or deferred | Same as above |
| Observed fanout rate | Up to **~10,000 QPS per project** in practice — explicitly **not a guarantee**, dependent on total system load | Same as above |

**Against this app's code:** `topicNotifyHandler()` sends to a single named
topic per call (`req.Topic`), never a `condition` expression — so the
5-topic condition limit doesn't currently apply, but it's a capability
available if multi-topic targeting is ever needed later without any new
infrastructure.

### The Instance ID API's status — read this before relying on it further

The topic subscribe/unsubscribe endpoints this app calls
(`iid.googleapis.com/iid/v1:batchAdd`/`batchRemove`) belong to the older
**Instance ID API**, not the FCM v1 API surface. Two things are worth
knowing:

- **Authentication changed, not the endpoint.** As of **June 21, 2024**,
  these endpoints stopped accepting the legacy static server-key header and
  now require an OAuth2 access token — exactly what this app already does
  via its shared `httpClient`. No action needed here; just know why the
  `access_token_auth: true` header exists in `pubsub.go`.
  ([Server Reference | Instance ID](https://developers.google.com/instance-id/reference/server))
- **It's still the mechanism the official Admin SDKs use under the hood**
  for `subscribeToTopic`/`unsubscribeFromTopic` in every language (Node,
  Java, Go, Python) — so it is not an abandoned or unsupported path today.
  That said, it sits outside the actively-documented FCM v1 REST surface,
  and Google has not published a committed long-term roadmap page for the
  raw IID endpoints the way it has for `messages:send`. **Recommendation
  for production:** if/when this service adopts an official Firebase Admin
  SDK (Go has one) instead of hand-built REST calls, switching topic
  management to the SDK's `subscribeToTopic`/`unsubscribeFromTopic` methods
  costs nothing functionally today and insulates the app from ever having
  to track an endpoint change itself — Google would update the SDK, and the
  app would just upgrade a dependency.

---

## 6. Token lifecycle and management

| Fact | Detail | Source |
|---|---|---|
| Recommended token refresh cadence | **Monthly** strikes the best balance of battery cost vs. detecting dead tokens; no benefit to refreshing more than weekly | [Best practices for FCM registration management](https://firebase.google.com/docs/cloud-messaging/manage-tokens) |
| Definition of "stale" | A token whose device hasn't connected to FCM in **over a month** | Same as above |
| Hard expiry (Android) | FCM considers a token **expired after 270 days** of inactivity, after which sends to it are rejected as invalid | Same as above |
| Recommended staleness window for your own tracking | **Two months** | Same as above |

**Against this app's code:** there is **no token lifecycle tracking
anywhere in this codebase** — no last-used timestamp, no proactive pruning
of stale tokens, no deduplication. The app is entirely reactive: it only
learns a token is dead when FCM itself returns `UNREGISTERED` on a send
attempt (§4), at which point the Redis path acknowledges and drops the
message, and the HTTP path just reports `success:false` back to the caller.
This is a reasonable POC posture — Google's own guidance frames proactive
token-freshness tracking as an *optimization*, not a requirement — but a
production deployment sending at real volume should have the *upstream
caller* (the service that owns device tokens) doing this bookkeeping,
since this service has no persistent store of its own to do it in.

---

## 7. Authentication: OAuth2 service-account tokens

| Fact | Detail | Source |
|---|---|---|
| Default access token lifetime | **1 hour (3600 seconds)** | [Using OAuth 2.0 for Server to Server Applications](https://developers.google.com/identity/protocols/oauth2/service-account) |
| Extended lifetime (if explicitly configured) | Up to **12 hours (43,200 seconds)**, requires organization policy changes — not the default | Same as above |
| Risk if a token leaks | Bounded — a leaked access token is only usable for **up to an hour** before it expires (this is one of the stated advantages of v1's OAuth2 model over the legacy API's long-lived static server key) | [Migrate from legacy FCM APIs to HTTP v1](https://firebase.google.com/docs/cloud-messaging/migrate-v1) |

**Against this app's code:** `main.go` builds the OAuth2-backed
`http.Client` once at startup via
`config.Client(ctx)` (from `golang.org/x/oauth2/google`), and reuses it for
the entire process lifetime. This is correct and requires no manual
handling — the `oauth2` library's underlying `TokenSource` transparently
requests a new 1-hour token whenever the current one is close to expiring,
on every outgoing request. No code changes needed here; this is exactly how
Google expects long-running server processes to use service-account
credentials.

---

## 8. Delivery performance and latency

| Metric | Figure | Source |
|---|---|---|
| FCM HTTP v1 API response latency (accepting the send request — *not* device delivery time) | **95% of requests responded in under 350ms** over a measured 30-day period | [Understanding FCM Message Delivery on Android](https://firebase.blog/posts/2024/07/understand-fcm-delivery-rates/) (Firebase's official engineering blog) |
| Example real-world delivery rate | One case study cited on Firebase's own blog: **85%+ delivery rate across 50 million monthly notifications** for a real app | Same as above — presented as an illustrative example, **not** a guaranteed SLA figure |
| Delivery status categories | FCM's delivery data breaks results into multiple categories including **Delivered**, **Pending** (device offline — retried until the message's TTL expires), and **Message throttled** (collapsible/rate throttling) — see the source for the full breakdown | [Understanding message delivery](https://firebase.google.com/docs/cloud-messaging/understand-delivery) |
| Formal delivery-time SLA from Google | **None published.** Google documents *behavior* (priority, TTL, Doze interaction — §2) but does not commit to a numeric delivery-time guarantee for either priority level | (absence confirmed across the message-priority and concept-options docs above) |

**Why this matters for the meeting:** the 350ms figure is about how fast
FCM *acknowledges your send request*, not how fast the notification reaches
the device — that's an important distinction if anyone asks "how fast is
FCM." Actual device delivery time depends on network conditions, whether
the device is in Doze mode (§2/§9), and whether the message was sent with
`normal` or `high` priority.

**Against this app's code:** every send in this codebase — `/notify`,
`/notify/topic`, and the Redis consumer path — sets
`android.priority: "high"` unconditionally (`worker-pool.go`, `pubsub.go`).
Per Google's own guidance, high priority is the correct choice for
"time-sensitive" notifications (this app's own stated purpose) since it's
the only priority level FCM will use to wake a device out of Doze mode
(§2). The trade-off — more battery impact on the receiving device — is an
accepted cost for this app's use case, not an oversight.

---

## 9. Legacy FCM HTTP/XMPP API — deprecation history (context, not action)

| Milestone | Date | Source |
|---|---|---|
| Deprecation announced | **June 20, 2023** | [Migrate from legacy FCM APIs to HTTP v1](https://firebase.google.com/docs/cloud-messaging/migrate-v1) |
| Legacy API sending stopped | **June 20, 2024** | Same as above |
| Full shutdown process | Began **July 22, 2024** | Same as above |

This app was built directly on the v1 API and OAuth2 from the start — this
section exists purely as confirmation there is nothing to migrate, and as
context in case the topic of "why v1 and not the old API" comes up.

---

## 10. Gap analysis: this codebase vs. official best practices

A consolidated, actionable list — everything above that represents a real
gap between what Google recommends and what the code currently does,
ordered by how much it matters before scaling up traffic:

1. **No `Retry-After` handling or exponential backoff on FCM retries**
   (§3). The single highest-priority fix — Google explicitly warns of
   possible blacklisting for senders who don't back off correctly on 429s.
2. **No client-side payload size validation** on `topicNotifyHandler`'s
   free-form `data` field (§2) — a large payload fails only after a round
   trip to FCM, with no earlier warning.
3. **No batching/chunking of `deviceTokens` against the 1,000-per-call IID
   limit** (§5) on the topic subscribe/unsubscribe endpoints.
4. **No token staleness tracking** (§6) — acceptable given this service
   has no persistent store, but worth explicitly assigning to whichever
   upstream service owns device tokens.
5. **Topic management rides on the Instance ID API rather than an official
   Admin SDK** (§5) — not broken, but worth revisiting if/when an Admin SDK
   is adopted for other reasons.

None of these are hosting-platform-specific — they apply identically
whether the service runs on Render+Upstash (the current POC) or EC2+
ElastiCache (the recommended production path), and fixing them is
independent of and complementary to the production infrastructure work in
[production-deployment-explained.md](./production-deployment-explained.md).

---

## 11. Quick-reference numbers

The one table to have open during the meeting:

| Figure | Value |
|---|---|
| Max message payload (Android/Web) | 4096 bytes |
| Max message payload (iOS via APNs) | 2 KB |
| Default message TTL | 4 weeks (2,419,200s) |
| Non-collapsible message queue cap per device | 100 messages |
| Collapsible message slots per device | 4 collapse keys |
| Default per-project send quota | 600,000 messages/minute |
| Quota increase notice required | 15 days (30 days if >18M/min) |
| Recommended traffic ramp-up window | ≥60 seconds, 0 → max RPS |
| Recommended retry backoff | Exponential with jitter (1s, 2s, 4s, 8s...) |
| Default `Retry-After` assumption if header absent | 60 seconds |
| Max topics per app instance | 2,000 |
| Max tokens per topic batch call | 1,000 |
| Topic subscribe/unsubscribe rate limit | 3,000 QPS/project |
| Max topics in a condition expression | 5 |
| Concurrent topic fanouts per project | 1,000 |
| OAuth2 access token lifetime | 1 hour (3,600s) |
| FCM v1 API response latency (P95) | <350ms |
| Legacy FCM API shutdown | June 20, 2024 |
| Token hard-expiry threshold (Android) | 270 days inactive |

---

## 12. Official resource index

Every source cited above, deduplicated:

- [About FCM messages](https://firebase.google.com/docs/cloud-messaging/concept-options)
- [Set the lifespan of a message](https://firebase.google.com/docs/cloud-messaging/customize-messages/setting-message-lifespan)
- [Non-collapsible and collapsible messages](https://firebase.google.com/docs/cloud-messaging/customize-messages/collapsible-message-types)
- [Send messages to topics](https://firebase.google.com/docs/cloud-messaging/send-topic-messages)
- [Topic Messaging](https://firebase.google.com/docs/cloud-messaging/topic-messaging)
- [Manage topics from the server](https://firebase.google.com/docs/cloud-messaging/manage-topics)
- [Manage topic subscriptions](https://firebase.google.com/docs/cloud-messaging/manage-topic-subscriptions)
- [FCM Throttling and Quotas](https://firebase.google.com/docs/cloud-messaging/throttling-and-quotas)
- [Best practices when sending FCM messages at scale](https://firebase.google.com/docs/cloud-messaging/scale-fcm)
- [FCM Error Codes reference (enum)](https://firebase.google.com/docs/reference/fcm/rest/v1/ErrorCode)
- [FCM Error Codes guide](https://firebase.google.com/docs/cloud-messaging/error-codes)
- [Best practices for FCM registration management](https://firebase.google.com/docs/cloud-messaging/manage-tokens)
- [Set and manage Android message priority](https://firebase.google.com/docs/cloud-messaging/android-message-priority)
- [Migrate from legacy FCM APIs to HTTP v1](https://firebase.google.com/docs/cloud-messaging/migrate-v1)
- [Understanding message delivery](https://firebase.google.com/docs/cloud-messaging/understand-delivery)
- [Understanding FCM Message Delivery on Android (Firebase engineering blog)](https://firebase.blog/posts/2024/07/understand-fcm-delivery-rates/)
- [Using OAuth 2.0 for Server to Server Applications](https://developers.google.com/identity/protocols/oauth2/service-account)
- [Instance ID API — Server Reference](https://developers.google.com/instance-id/reference/server)
