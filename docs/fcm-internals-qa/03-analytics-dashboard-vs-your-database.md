# Analytics: Firebase dashboard vs. your own database

## Bottom line

Firebase gives you **aggregate, project-level trends** — sends, opens,
delivery-outcome percentages — through two official surfaces (a console
dashboard and a REST API). It does **not** give you per-message,
per-your-own-ID, real-time, or indefinitely-retained records. Anything you
need tied to your own business/notification IDs, or needed immediately
rather than with a multi-day delay, has to live in your own database
regardless of which Google surface you also use.

## Surface 1: the Firebase Console "Reports" tab

**Requires Google Analytics linked to the Firebase project** — without it,
this tab doesn't function at all.
([How to monitor delivery statistics](https://firebase.google.com/docs/cloud-messaging/understand-delivery))

What it shows, as a funnel over a selected time range, for messages sent
via the Notifications composer *or* the API (this app's usage counts):

| Metric | What it means |
|---|---|
| **Sends** | The message was enqueued for delivery, or successfully handed to APNs for iOS — not proof of device delivery |
| **Received** (Android only) | The message physically reached the app on the device — only reported by devices with **FCM SDK 18.0.1+** |
| **Impressions** | The notification was displayed to the user |
| **Opens** ("Message Opens") | The user tapped the notification |

This is **campaign/project-level aggregate data** — you cannot look up "did
notification `order-12345` reach device X" here. There's no per-message
lookup by your own identifiers.

## Surface 2: the FCM (Aggregated) Data API — a real, official REST API

Yes, there is an API to pull this programmatically:
`fcmdata.googleapis.com`, documented at
[Firebase Cloud Messaging Data API](https://firebase.google.com/docs/reference/fcmdata/rest).

```
GET /v1beta1/{parent=projects/*/androidApps/*}/deliveryData
```

Key facts, all from the official reference and the companion
[Understanding message delivery](https://firebase.google.com/docs/cloud-messaging/understand-delivery) guide:

- Returns **aggregated percentages**, not per-message or per-device
  records: `delivered`, `deliveredNoDelay`, `priorityLowered`,
  `droppedDeviceInactive`, and related outcome fields.
- **Rolling 7-day history**, and data can be **delayed by up to 5 days** —
  this is not a real-time or even next-day API.
- Documented and demonstrated primarily for **Android** apps
  (`androidApps` in the path). iOS message delivery data export exists
  (requires **FCM SDK 8.6.0+** on the device) but the API's documented
  coverage and examples are Android-centric — don't assume parity for iOS
  without verifying against your own traffic.
- Google's own caveat: these metrics are for **"broad trends," not 100%
  coverage of all message scenarios** — messages to inactive devices in
  particular may or may not be counted consistently.

## Surface 3: Google Analytics automatically-collected events

If the Analytics SDK is integrated in the client app (with data sharing
enabled), FCM-related user interactions are logged as standard Analytics
events — commonly cited as including `notification_receive`,
`notification_open`, `notification_dismiss`, and `notification_foreground`
— exportable to **BigQuery** for arbitrary custom querying.
([Automatically collected events](https://support.google.com/firebase/answer/6317485))

To make any of this attributable to a specific *campaign* rather than just
"some notification happened," tag the send with
`fcmOptions.analyticsLabel` (or the platform-specific
`AndroidFcmOptions`/`ApnsFcmOptions` equivalents) — a string matching
`^[a-zA-Z0-9-_.~%]{1,50}$`. Without a label, data-message statistics in
particular may not display at all.
([Analytics Labels for Messaging Campaigns](https://firebase.blog/posts/2021/09/analytics-labels-app-messaging-campaigns/))

## What Google will never give you — put these in your own database

| Need | Why Google can't provide it |
|---|---|
| Correlating delivery outcome with **your own business/notification ID** | Google's systems don't know your ID — this app doesn't even generate or send one today (a pre-existing gap noted in `docs/production-deployment-explained.md`) |
| **Real-time** per-request success/failure | The Data API is delayed up to 5 days; the Reports tab is a trend dashboard, not a live feed |
| **Per-device** outcome for a **topic** send | Never exposed, at any latency — see [broadcast vs. direct](./01-broadcast-vs-direct-messaging.md) |
| Long-term historical raw records | Data API is a 7-day rolling window; nothing longer is documented |
| The specific FCM error code your own retry logic needs *right now* | Only available from the synchronous response to your own send call — which is exactly what this app's `[AUDIT]`/`[REDIS]` logs already capture |

**Practical split:** keep this app's existing per-send audit logging
(`[AUDIT]`, `[BATCH]`, `[REDIS]` lines) as the source of truth for
"did *my* request succeed, with *my* IDs, right now." Use the Firebase
Reports tab and Data API for trend-level questions ("is our delivery rate
degrading this week") that don't need to be tied to a specific business
transaction.

## Sources

- [Understanding message delivery](https://firebase.google.com/docs/cloud-messaging/understand-delivery)
- [Firebase Cloud Messaging Data API — REST reference](https://firebase.google.com/docs/reference/fcmdata/rest)
- [Automatically collected events](https://support.google.com/firebase/answer/6317485)
- [Analytics Labels for Messaging Campaigns](https://firebase.blog/posts/2021/09/analytics-labels-app-messaging-campaigns/)
