# How fast can you subscribe tokens and then message the topic?

## Bottom line

**Google does not publish a numeric guarantee for this.** What's
documented is only qualitative: subscription is asynchronous and
rate-limited, and topic fanout is explicitly "not instantaneous." If your
workflow is "subscribe a batch of tokens, then immediately notify them,"
the officially safer approach is to **send directly to the tokens you just
subscribed**, not rely on the topic fanout for the first message.

## What's actually documented about subscription timing

- Subscribing (`batchAdd`) is **rate-limited to 3,000 QPS per project**;
  exceeding that returns `429 RESOURCE_EXHAUSTED`, requiring exponential
  backoff before retrying.
  ([Manage topic subscriptions](https://firebase.google.com/docs/cloud-messaging/manage-topic-subscriptions))
- Subscription calls are limited to **1,000 app instances per batch call**
  — already handled by this app's chunking, added in a prior change (see
  `src/pubsub.go`'s `sendTopicBatches`).
  ([Manage topics from the server](https://firebase.google.com/docs/cloud-messaging/manage-topics))
- No official page states a specific number of seconds/minutes a caller
  must wait after subscribing before a topic send will reliably reach a
  newly-subscribed device.

## What's documented about send timing (and why it matters here)

- **"Message fanout is not instantaneous"** — Google's own words, in the
  same breath as the concurrent-fanout-limit and QPS figures already
  covered in [broadcast vs. direct](./01-broadcast-vs-direct-messaging.md).
- Topics are explicitly described as **optimized for throughput, not
  latency** — the same guidance that recommends direct token targeting for
  anything time-sensitive applies directly to the "just subscribed, need it
  now" scenario.
  ([Topic Messaging](https://firebase.google.com/docs/cloud-messaging/topic-messaging))

Combining these two documented facts (subscription is asynchronous and
rate-limited; fanout is not instantaneous and not latency-optimized) leads
to a straightforward, honestly-stated conclusion: **there is no official
basis for assuming "subscribe now, broadcast now" reaches every new
subscriber immediately** — it may often work fine in practice, but nothing
in Google's documentation commits to that, and treating it as guaranteed
would be an assumption this report can't back with a source.

## Practical guidance (explicitly not an official SLA — just the logical consequence of the above)

If a workflow genuinely requires "subscribe, then notify right away,"
sidestep the uncertainty rather than trying to measure or tune around it:

- **Send directly to the token(s) you just subscribed**, in addition to or
  instead of the topic publish — you already have those tokens in hand
  from the subscribe call, so this costs nothing extra to know.
- Reserve the topic publish for reaching the **rest** of the topic's
  existing subscriber base, where "not instantaneous" is an acceptable
  trade-off because there's no adjacent "just subscribed" timing pressure.
- If topic-only delivery is a hard requirement, add a short delay before
  the first send to a brand-new subscription batch as a practical
  mitigation — but label this in code/ops docs as a heuristic, not a
  documented guarantee, since Google gives no number to target.

## Sources

- [Manage topic subscriptions](https://firebase.google.com/docs/cloud-messaging/manage-topic-subscriptions)
- [Manage topics from the server](https://firebase.google.com/docs/cloud-messaging/manage-topics)
- [Topic Messaging](https://firebase.google.com/docs/cloud-messaging/topic-messaging)
