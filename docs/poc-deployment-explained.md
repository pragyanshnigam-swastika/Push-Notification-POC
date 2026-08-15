# POC Deployment Deep Dive: Render + Upstash

Everything about the current live POC deployment — what it is, why it's built
this way, exactly how it behaves at runtime, what it costs, its limits, and
how to test it. This document is written to be presentable on its own: it
assumes no prior context beyond "we deployed the Go push-notification
service somewhere for free."

Companion documents:
- [free-poc-deployment.md](./free-poc-deployment.md) — the step-by-step
  "how to click through it" setup guide.
- [production-deployment-guide.md](./production-deployment-guide.md) — what
  changes for a real production deployment.

---

## 1. Executive summary

The service (a Go HTTP + Redis-consumer app that sends Firebase Cloud
Messaging pushes) is deployed as:

- **Compute:** [Render](https://render.com) — a Platform-as-a-Service (PaaS)
  that builds the Go binary from GitHub and runs it as a **free, scale-to-zero
  web service**.
- **Queue/cache:** [Upstash](https://upstash.com) — a **serverless, managed
  Redis** database, used on its **free tier**.

**Cost: $0/month**, with no credit card on file for either service, as long
as usage stays POC-shaped (occasional demos, not 24/7 traffic — details in
§7 and §8).

**The one sentence a stakeholder needs:** this deployment proves the code
works end-to-end on the public internet for free, but it deliberately trades
away 24/7 availability and consumer responsiveness to get that $0 price —
which is exactly the right trade for a demo, and exactly the wrong trade for
production. §9 in this document and all of
[production-deployment-guide.md](./production-deployment-guide.md) covers
what changes.

---

## 2. Architecture at a glance

```mermaid
flowchart LR
    subgraph Internet
        Client[HTTP client / curl / Postman]
        Upstream[Upstream service<br/>publishing urgent pushes]
    end

    subgraph Render["Render (free Web Service)"]
        App[Go binary<br/>HTTP server + Redis consumer<br/>goroutines in ONE process]
    end

    subgraph Upstash["Upstash (managed Redis, free tier)"]
        Stream[(Redis Stream<br/>notification_requests)]
    end

    subgraph Google["Google / Firebase"]
        FCM[FCM v1 API<br/>messages:send]
        IID[Instance ID API<br/>topic subscribe/unsubscribe]
    end

    Client -- "POST /notify, /topics/*" --> App
    Upstream -- "XADD payload" --> Stream
    App -- "XREADGROUP / XAUTOCLAIM (poll ~1/sec)" --> Stream
    App -- "OAuth2 service-account bearer token" --> FCM
    App -- "OAuth2 service-account bearer token" --> IID
```

Two independent things live inside the **single Go process** Render runs:

1. The **HTTP server** (`net/http` mux) serving `/notify`, `/topics/*`,
   `/healthz`, `/readyz`.
2. The **Redis consumer** (`redis-consumer.go`) — two background goroutines,
   `consumeLoop` and `reclaimLoop`, started once at process startup and
   running for the entire lifetime of the process.

This matters a lot for §7 below: there is no separate "worker" process. The
consumer's uptime is 100% tied to whether the HTTP process is alive.

---

## 3. Why Render and why Upstash

### The original plan, and why it changed

The initial recommendation was Fly.io + Upstash. Fly.io was dropped after
checking current terms: **Fly.io removed its free allowance for new accounts
in 2024.** New signups now require a credit card and get only a 7-day /
2-VM-hour trial before real billing starts (~$2–5/month minimum afterward).
That directly conflicts with "free POC, no billing anxiety," so Render
replaced it — same category of service (PaaS, builds from Git, HTTP-facing),
genuinely free, no card.

### Why Render (compute)

| Requirement | How Render meets it |
|---|---|
| Free, no credit card | Free Web Service tier: 750 shared compute-hours/month, no card required to sign up or deploy |
| Builds Go from source | Native Go buildpack (detects `go.mod`, runs a build + start command) — no Dockerfile needed, though one is available in the repo if preferred |
| Public HTTPS endpoint | Every web service gets a `*.onrender.com` URL with TLS automatically |
| Git-based deploys | Auto-deploys on push to the connected branch (what triggered the PR #1 build) |
| Logs/metrics | Built-in log viewer and basic metrics, no extra setup |

The cost of "free" here is that Render **scales the instance to zero after 15
minutes with no inbound HTTP traffic** and cold-starts it on the next
request (30–60 seconds). That's covered in depth in §7.

### Why Upstash (Redis)

| Requirement | How Upstash meets it |
|---|---|
| Free, no credit card | 500,000 commands/month, 256 MB storage, 10 GB bandwidth, indefinitely, no card |
| Drop-in Redis protocol | It's real Redis (not a REST-only reinterpretation) reachable via `host:port` + TLS + password — the app's existing `go-redis` client works unmodified |
| No infra to run | Fully managed; no VM, no patching, no persistence config to think about |
| Matches the code's existing config surface | `src/redis-consumer.go` already reads `REDIS_ADDR` / `REDIS_USERNAME` / `REDIS_PASSWORD` / `REDIS_TLS` from env — zero code changes were needed |

Upstash's pricing model is fundamentally different from traditional
"rent a VM running Redis" hosting (like AWS ElastiCache): it's **serverless
and metered per command**, not per hour of a running server. That's precisely
why an idle Upstash database costs nothing — there's no server to bill for,
only operations performed against it. This is the same reason it pairs so
well with Render's scale-to-zero model: both services are $0 while idle by
design, not by coincidence.

### Alternatives considered and rejected for the POC

| Option | Why not, for a POC |
|---|---|
| AWS EC2 + ElastiCache (`docs/ec2-systemd-deployment.md`) | Real money from hour one (~$18–25/month minimum), requires a VPC, security groups, an AWS account — overkill to *prove the code works* |
| Fly.io + Upstash | Fly.io no longer has an indefinite free tier (see above) |
| Local-only (`go run .`) | Not reachable from the internet — can't be demoed to anyone outside your machine |

---

## 4. What Render actually is, precisely

Render is a **Platform-as-a-Service**: you point it at a Git repo, it builds
your app using a detected or specified runtime, and it runs the result as a
managed, internet-facing service. You don't provision servers, configure a
reverse proxy, manage TLS certificates, or write a systemd unit — Render does
all of that.

For this deployment specifically, Render is configured as:

- **Runtime:** native Go buildpack (not the Docker route, even though a
  `Dockerfile`/`.dockerignore` exist in the repo root — Render's repo scan
  happened before those files were pushed, so it defaulted to Go).
- **Root Directory:** `src` — this is where `go.mod` lives, and Render runs
  its build/start commands from this directory.
- **Build Command:** `go build -tags netgo -ldflags '-s -w' -o app`
  - `-tags netgo` forces the pure-Go DNS resolver (avoids needing a C
    toolchain/cgo in the build image).
  - `-ldflags '-s -w'` strips debug symbols, producing a smaller binary.
- **Start Command:** `./app`
- **Instance:** Free plan — 512 MB RAM, 0.1 shared vCPU.
- **Region:** Oregon (US West) by default on the free tier.

Because `go.mod` declares `go 1.26.5`, and Go's toolchain manager
auto-downloads the exact matching Go release when it isn't already
installed, Render's build environment fetches Go 1.26.5 on demand from
`proxy.golang.org` during the build — no manual version pinning needed.

Every git push to the connected branch (`claude/repository-overview-gnsdx3`,
tracked via PR #1) triggers an automatic rebuild and redeploy.

---

## 5. What Upstash actually is, precisely

Upstash provides **serverless Redis**: a managed Redis-protocol-compatible
database that you connect to exactly like any Redis server (TCP + TLS +
`AUTH`), but that Upstash bills per operation rather than per hour of a
running instance.

Concretely, for this deployment:

- **Database:** a single Redis database, free tier, TLS-only (`rediss://`
  scheme — TLS is mandatory on Upstash's standard port, not optional).
- **Connection shape:** `host:port` + `username` (`default`) + `password`,
  fed into `redis.Options{Addr, Username, Password, TLSConfig}` in
  `src/redis-consumer.go`. **Important operational lesson learned during
  setup:** Upstash's dashboard shows a ready-made `rediss://default:<password>@host:port`
  connection string for copy-pasting into other tools. Pasting that *whole
  string* into `REDIS_ADDR` breaks `go-redis`, because `Addr` expects a bare
  `host:port` — the scheme, username, and password must be split out into
  their own env vars (`REDIS_TLS=true`, `REDIS_USERNAME=default`,
  `REDIS_PASSWORD=<password>`). This produced the deploy failure
  `too many colons in address`, fixed by correcting the env vars. Worth
  knowing cold if anyone asks "what went wrong during setup."
- **Data model in use:** exactly what the app already implements locally —
  a Redis **Stream** (`notification_requests`) with a **consumer group**
  (`push-delivery`), read via `XREADGROUP` and reclaimed via `XAUTOCLAIM`.
  Upstash didn't require any change to this; it's just Redis.

---

## 6. Environment variables actually in use

| Variable | Value in this deployment | Purpose |
|---|---|---|
| `PROJECT_ID` | Firebase project ID | Builds the FCM REST URL |
| `PORT` | `8080` | Port the Go HTTP server binds to (Render proxies its public URL to this) |
| `FIREBASE_SERVICE_ACCOUNT_BASE64` | base64 of `service-account.json` | OAuth2 credential for FCM/IID calls — chosen over a JSON env var because Render's env var UI is friendlier to single-line values |
| `REDIS_ADDR` | `<upstash-endpoint>:6379` | Bare host:port, **no scheme, no credentials embedded** |
| `REDIS_USERNAME` | `default` | Upstash's default ACL user |
| `REDIS_PASSWORD` | Upstash database password | Redis `AUTH` |
| `REDIS_TLS` | `true` | Upstash requires TLS on this port |
| `REDIS_STREAM` | `notification_requests` | Stream name |
| `REDIS_GROUP` | `push-delivery` | Consumer group name |
| `REDIS_BATCH_SIZE` | `16` | Max entries read per `XREADGROUP` call (turned down from the local default of 64 — no need for that much concurrency against a free-tier Redis for a demo) |
| `REDIS_PARALLELISM` | `16` | Max concurrent FCM sends per batch |
| `REDIS_RECLAIM_AFTER_SECONDS` | `15` | How long an unacked entry sits before `XAUTOCLAIM` retries it (must exceed the 5-second FCM HTTP timeout) |

---

## 7. Runtime lifecycle — the questions that actually matter

This is the section to have memorized for the meeting.

### Is the Go server always running?

**No.** Render's free tier spins the container down after **15 minutes with
no inbound HTTP request**. "Inactivity" is measured purely by HTTP traffic to
the public URL — background goroutine activity (like Redis polling) does
**not** count as activity and does not keep the instance awake.

When a new HTTP request arrives after a spin-down, Render cold-starts a fresh
container: pulls the built binary, starts the process, runs `main()` again
(which reconnects to Redis, re-creates the OAuth2 HTTP client, re-registers
the mux) — this takes roughly **30–60 seconds** before the request is
actually served.

### Is the Upstash Redis database always running?

**Yes, in the sense that matters — the database itself never sleeps or
disappears.** Upstash Redis is a persistently available managed service; you
can `XADD` to it at 3 a.m. and it'll be sitting there when something reads it
later. What's different from a traditional always-on Redis server is only
the **billing model**: Upstash meters by command executed, not by server
uptime, so an idle *database* costs nothing — but the database is not
"asleep" the way the Go process is. It's always reachable.

### Is the Redis consumer (the code that reads the stream) always listening?

**No — and this is the most important architectural fact to bring to the
meeting.** The consumer is just two goroutines inside the same process as
the HTTP server. When Render kills the process for inactivity, `consumeLoop`
and `reclaimLoop` stop completely along with everything else. They only
start running again when a fresh HTTP request cold-starts the container and
`main()` executes `consumer.start(ctx)` again.

Consequence: **if an upstream service `XADD`s a notification request while
the Render instance is asleep, nothing consumes it until the next HTTP
request wakes the app up.** Publishing to Redis does **not** itself count as
"activity" from Render's point of view — Render has no visibility into
Upstash at all. So a message could sit unclaimed for minutes, or indefinitely
if nobody happens to hit the public URL. For a service explicitly designed
for "time-sensitive, high-priority" delivery, this is a real gap — acceptable
for a demo, disqualifying for production. (This exact gap is the top-line
requirement driving the production design in the companion document.)

### What actually wakes the app up?

Any HTTP request to any registered route — `/healthz`, `/readyz`, `/notify`,
`/topics/*` — all count as activity and reset Render's 15-minute idle timer.
`curl https://<service>.onrender.com/healthz` is the simplest way to wake it
before a demo.

---

## 8. Cost breakdown

### Render

- Free tier gives **750 shared compute-hours/month** across your free
  services — far more than one intermittently-used demo service will ever
  consume, *because* the instance is asleep (consuming 0 hours) whenever
  nobody's hitting it.
- If the service were kept awake 24/7 for a full month, that's ~720 hours —
  right at the edge of the free pool, and Render would start charging (or
  throttling) beyond it. In practice, a demo session lasting minutes to a
  couple of hours per day uses a tiny fraction of that.
- **No credit card is required for the free Web Service plan itself.**

### Upstash

- Free tier: **500,000 commands/month**, 256 MB storage, 10 GB bandwidth.
- The consumer polls Redis roughly **twice per second while the process is
  awake** (`XREADGROUP` with a 1-second block, plus `XAUTOCLAIM` on a
  1-second ticker) — call it ~120 commands/minute of pure background
  overhead, before counting any actual notification traffic.
- Worked example: a 20-minute demo session ≈ 20 × 120 = **2,400 commands**.
  Even an unusually long 8-hour continuous test session ≈ 8 × 60 × 120 =
  **~57,600 commands** — still well under the 500K/month cap.
- **The failure mode to avoid:** pointing an uptime pinger (UptimeRobot, a
  cron `curl`, etc.) at the service to prevent cold starts. That keeps the
  process — and therefore the Redis polling — running 24/7, which is roughly
  **2 × 60 × 60 × 24 × 30 ≈ 5.2 million commands/month**, more than **10×**
  the free cap. This would either get the database throttled/blocked or
  (if a card were ever added) start generating real charges. Don't do this
  on the free tier; it's the one thing that turns "$0" into "not $0."

### Total

**$0/month**, holding as long as the service is used the way a POC is meant
to be used — occasional, attended demo sessions, not an always-on background
service.

---

## 9. Efficiency and limitations summary

| Dimension | POC state | Why it's acceptable here | Why it wouldn't be in production |
|---|---|---|---|
| Availability | Scale-to-zero, 30–60s cold start | Nobody's paying for downtime during a demo | Push notifications are explicitly "time-sensitive" per the app's own docs — a sleeping consumer defeats the purpose |
| Consumer uptime | Tied to HTTP traffic, not decoupled | Fine when a human drives the demo | A real upstream producer has no reason to also poke `/healthz` — messages could queue indefinitely unseen |
| Compute size | 0.1 vCPU / 512 MB shared | Plenty for a handful of test requests | No headroom for concurrent load or the FCM worker pool's real parallelism (50 workers) |
| Redis command budget | 500K/month metered | Comfortably covers demo usage | A real 24/7 consumer alone burns ~10× this every month |
| Security | Env vars in Render's dashboard, no auth on `/notify` | Fine when only you hold the URL | `/notify` and the topic endpoints have **no authentication at all** in the current code — anyone with the URL can send pushes through your Firebase project. This must be closed before production, regardless of hosting choice |
| Observability | Render's basic log viewer only | Enough to read `[REDIS]`/`[AUDIT]` log lines by hand | No alerting, no metrics dashboards, no automated failure detection |
| Region/latency | Single region, whatever Render/Upstash default to | Irrelevant for a demo | No control over placement relative to users or FCM |
| Deploy safety | Auto-deploy straight from a branch push | Fine for one developer iterating | No staging gate, no rollback strategy, no CI test gate before deploy |

---

## 10. Testing & validation tutorial

Every command below assumes `<url>` is the Render-assigned public URL
(`https://<service-name>.onrender.com`).

### 10.1 Wake the service and confirm liveness

```bash
curl -fsS <url>/healthz
# 200 OK with no body = process is up
```

### 10.2 Confirm Redis connectivity

```bash
curl -fsS <url>/readyz
# 200 OK = Redis ping succeeded
# 503 = Redis unreachable — check REDIS_ADDR/PASSWORD/TLS env vars first
```

### 10.3 Send a direct HTTP push

```bash
curl -X POST <url>/notify \
  -H "Content-Type: application/json" \
  -d '{"deviceTokens":["<REAL_FCM_TOKEN>"],"title":"POC test","message":"Hello from Render"}'
```
Expect a `200` with a JSON body listing per-token `success`/`retryable`/
`latencyMs`. Check the Render **Logs** tab for the matching `[AUDIT]` and
`[BATCH]` lines.

### 10.4 Exercise the Redis 1:1 delivery path

From the Upstash console's **CLI** tab, or any local `redis-cli` built with
TLS support, pointed at the Upstash database:

```bash
redis-cli -u rediss://default:<password>@<upstash-endpoint>:6379 \
  XADD notification_requests '*' payload \
  '{"deviceTokens":["<REAL_FCM_TOKEN>"],"title":"Price alert","message":"Your order was filled"}'
```

Then, **within a few seconds, send one more HTTP request to any endpoint**
(e.g. `/healthz`) if the Render instance might have been asleep — remember,
the `XADD` itself does not wake it. Check the Render logs for:

```
[REDIS] id=... token=... success=true latencyMs=...
```

### 10.5 Prove the retry behavior (temporary vs permanent failure)

To see a **permanent** failure get acknowledged and dropped, publish an entry
with an obviously malformed token (e.g. `"deviceTokens":["not-a-real-token"]`)
— FCM will return `INVALID_ARGUMENT` or `UNREGISTERED`, and the logs should
show `success=false retryable=false`, with the stream entry acknowledged
(check `XPENDING notification_requests push-delivery` afterward — it should
not appear).

To see a **retryable** failure, you can temporarily set
`REDIS_RECLAIM_AFTER_SECONDS` very low (still above 5) and watch the reclaim
loop pick the same entry back up on the next `XAUTOCLAIM` tick if the FCM
call fails transiently — logs will show the same message ID processed more
than once.

### 10.6 Inspect stream/consumer-group state directly

```bash
redis-cli -u rediss://default:<password>@<upstash-endpoint>:6379 XLEN notification_requests
redis-cli -u rediss://default:<password>@<upstash-endpoint>:6379 XPENDING notification_requests push-delivery
redis-cli -u rediss://default:<password>@<upstash-endpoint>:6379 XINFO GROUPS notification_requests
```

`XPENDING` with entries still listed after several `XAUTOCLAIM` cycles is the
signal that something is stuck (e.g. a permanent failure not classified as
such, or the app crash-looping) — worth knowing as the go-to diagnostic
command.

---

## 11. Troubleshooting case studies from this deployment

### "too many colons in address"

**Symptom:** deploy log shows
`redis: connection pool: failed to dial after 5 attempts: dial tcp: address rediss://default:...@...upstash.io:6379: too many colons in address`,
and the process exits (status 1) because `consumer.start()` fails at the
initial `Ping`.

**Root cause:** `REDIS_ADDR` was set to Upstash's full `rediss://user:pass@host:port`
connection string instead of a bare `host:port`. `go-redis`'s `Options.Addr`
field is documented to accept only `host:port` — everything else (scheme,
credentials, TLS) is separate, explicit configuration.

**Fix:** split the string into its parts across `REDIS_ADDR` (host:port
only), `REDIS_USERNAME`, `REDIS_PASSWORD`, and `REDIS_TLS=true`.

**Why it's worth knowing:** it's the natural mistake to make, because
Upstash's own dashboard prominently displays the combined connection string
for convenience with tools that *do* accept full URLs (like plain
`redis-cli -u ...`). Any future re-deploy or teammate doing this setup should
expect the same trap.

### Render defaulted to the Go native runtime instead of Docker

**Cause:** the repo was connected to Render *before* PR #1 (containing
`Dockerfile`/`.dockerignore`) was merged — Render scans the repository once
at connection time to decide the runtime, and picked "Go" because that's
what it saw then. The Dockerfile isn't wrong; Render simply never
re-evaluated its choice after the file appeared. Re-linking the repository
(or switching the Language/Runtime dropdown, where available) would let
Render pick up the Dockerfile if the Docker route is wanted later.

---

## 12. Anticipated questions — quick answers

- **"Is this what we'll ship to real users?"** No — this specific setup
  trades availability for cost. It proves the code path works publicly; it
  is not meant to stay this shape.
- **"What breaks first if we got real traffic right now?"** Two things
  simultaneously: (1) the 15-minute sleep means messages queue unseen
  between visits, and (2) there's no authentication on `/notify`, so it's
  not safe to expose broadly as-is.
- **"How much does this cost us today?"** $0, no card on file anywhere,
  provided nobody sets up a keep-alive pinger against it.
- **"What would make this always-on without leaving free tier?"** Nothing
  — scale-to-zero is inherent to Render's *free* plan. Always-on requires
  Render's paid Starter tier ($7/month) or an equivalent always-on compute
  option; see the production document for the full comparison.
- **"Is Upstash secure enough for real credentials?"** TLS is enforced by
  default and the password functions as an ACL credential; it's a reasonable
  free-tier security posture, but production should still isolate Redis
  in a private network rather than expose it over the public internet as
  we do here.
