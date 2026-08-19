# FCM Internals & Integration Q&A

Seven focused answers to specific integration questions raised before
finalizing production use of FCM — each in its own file, each sourced
directly against official Google/Firebase documentation with links to
verify. Written as a research-report: skimmable, figure-first, not
exhaustive for its own sake.

Companion to [../fcm-api-reference.md](../fcm-api-reference.md), which
remains the authoritative limits/quota/error-code table — the files here
link back to it rather than repeating it wholesale.

**Trouble remembering all the numbers?** [00-all-figures-cheat-sheet.md](./00-all-figures-cheat-sheet.md)
puts every figure from every file below (plus the core limits from
`fcm-api-reference.md`) on one page, sourced and linked, so you don't have
to open eight files to recall one number.

1. [Broadcast vs. direct messaging](./01-broadcast-vs-direct-messaging.md) — which is faster, how each is acknowledged, what FCM discloses about its internals.
2. [Retry and failure handling, 1:1 and topics](./02-broadcast-retry-and-failure-handling.md) — what FCM retries automatically vs. what this app implements itself (bounded retry on the synchronous paths, exponential backoff + dead-letter on the Redis path), and the one thing no code can fix (no per-device visibility into a topic's fanout).
3. [Analytics: dashboard vs. your own database](./03-analytics-dashboard-vs-your-database.md) — every metric Google exposes, and what it will never give you.
4. [Subscribe-then-send latency](./04-subscribe-then-send-latency.md) — how fast a brand-new topic subscriber can expect to receive a message sent right after subscribing.
5. [API limits and oversized-payload handling](./05-api-limits-and-payload-handling.md) — condensed headline numbers, linking to the full reference.
6. [Deep linking](./06-deep-linking.md) — how it actually works per platform, click analytics, and the Firebase Dynamic Links shutdown.
7. [Pricing](./07-pricing.md) — what costs money, what doesn't, and why.

**On sourcing:** all figures are drawn from official `firebase.google.com`
and `developers.google.com` documentation, linked inline. This session's
network cannot directly render those domains to double-check formatting
(a sandbox restriction, not a claim about the pages' validity) — treat
each link as "go here to verify," and do a quick click-through on anything
you plan to rely on in front of stakeholders.
