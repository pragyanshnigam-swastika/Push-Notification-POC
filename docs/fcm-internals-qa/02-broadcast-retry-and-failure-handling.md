# Retry and failure handling for broadcast (topic) messages

## Bottom line

FCM retries **the delivery attempt to each device** internally according
to the same documented rules regardless of whether the message came from a
direct send or a topic fanout. What changes with topics is that **you lose
all visibility into those per-device outcomes** — there is nothing to
"explicitly handle" per subscriber, because there's nothing reported to
handle. What you must still handle explicitly is the *one* outer API call
itself, exactly like any other FCM request.

## What Google handles automatically, per device, regardless of origin

These rules apply identically whether a device received the message
because it was the direct `token` target or because it's subscribed to the
`topic` that was published to — see
[fcm-api-reference.md §2](../fcm-api-reference.md#2-fcm-http-v1-api--message-construction-limits)
for the full table:

- **TTL (time-to-live):** default 4 weeks; FCM keeps retrying delivery to
  an offline device until the message expires or is superseded.
  ([Set the lifespan of a message](https://firebase.google.com/docs/cloud-messaging/customize-messages/setting-message-lifespan))
- **Priority:** `high` priority (what this app always sets) gets FCM's most
  aggressive delivery attempts, including waking a device out of Doze.
  ([Set and manage Android message priority](https://firebase.google.com/docs/cloud-messaging/android-message-priority))
- **Collapsible/non-collapsible behavior:** governs whether a repeated
  message replaces a pending one or queues separately, capped at 100
  queued non-collapsible messages per device.
  ([Non-collapsible and collapsible messages](https://firebase.google.com/docs/cloud-messaging/customize-messages/collapsible-message-types))

None of this is topic-specific — it's how FCM treats *any* per-device
delivery attempt. Topics don't get special retry treatment; they just
multiply how many devices this logic runs against per publish call.

## What you must still handle explicitly: the outer call

The single HTTP call you make to publish to a topic is subject to the exact
same request-level error/retry rules as a direct send
([fcm-api-reference.md §4-§5](../fcm-api-reference.md#4-fcm-http-v1-api--quotas-and-throttling)):

- `429 QUOTA_EXCEEDED` → back off, honoring `Retry-After` (default 60s if
  absent), then retry.
- `5xx` / `UNAVAILABLE` / `INTERNAL` → retryable with exponential backoff.
- `400 INVALID_ARGUMENT`, `404 UNREGISTERED` (topic-equivalent doesn't
  really apply — see below), `403 SENDER_ID_MISMATCH` → permanent, don't
  retry.

This is one call to classify, not one per subscriber — that's the whole
point of the asymmetry described in
[broadcast vs. direct](./01-broadcast-vs-direct-messaging.md).

## What you cannot handle explicitly: individual dead tokens inside a topic

This is the gap worth understanding before relying on topics operationally.
If 1 of 10,000 subscribers to a topic has an uninstalled app or a stale
token:

- FCM does **not** tell the sender which subscriber failed.
- FCM does **not** return a per-device error the way a direct
  `UNREGISTERED` response would.
- The dead token isn't retried forever, but the *sender has no programmatic
  signal that it happened* — that information only shows up, if at all, in
  aggregate delivery-outcome analytics after the fact (see
  [analytics](./03-analytics-dashboard-vs-your-database.md)), not as
  something your code can branch on per message.

This is exactly why Google's [token management best
practices](https://firebase.google.com/docs/cloud-messaging/manage-tokens)
(refresh cadence, staleness windows — already covered in
[fcm-api-reference.md §7](../fcm-api-reference.md#7-token-lifecycle-and-management))
matter *more*, not less, for topic-based delivery: with direct sends, a
dead token announces itself via an error response you can act on
immediately; with topics, dead tokens just quietly reduce your effective
reach with no per-message signal.

## Practical implication for this app

If topic-based broadcast is adopted later, the retry/failure model doesn't
need new code for individual failures — there's nothing to catch, because
Google never reports it. What *is* worth adding (same requirement as the
direct path, already flagged in
[fcm-api-reference.md §11](../fcm-api-reference.md#11-gap-analysis-this-codebase-vs-official-best-practices))
is `Retry-After`-aware exponential backoff on the *publish call itself* —
that part is identical to the direct-send gap already identified, not a
new topic-specific one.

## Sources

- [Set the lifespan of a message](https://firebase.google.com/docs/cloud-messaging/customize-messages/setting-message-lifespan)
- [Set and manage Android message priority](https://firebase.google.com/docs/cloud-messaging/android-message-priority)
- [Non-collapsible and collapsible messages](https://firebase.google.com/docs/cloud-messaging/customize-messages/collapsible-message-types)
- [FCM Error Codes reference](https://firebase.google.com/docs/reference/fcm/rest/v1/ErrorCode)
- [Best practices when sending FCM messages at scale](https://firebase.google.com/docs/cloud-messaging/scale-fcm)
- [Best practices for FCM registration management](https://firebase.google.com/docs/cloud-messaging/manage-tokens)
