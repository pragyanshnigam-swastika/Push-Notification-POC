# API limits and oversized-payload handling — condensed

The full, exhaustive table with every source link lives in
[`../fcm-api-reference.md`](../fcm-api-reference.md); this file is the
short version for this specific question, plus the one thing worth adding:
**what actually happens when you exceed a limit.**

## Headline numbers

| Limit | Value | Source |
|---|---|---|
| Tokens accepted per `messages:send` call | **Exactly 1** — no batch/array field exists in v1 | [Send a message using FCM HTTP v1 API](https://firebase.google.com/docs/cloud-messaging/send/v1-api) |
| Max message payload (Android/Web) | **4096 bytes**, data + notification combined | [About FCM messages](https://firebase.google.com/docs/cloud-messaging/concept-options) |
| Max message payload (iOS via APNs) | **2 KB** | [Send messages to topics](https://firebase.google.com/docs/cloud-messaging/send-topic-messages) |
| Tokens per Instance ID `batchAdd`/`batchRemove` call | **1,000** | [Manage topics from the server](https://firebase.google.com/docs/cloud-messaging/manage-topics) |
| Default per-project send quota | **600,000 messages/minute** | [FCM Throttling and Quotas](https://firebase.google.com/docs/cloud-messaging/throttling-and-quotas) |
| Message TTL default / max | **4 weeks / 4 weeks** (2,419,200s) | [Set the lifespan of a message](https://firebase.google.com/docs/cloud-messaging/customize-messages/setting-message-lifespan) |

See [`../fcm-api-reference.md` §12](../fcm-api-reference.md#12-quick-reference-numbers)
for the complete list (topic limits, condition-expression limits,
collapsible-message limits, OAuth2 token lifetime, etc.) — not repeated
here to avoid maintaining the same numbers in two places.

## What happens when a payload exceeds the limit

**The request is rejected outright — there is no truncation, no partial
acceptance, no silent trimming.** Google returns:

- `400 INVALID_ARGUMENT` for an oversized message payload — the error
  message explicitly identifies "message too big" as one of the causes of
  this code.
  ([FCM Error Codes reference](https://firebase.google.com/docs/reference/fcm/rest/v1/ErrorCode))
- This is a **permanent, non-retryable** failure by FCM's own
  classification — retrying the same oversized payload will never succeed;
  the payload itself must be reduced before resending.

There is no server-side compression, chunking, or "send what fits and drop
the rest" behavior for an oversized `data`/`notification` block — the
whole message is atomic. If a caller needs to send more information than
4096 bytes allows, the standard pattern is to send a **small pointer**
(an ID) in the push payload and have the client fetch the full content from
your own backend after receiving it — not to try to make the push payload
itself bigger.

## What happens when you exceed a *count* limit (tokens per call)

Different failure mode from payload size, and this app now handles both
cases that actually apply to it:

- **`messages:send`:** there's no "count" to exceed — it only ever accepts
  one token, so this scenario doesn't arise there at all.
- **Instance ID `batchAdd`/`batchRemove`:** a single call above 1,000
  tokens is rejected outright, with no partial-success behavior documented
  to rely on. This app's `sendTopicBatches()` (in `src/pubsub.go`) now
  chunks any request into ≤1,000-token calls specifically because of this
  — see the [gap analysis](../fcm-api-reference.md#11-gap-analysis-this-codebase-vs-official-best-practices)
  for the before/after.
- **`/notify`'s own `deviceTokens` array:** capped at 1,000 by this app's
  own code (`maxDeviceTokensPerRequest`), rejected with an ordinary 400 —
  this is a self-imposed limit protecting this service's own resources,
  not a Google-imposed one, since every token in that array becomes its
  own separate `messages:send` call regardless of array size.

## Sources

- [`../fcm-api-reference.md`](../fcm-api-reference.md) (full limits table)
- [Send a message using FCM HTTP v1 API](https://firebase.google.com/docs/cloud-messaging/send/v1-api)
- [FCM Error Codes reference](https://firebase.google.com/docs/reference/fcm/rest/v1/ErrorCode)
- [Manage topics from the server](https://firebase.google.com/docs/cloud-messaging/manage-topics)
