# FCM pricing

## Bottom line

**Sending messages through FCM costs $0, with no volume limit, on every
Firebase plan.** This has not changed as of August 2026. There is no
per-message, per-device, or per-project usage fee for the core service —
confirmed on Firebase's own official pricing page.

## What's actually free

- **All `messages:send` calls** — direct token, topic, or condition
  targets alike — no per-message charge, no monthly cap, no overage fee.
- **Instance ID topic management calls** (`batchAdd`/`batchRemove`) — same,
  no charge.
- This holds on **both** Firebase plans:
  - **Spark** (the $0, no-payment-method-required plan)
  - **Blaze** (pay-as-you-go for *other* Firebase/GCP services) — FCM
    remains free even on Blaze; Blaze doesn't turn FCM into a metered
    product, it just means *other* things you might add (Cloud Functions,
    Firestore, etc.) get billed if they exceed their own free quotas.
- ([Firebase Pricing](https://firebase.google.com/pricing))

## How Google frames this, and why there's no "per-message rate" to quote

There is no published per-message price, discount tier, or invoice line
item to reference — because none exists. FCM is explicitly positioned as a
no-cost platform capability rather than a metered product. The per-project
**quota system** covered in
[fcm-api-reference.md §4](../fcm-api-reference.md#4-fcm-http-v1-api--quotas-and-throttling)
(default 600,000 messages/minute) exists for **system stability and
fairness**, not as a monetization lever — there is no paid tier to
purchase additional quota with money. A quota increase is **granted** by
Firebase Support based on demonstrated legitimate usage patterns, not sold.

## Where real costs actually show up in this project (none of them are FCM)

Nothing below is an FCM charge — they're the surrounding infrastructure
this service runs on, already covered in the deployment docs:

| Cost source | Where it's documented | FCM-related? |
|---|---|---|
| Compute running this Go service (Render free tier, or EC2 in production) | [`poc-deployment-explained.md`](../poc-deployment-explained.md), [`production-deployment-explained.md`](../production-deployment-explained.md) | No — hosting cost |
| Redis (Upstash, or ElastiCache in production) | Same docs | No — queue/cache cost |
| Google Analytics for Firebase (used for the Reports dashboard) | [analytics](./03-analytics-dashboard-vs-your-database.md) | Adjacent, but itself free |
| FCM (Aggregated) Data API calls | [analytics](./03-analytics-dashboard-vs-your-database.md) | No separate price identified on the official pricing page beyond standard API usage — if this is ever pulled at high volume, check the API's quota page in Google Cloud Console rather than assuming it's unlimited, since that specific figure wasn't found published |
| Apple Developer Program membership ($99/year) | N/A — not in this repo's docs | Required to *publish* an iOS app at all, not a per-notification APNs fee — APNs delivery itself is also free |

## What you're actually paying for, in this project, overall

Given all of the above: **$0 goes to Google for sending push notifications
themselves**, at any volume this project is realistically going to reach.
Every dollar in this project's infrastructure budget (see the production
deployment guide's cost breakdown) is for compute and Redis — not for FCM.

## Sources

- [Firebase Pricing](https://firebase.google.com/pricing)
- [Firebase Cloud Messaging product page](https://firebase.google.com/products/cloud-messaging)
- [FCM Throttling and Quotas](https://firebase.google.com/docs/cloud-messaging/throttling-and-quotas)
