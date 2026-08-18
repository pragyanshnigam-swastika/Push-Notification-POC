# All figures, one page

Every number, limit, and date from across
[`fcm-api-reference.md`](../fcm-api-reference.md) and this folder, in one
place, so you don't have to open eight files to recall one figure. Each row
links straight to its official source; each section links to the fuller
write-up if you need the "why."

## Message construction

| Figure | Value | Source |
|---|---|---|
| Max payload (data + notification), Android/Web | **4096 bytes** | [About FCM messages](https://firebase.google.com/docs/cloud-messaging/concept-options) |
| Max payload, iOS (via APNs) | **2 KB** | [Send messages to topics](https://firebase.google.com/docs/cloud-messaging/send-topic-messages) |
| Max payload composed in Firebase Console UI | **1000 characters** | [About FCM messages](https://firebase.google.com/docs/cloud-messaging/concept-options) |
| Message TTL — default & max | **4 weeks (2,419,200s)** | [Set the lifespan of a message](https://firebase.google.com/docs/cloud-messaging/customize-messages/setting-message-lifespan) |
| Message TTL — valid range | **0–2,419,200s (28 days)** | Same as above |
| Non-collapsible messages queued per device before drop | **100** | [Collapsible message types](https://firebase.google.com/docs/cloud-messaging/customize-messages/collapsible-message-types) |
| Collapsible message slots per device | **4** collapse keys | Same as above |
| Priority levels | **`normal`** (delayed in Doze) / **`high`** (wakes device, used everywhere in this app) | [Android message priority](https://firebase.google.com/docs/cloud-messaging/android-message-priority) |
| Successful send response body | `{"name": "projects/{project}/messages/{message_id}"}` — one ack per call, never per device on a topic send | [projects.messages.send](https://firebase.google.com/docs/reference/fcm/rest/v1/projects.messages/send) |

→ [Message limits & capacity](./05-api-limits-and-payload-handling.md) · [Broadcast vs. direct](./01-broadcast-vs-direct-messaging.md)

## Tokens per call — three different numbers, easy to conflate

| Figure | Value | Source |
|---|---|---|
| Tokens accepted per `messages:send` call | **Exactly 1** — no batch field exists | [Send a message using FCM HTTP v1 API](https://firebase.google.com/docs/cloud-messaging/send/v1-api) |
| Legacy API's old batch size (`registration_ids`, removed) | **1,000** | [Migrate to HTTP v1](https://firebase.google.com/docs/cloud-messaging/migrate-v1) |
| Admin SDK `sendEachForMulticast` (not used by this app) | **500** tokens/invocation | [Send messages to multiple devices](https://firebase.google.com/docs/cloud-messaging/send-message) |
| This app's `/notify` self-imposed cap (`maxDeviceTokensPerRequest`) | **1,000**, 400 if exceeded | `src/worker-pool.go` |
| This app's concurrent-send limit (`sendToManyPooled`) | **50** workers | `src/worker-pool.go` |
| IID `batchAdd`/`batchRemove` hard cap (now chunked in code) | **1,000** tokens/call | [Manage topics from the server](https://firebase.google.com/docs/cloud-messaging/manage-topics) |

→ [API limits & payload handling](./05-api-limits-and-payload-handling.md)

## Quotas and throttling

| Figure | Value | Source |
|---|---|---|
| Default per-project send quota | **600,000 messages/minute**, refills each rolling 1-minute window | [FCM Throttling and Quotas](https://firebase.google.com/docs/cloud-messaging/throttling-and-quotas) |
| Coverage of default quota | **>99%** of FCM developers | Same as above |
| Quota increase notice required | **15 days** (30 days if request is >18M msg/min) | Same as above |
| Quota increase eligibility bar | **≥80%** usage for **≥5 min/day**, **<5%** client-error ratio | Same as above |
| Temporary quota bumps allowed | **≤2/year**, **≤30 days** total | Same as above |
| Recommended traffic ramp-up | **≥60s window**, 0 → max RPS | [Scale FCM best practices](https://firebase.google.com/docs/cloud-messaging/scale-fcm) |
| Recommended retry backoff | Exponential + jitter: **1s, 2s, 4s, 8s, 16s, 32s...** | Same as above |
| `Retry-After` default if header absent | **60 seconds** | Same as above |

→ [fcm-api-reference.md §4](../fcm-api-reference.md#4-fcm-http-v1-api--quotas-and-throttling)

## Error codes — retryable or not

| Code | HTTP | Retryable? |
|---|---|---|
| `INVALID_ARGUMENT` | 400 | No |
| `UNREGISTERED` | 404 | No |
| `SENDER_ID_MISMATCH` | 403 | No |
| `THIRD_PARTY_AUTH_ERROR` | 401 | No |
| `QUOTA_EXCEEDED` | 429 | Yes, with backoff |
| `UNAVAILABLE` | 503 | Yes, with backoff |
| `INTERNAL` | 500 | Yes, with backoff |

Source: [FCM Error Codes reference](https://firebase.google.com/docs/reference/fcm/rest/v1/ErrorCode) → [fcm-api-reference.md §5](../fcm-api-reference.md#5-fcm-http-v1-api--error-codes-and-retry-semantics)

## Topics

| Figure | Value | Source |
|---|---|---|
| Topics one app instance can subscribe to | **2,000** | [Manage topics from the server](https://firebase.google.com/docs/cloud-messaging/manage-topics) |
| Tokens per `batchAdd`/`batchRemove` call | **1,000** | Same as above |
| Subscribe/unsubscribe rate | **3,000 QPS/project** | [Manage topic subscriptions](https://firebase.google.com/docs/cloud-messaging/manage-topic-subscriptions) |
| Topic name character set | `[a-zA-Z0-9-_.~%]+` | [Manage topics from the server](https://firebase.google.com/docs/cloud-messaging/manage-topics) |
| Max topics in a `condition` expression | **5** (`&&`, `\|\|`, `!`) | [Topic Messaging](https://firebase.google.com/docs/cloud-messaging/topic-messaging) |
| Concurrent fanouts per project | **1,000** | Same as above |
| Observed fanout rate (not guaranteed) | up to **~10,000 QPS/project** | Same as above |
| Subscribe-then-send delivery guarantee | **None published** — qualitative only ("not instantaneous") | [Topic Messaging](https://firebase.google.com/docs/cloud-messaging/topic-messaging) |

→ [Broadcast vs. direct](./01-broadcast-vs-direct-messaging.md) · [Subscribe-then-send latency](./04-subscribe-then-send-latency.md)

## Token lifecycle

| Figure | Value | Source |
|---|---|---|
| Recommended refresh cadence | **Monthly** (no benefit refreshing more than weekly) | [FCM registration management](https://firebase.google.com/docs/cloud-messaging/manage-tokens) |
| "Stale" defined as | No FCM connection for **>1 month** | Same as above |
| Hard expiry (Android) | **270 days** inactive | Same as above |
| Recommended staleness-tracking window | **2 months** | Same as above |

## Authentication

| Figure | Value | Source |
|---|---|---|
| OAuth2 access token lifetime (default) | **1 hour (3,600s)** | [OAuth 2.0 for Server to Server Apps](https://developers.google.com/identity/protocols/oauth2/service-account) |
| Extended lifetime (special org config only) | up to **12 hours (43,200s)** | Same as above |
| Legacy FCM API deprecation announced / sending stopped / shutdown began | **June 20, 2023 / June 20, 2024 / July 22, 2024** | [Migrate to HTTP v1](https://firebase.google.com/docs/cloud-messaging/migrate-v1) |

## Delivery performance

| Figure | Value | Source |
|---|---|---|
| FCM v1 API response latency (P95) — *API ack, not device delivery* | **<350ms** | [Understanding FCM delivery](https://firebase.blog/posts/2024/07/understand-fcm-delivery-rates/) |
| Example case-study delivery rate (illustrative only, not an SLA) | **85%+** across 50M monthly notifications | Same as above |
| Formal delivery-time SLA from Google | **None published** | — |

## Analytics

| Figure | Value | Source |
|---|---|---|
| Reports tab metrics | **Sends, Received** (Android, SDK ≥18.0.1 only)**, Impressions, Opens** | [Understanding message delivery](https://firebase.google.com/docs/cloud-messaging/understand-delivery) |
| Reports tab prerequisite | Requires **Google Analytics** linked to the project | Same as above |
| FCM Data API history window | **7 days**, rolling | [FCM Data API reference](https://firebase.google.com/docs/reference/fcmdata/rest) |
| FCM Data API latency | Data may be delayed **up to 5 days** | Same as above |
| FCM Data API platform coverage | **Android-centric**; iOS export needs FCM SDK **≥8.6.0**, documented coverage is thinner | Same as above |
| Automatically-collected Analytics events | `notification_receive`, `notification_open`, `notification_dismiss`, `notification_foreground` | [Automatically collected events](https://support.google.com/firebase/answer/6317485) |
| Analytics label format (for campaign tagging) | `^[a-zA-Z0-9-_.~%]{1,50}$` | [Analytics Labels for Messaging Campaigns](https://firebase.blog/posts/2021/09/analytics-labels-app-messaging-campaigns/) |

→ [Analytics: dashboard vs. your database](./03-analytics-dashboard-vs-your-database.md)

## Deep linking

| Figure | Value | Source |
|---|---|---|
| Web push deep-link field | `webpush.fcm_options.link` (official, web only) | [Receive messages in Web apps](https://firebase.google.com/docs/cloud-messaging/web/receive-messages) |
| Android/iOS deep-link field | **None** — must use a custom `data` key | [Receive messages in Android apps](https://firebase.google.com/docs/cloud-messaging/android/receive) |
| Firebase Dynamic Links — deprecation announced | **August 2023** | [Dynamic Links Deprecation FAQ](https://firebase.google.com/support/dynamic-links-faq) |
| Firebase Dynamic Links — shutdown date | **August 25, 2025** (all links now dead/404, permanent, no first-party replacement) | Same as above |

→ [Deep linking](./06-deep-linking.md)

## Pricing

| Figure | Value | Source |
|---|---|---|
| Cost per FCM message (any target type) | **$0** | [Firebase Pricing](https://firebase.google.com/pricing) |
| Volume cap on free sending | **None**, on Spark or Blaze | Same as above |
| Cost of Instance ID topic management calls | **$0** | Same as above |

→ [Pricing](./07-pricing.md)
