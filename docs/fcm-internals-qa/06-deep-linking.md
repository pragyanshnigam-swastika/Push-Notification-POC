# Deep linking with FCM

## Bottom line

There is **no single, cross-platform "deep link" field** in the FCM
message schema. Web push has one official field for it
(`webpush.fcm_options.link`); Android and iOS don't, and the destination
has to travel as a custom `data` key that your app interprets itself. And
critically: **the product that used to handle "app not installed" deferred
deep linking — Firebase Dynamic Links — was fully shut down on August 25,
2025.** If your integration plan assumes FDL is available, it needs to
change before finalizing.

## How to actually pass a deep link, per platform

### Web push — there's a real, dedicated field

```json
"webpush": { "fcm_options": { "link": "https://example.com/orders/123" } }
```

- If that URL is already open in a browser tab, clicking the notification
  **brings that tab to the foreground**. Otherwise, it **opens a new tab**
  at that URL.
- **Data-only messages don't support `fcm_options.link`** — Google's own
  recommendation is to always include a `notification` payload alongside
  any `data` payload if you need click-to-open behavior.
- ([Receive messages in Web apps](https://firebase.google.com/docs/cloud-messaging/web/receive-messages))

### Android — no dedicated field; you carry the link yourself

Android has no equivalent "open this URL" field. The documented mechanics:

- **Default tap behavior:** tapping a notification opens the app's
  launcher activity. If the message had both `notification` and `data`
  payloads and arrived while the app was backgrounded, the `data` payload
  is delivered as **extras on that launcher activity's Intent**.
  ([Receive messages in Android apps](https://firebase.google.com/docs/cloud-messaging/android/receive))
- **Custom routing:** put your destination (a URL, a route name, an ID —
  your choice, there's no reserved key name) in a custom `data` field, read
  it in `onMessageReceived` (foreground) or from the launcher Intent's
  extras (background), then build your own `Intent(Intent.ACTION_VIEW, uri)`
  and start the target activity — ideally one that's registered as an
  [Android App Link](https://developer.android.com/training/app-links) so
  the same URL also works from outside the app (email, browser, etc.).
- The `AndroidNotification.click_action` field exists and can target a
  specific activity via an intent-filter, but it identifies *which
  activity to open*, not an arbitrary destination with parameters — the
  actual link/ID still needs to travel via `data`.

### iOS — same pattern as Android, no dedicated field

No structural `ApnsConfig` field for a URL either. The standard pattern is
identical in spirit: a custom `data`/APNs payload key carrying the
destination, handled by the app via
[Universal Links](https://developer.apple.com/ios/universal-links/) once
the app is foregrounded from the notification.

### Do you have to put it in the FCM payload?

**Yes, for Android and iOS** — there's no other channel; the destination
has to be part of the message. **For Web, you can use the official
`fcm_options.link` field instead of a custom key**, which is simpler and
gets you the "focus existing tab" behavior for free.

## Click analytics that come with it

If the Analytics SDK is integrated (data sharing enabled):

- **`notification_open`** is the automatically-collected event fired when
  a user taps a notification — this is your click-through signal.
  ([Automatically collected events](https://support.google.com/firebase/answer/6317485))
- Related automatically-collected events commonly cited alongside it:
  `notification_receive`, `notification_dismiss`, `notification_foreground`
  — these give you the rest of the funnel (received → shown → opened/
  dismissed) that also feeds the Firebase Console **Reports** tab covered
  in [analytics](./03-analytics-dashboard-vs-your-database.md).
- To attribute opens to a specific campaign rather than just "a
  notification happened," tag the send with `fcmOptions.analyticsLabel`
  (format `^[a-zA-Z0-9-_.~%]{1,50}$`) — without it, per-campaign breakdowns
  (especially for data messages) may not appear in the dashboard at all.
  ([Analytics Labels for Messaging Campaigns](https://firebase.blog/posts/2021/09/analytics-labels-app-messaging-campaigns/))

None of this is deep-link-specific tracking (e.g., "which product page was
opened") — that level of detail is your own app's job to log once it
receives the `data` payload and navigates, same as any in-app analytics
event.

## What happens if the app isn't installed — the part that changed

For a **native mobile push notification**, this scenario is largely moot in
the literal sense: a device can't receive an FCM push at all without the
app installed and registered for a token, so there's no "notification
exists but app is missing" state to click through.

The scenario that *does* come up in practice is different: a **marketing
link shared elsewhere** (not a push notification itself) that should open
the app if installed, or send the user to install it and then resume the
same destination after first launch — "deferred deep linking."

**This is where the landscape changed materially.** Firebase Dynamic Links
was Google's first-party product for exactly this:

- Deprecation announced **August 2023**; final shutdown **August 25,
  2025**.
- As of that date, **every FDL link — both custom-domain and
  `page.link` — stops working and returns an error** (commonly reported as
  404). This is permanent; there is no grace period remaining as of this
  writing (August 2026).
- ([Dynamic Links Deprecation FAQ](https://firebase.google.com/support/dynamic-links-faq))

**Google has not shipped a first-party replacement.** The current, official
alternatives (none Firebase-branded) are:

- **[Android App Links](https://developer.android.com/training/app-links)**
  with a Play Store fallback configured in your manifest/intent-filters, or
  **[iOS Universal Links](https://developer.apple.com/ios/universal-links/)**
  with an App Store fallback — both open a normal web page (which you
  control) when the app isn't installed, rather than deferring the deep
  link automatically after install.
- A **third-party deferred deep linking provider** (Branch, AppsFlyer,
  Adjust, and similar) if you need the full "install now, resume the exact
  destination after first launch" behavior FDL used to provide — this is a
  genuine gap in Google's own product lineup right now, not a
  recommendation for a specific vendor, just an accurate statement of what
  exists.

**Action item for finalizing FCM integration:** if any part of the planned
notification flow assumes a device without the app can still be routed
through a deep link and land in the right place post-install, that
assumption needs a concrete alternative decided before launch — it is not
something FCM or any other current Firebase product provides out of the
box.

## Sources

- [Receive messages in Web apps](https://firebase.google.com/docs/cloud-messaging/web/receive-messages)
- [Receive messages in Android apps](https://firebase.google.com/docs/cloud-messaging/android/receive)
- [Android App Links](https://developer.android.com/training/app-links)
- [Apple: Universal Links](https://developer.apple.com/ios/universal-links/)
- [Dynamic Links Deprecation FAQ](https://firebase.google.com/support/dynamic-links-faq)
- [Automatically collected events](https://support.google.com/firebase/answer/6317485)
- [Analytics Labels for Messaging Campaigns](https://firebase.blog/posts/2021/09/analytics-labels-app-messaging-campaigns/)
