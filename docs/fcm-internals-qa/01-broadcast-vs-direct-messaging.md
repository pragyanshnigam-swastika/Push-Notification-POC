# Broadcast (topics) vs. direct (token) messaging

## Bottom line

**Direct token sends are faster and give you per-message acknowledgment.
Topics are slower by design and give you none.** Google says this
explicitly, not just as an inference:

> "Topic messages are optimized for throughput rather than latency. For
> fast, secure delivery to single devices or small groups of devices,
> target messages to registration tokens, not topics."
> — [Topic Messaging](https://firebase.google.com/docs/cloud-messaging/topic-messaging)

This app's architecture (a worker pool sending directly to tokens for
`/notify`, and topics reserved only for `/notify/topic`) already matches
Google's own guidance — it isn't a workaround, it's the recommended shape.

## What "direct" and "broadcast" mean in FCM's terms

FCM's `messages:send` API has exactly three targeting modes, mutually
exclusive per call: `token`, `topic`, or `condition` (a boolean expression
over up to 5 topics). There is no fourth "send to many tokens" mode — see
[api-limits](./05-api-limits-and-payload-handling.md) for why.

- **Direct (token):** one `messages:send` call per device. This app does
  this in a loop (`sendToManyPooled`, up to 50 concurrent).
- **Broadcast (topic):** one `messages:send` call naming a `topic`; Google's
  infrastructure fans that single call out to every currently-subscribed
  device internally.

## What acknowledgment you actually get

A successful `messages:send` call — direct or topic — returns exactly one
JSON body:
```json
{ "name": "projects/{project}/messages/{message_id}" }
```
That `name` is a message identifier, not a delivery receipt.
([Method: projects.messages.send](https://firebase.google.com/docs/reference/fcm/rest/v1/projects.messages/send))

The practical difference:

| | Direct (token) | Broadcast (topic) |
|---|---|---|
| Calls made | One per device | One, total |
| Acknowledgment | One `name` **per device**, since it's one call per device | One `name` **for the whole topic publish** |
| Per-device success/failure visibility | Yes — each call's HTTP status/error tells you about that one device (§ [error codes](../fcm-api-reference.md#5-fcm-http-v1-api--error-codes-and-retry-semantics)) | **No** — Google never tells the sender which of the topic's subscribers actually received it |

This is the single most consequential difference for integration design:
**topics are fire-and-forget from the sender's point of view.** If your
workflow needs to know "did device X get this," topics cannot answer that
— only direct sends can (see [analytics](./03-analytics-dashboard-vs-your-database.md)
for what aggregate visibility topics *do* give you after the fact).

## How the fanout actually works internally

Here's what's genuinely disclosed by Google versus what isn't:

**Documented (you can rely on these):**
- Fan-out is explicitly **not instantaneous**.
- FCM limits **concurrent message fanouts to 1,000 per project**; beyond
  that, additional fanout requests may be rejected or deferred.
- Observed fanout throughput can reach **~10,000 QPS per project** in
  practice, but Google explicitly states this is **not a guarantee** —
  it depends on total system load at the time.
  ([Topic Messaging](https://firebase.google.com/docs/cloud-messaging/topic-messaging))

**Not disclosed (Google doesn't publish this, and no credible official
source does):** the literal internal architecture — whether fanout to
individual subscriber connections happens sequentially, in parallel
batches, or via some other distributed mechanism. FCM's serving
infrastructure is not open-sourced or architecturally documented at that
level of detail. Any claim you see online describing the literal
queue/worker topology inside Google's fanout system is not sourced to an
official document — treat it as speculation, including from this report.
What *is* certain is that it is **not** a simple sequential loop over
subscribers at web-request timescale — the whole point of "throughput
over latency" is that it's built to fan out to very large subscriber counts
efficiently, just not with per-recipient timing guarantees.

## Which is actually faster — direct comparison

| Dimension | Direct (token loop) | Broadcast (topic) |
|---|---|---|
| Speed for a single/small target set | Fastest — bounded by this app's own worker concurrency and FCM's response latency (~350ms P95, per [fcm-api-reference.md §9](../fcm-api-reference.md#9-delivery-performance-and-latency)) | Not designed for this case at all |
| Speed at large scale (thousands+ of recipients) | Bounded by your own concurrency and the 600K/minute project quota | Optimized for this case, but with no per-recipient timing guarantee |
| Guaranteed low latency | As close as FCM offers | Explicitly **not** what topics are for |
| Sender visibility into outcome | Full, per device | None, per device |

## Recommendation for this app

Keep the current split: **direct token sends for anything time-sensitive
or 1:1** (already the entire design of the Redis consumer path and
`/notify`), and reserve topics for genuinely broadcast, non-time-critical
content (Google's own example: "publicly available information like
weather alerts"). Don't migrate the time-sensitive path to topics for
"efficiency" — Google's own documentation says that trade would move in
the wrong direction.

## Sources

- [Topic Messaging](https://firebase.google.com/docs/cloud-messaging/topic-messaging)
- [Method: projects.messages.send](https://firebase.google.com/docs/reference/fcm/rest/v1/projects.messages/send)
- [Best practices when sending FCM messages at scale](https://firebase.google.com/docs/cloud-messaging/scale-fcm)
