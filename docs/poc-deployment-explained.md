# POC Deployment Deep Dive: Render + Upstash

Everything about the current live POC deployment — what it is, why it's built
this way, exactly how it behaves at runtime, what it costs, its limits, and
how to test it. This document is written to be presentable on its own: it
assumes no prior context beyond "we deployed the Go push-notification
service somewhere for free."

Companion documents:
- [free-poc-deployment.md](./free-poc-deployment.md) — the step-by-step
  "how to click through it" setup guide.
- [production-deployment-explained.md](./production-deployment-explained.md)
  — what changes for a real production deployment.

---

## 1. Executive summary

The service (a Go HTTP + Redis-consumer app that sends Firebase Cloud
Messaging pushes) is deployed as:

- **Compute:** [Render](https://render.com) — a Platform-as-a-Service (PaaS)
  that builds the repo's `Dockerfile` into a container image and runs it as
  a **free, scale-to-zero web service**.
- **Queue/cache:** [Upstash](https://upstash.com) — a **serverless, managed
  Redis** database, used on its **free tier**.

**Cost: $0/month**, with no credit card on file for either service, as long
as usage stays POC-shaped (occasional demos, not 24/7 traffic — details in
§10 and §11).

**The one sentence a stakeholder needs:** this deployment proves the code
works end-to-end on the public internet for free, but it deliberately trades
away 24/7 availability and consumer responsiveness to get that $0 price —
which is exactly the right trade for a demo, and exactly the wrong trade for
production. §12 in this document and all of
[production-deployment-explained.md](./production-deployment-explained.md)
covers what changes.

---

## 2. Architecture at a glance

```mermaid
flowchart LR
    subgraph Internet
        Client[HTTP client / curl / Postman]
        Upstream[Upstream service<br/>publishing urgent pushes]
    end

    subgraph Render["Render (free Web Service, Docker build)"]
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

This matters a lot for §9 below: there is no separate "worker" process. The
consumer's uptime is 100% tied to whether the HTTP process is alive.

### How it works, step by step

1. A developer pushes code to the branch Render is watching, on GitHub.
2. Render detects the push and builds the repo's `Dockerfile` (§7) into a
   fresh container image, then rolls the running service over to it.
3. The new container starts: `main()` runs, loads the Firebase credential,
   connects to Upstash Redis, creates the consumer group if it doesn't exist
   yet, and starts the `consumeLoop`/`reclaimLoop` goroutines alongside the
   HTTP server — all in the same process, at the same moment.
4. From here on, two independent request paths run side by side:
   - A **client calls the HTTP API directly** (`POST /notify` or
     `/topics/*`) → handled synchronously; a response is returned
     immediately.
   - An **upstream service `XADD`s an entry into the Redis stream** → the
     consumer picks it up on its next poll, calls FCM, and either
     acknowledges the entry (success, or a permanent failure) or leaves it
     pending for `XAUTOCLAIM` to retry later (a temporary failure).
5. Both paths end the same way: an authenticated HTTPS POST to FCM's
   `messages:send` endpoint (or the IID `batchAdd`/`batchRemove` endpoints
   for topic management), using a bearer token derived from the Firebase
   service-account credential via OAuth2.

---

## 3. Purpose of every component, explicitly

Before anything else, here is exactly what each moving part is *for* — the
one-line job each one does, so there's no ambiguity about why it's in the
picture:

- **Render's purpose:** run the compiled Go binary as an internet-reachable
  service (while awake). It provides compute, a public HTTPS URL with a TLS
  certificate, and the automation that turns a `git push` into a running
  deployment. Nothing about Render is specific to this app's business
  logic — it is purely "where the process lives and how the outside world
  reaches it."
- **Upstash's purpose:** host the Redis Stream (`notification_requests`)
  that the 1:1 delivery path reads from. It is the durable "inbox" an
  upstream service drops urgent push requests into, plus the coordination
  primitives (consumer groups, `XAUTOCLAIM`) that make retries and
  crash-recovery possible. The app's HTTP endpoints (`/notify`,
  `/topics/*`) never touch Upstash at all — Redis is exclusively the
  high-priority intake lane, not general-purpose storage.
- **Docker's purpose (the `Dockerfile`/`.dockerignore`):** describe, as
  code, exactly how to turn the Go source into a runnable container image,
  so the same build produces an identical artifact wherever a container
  runtime exists — Render today, potentially ECS, Kubernetes, or a laptop
  tomorrow. Full explanation in §7.
- **GitHub's purpose:** the source of truth for the code, and the trigger
  for deployment. Render watches a branch; every push to it is what causes
  a new Docker image to be built and rolled out.
- **Firebase Cloud Messaging (FCM) / Instance ID (IID) APIs' purpose:** the
  actual delivery network. Everything else in this stack exists only to get
  a well-formed, authenticated HTTPS request to these two Google-operated
  APIs at the right time — this deployment doesn't host or control them at
  all.

---

## 4. Why Render and why Upstash

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
| Builds the app reproducibly | Builds the repo's `Dockerfile` into a container image — a fixed Go toolchain version and OS baseline every time, rather than depending on whatever build environment a language-specific buildpack happens to provide |
| Public HTTPS endpoint | Every web service gets a `*.onrender.com` URL with TLS automatically |
| Git-based deploys | Auto-deploys on every push to the connected branch — no manual build/upload step |
| Logs/metrics | Built-in log viewer and basic metrics, no extra setup |

The cost of "free" here is that Render **scales the instance to zero after 15
minutes with no inbound HTTP traffic** and cold-starts it on the next
request (30–60 seconds). That's covered in depth in §9.

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
| AWS EC2 + ElastiCache ([production-deployment-explained.md](./production-deployment-explained.md)) | Real money from hour one (~$18–25/month minimum), requires a VPC, security groups, an AWS account — overkill to *prove the code works* |
| Fly.io + Upstash | Fly.io no longer has an indefinite free tier (see above) |
| Local-only (`go run .`) | Not reachable from the internet — can't be demoed to anyone outside your machine |

---

## 5. What Render actually is, precisely

Render is a **Platform-as-a-Service**: point it at a Git repo, and it builds
and runs the result as a managed, internet-facing service — no manual server
provisioning, reverse proxy configuration, or TLS certificate management.

For this deployment specifically, Render runs the **Docker route**:

- **Runtime:** Docker. Render builds the repository's root `Dockerfile`
  directly — the multi-stage build described in §7 — rather than using a
  language-specific buildpack.
- **Dockerfile path / build context:** the default (`./Dockerfile`, repo
  root as the build context), which matches how the Dockerfile's
  `COPY src/...` instructions expect to find the source.
- **Start command:** none set — the container runs whatever the image's own
  `ENTRYPOINT` specifies (`/push-service`, the compiled binary).
- **Instance:** Free plan — 512 MB RAM, 0.1 shared vCPU.
- **Region:** Oregon (US West) by default on the free tier.

Every push to the connected branch triggers Render to rebuild the Docker
image from scratch and redeploy it automatically.

---

## 6. What Upstash actually is, precisely

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

## 7. The Dockerfile and .dockerignore

### Purpose

The `Dockerfile` (repo root) is the build recipe that turns the Go source in
`src/` into a runnable container image. The `.dockerignore` alongside it
tells Docker which files to leave out of that build entirely. Together they
make the build **reproducible and portable**: the exact same image can be
built on a developer's laptop, in CI, or by Render, and behaves identically
every time — independent of whatever Go version, OS, or tools happen to be
installed on whichever machine runs `docker build`. This is also, concretely,
**the mechanism Render uses to build and run this deployment** (§5).

### How it works

The Dockerfile uses a **multi-stage build**:

```dockerfile
FROM golang:1.26 AS builder
WORKDIR /build
COPY src/go.mod src/go.sum ./
RUN go mod download
COPY src/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /push-service .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /push-service /push-service
EXPOSE 8080
ENTRYPOINT ["/push-service"]
```

- **Stage 1 (`builder`):** starts from the official `golang:1.26` image
  (the full Go toolchain), copies just `go.mod`/`go.sum` first so dependency
  downloads are cached independently of source-code changes, then copies
  the rest of `src/` and compiles a **statically linked** binary
  (`CGO_ENABLED=0`) for Linux.
- **Stage 2 (runtime):** starts fresh from a minimal `alpine` base image —
  none of the Go toolchain, build cache, or source code from stage 1 carries
  over. Only the compiled binary and the `ca-certificates` package are
  copied in — `ca-certificates` is required because the app makes outbound
  TLS calls to both FCM and Upstash Redis, and needs a trusted root
  certificate bundle to validate those connections. The result is a small
  image with nothing in it beyond what's needed to run — no compiler, no
  source, no package-manager cache.
- **`.dockerignore`** excludes `.git`, `docs`, `tests`, markdown files, and
  — importantly — `.env`/`service-account.json` from ever being sent to the
  Docker build context, so a secret sitting in a local `.env` file can never
  accidentally end up baked into an image layer.

### Do we need it in production?

**Not for the recommended EC2 + systemd production path** (see
[production-deployment-explained.md](./production-deployment-explained.md)):
there, the binary is cross-compiled directly (`GOOS=linux GOARCH=amd64
CGO_ENABLED=0 go build`) and copied straight onto the EC2 instance, where
`systemd` runs it directly — no container runtime involved at all.

The Dockerfile still earns its place in the repo, though: it's exactly what
you'd reach for if the team later chooses a container-orchestrated
production path instead (ECS/Fargate, Kubernetes, or staying on a paid
Render plan) — the same, already-validated Dockerfile would carry over
unchanged. It's also genuinely useful today for local testing without a Go
toolchain installed at all:

```bash
docker build -t push-service .
docker run --rm -p 8080:8080 --env-file src/.env push-service
```

---

## 8. Environment variables actually in use

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

## 9. Runtime lifecycle — the questions that actually matter

This is the section to have memorized for the meeting.

### Is the Go server always running?

**No.** Render's free tier spins the container down after **15 minutes with
no inbound HTTP request**. "Inactivity" is measured purely by HTTP traffic to
the public URL — background goroutine activity (like Redis polling) does
**not** count as activity and does not keep the instance awake.

When a new HTTP request arrives after a spin-down, Render cold-starts a fresh
container: pulls the built image, starts the process, runs `main()` again
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

## 10. Cost breakdown

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

## 11. Efficiency and limitations summary

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

## 12. Testing & validation tutorial

Every command below assumes `<url>` is the Render-assigned public URL
(`https://<service-name>.onrender.com`).

### 12.1 Wake the service and confirm liveness

```bash
curl -fsS <url>/healthz
# 200 OK with no body = process is up
```

### 12.2 Confirm Redis connectivity

```bash
curl -fsS <url>/readyz
# 200 OK = Redis ping succeeded
# 503 = Redis unreachable — check REDIS_ADDR/PASSWORD/TLS env vars first
```

### 12.3 Send a direct HTTP push

```bash
curl -X POST <url>/notify \
  -H "Content-Type: application/json" \
  -d '{"deviceTokens":["<REAL_FCM_TOKEN>"],"title":"POC test","message":"Hello from Render"}'
```
Expect a `200` with a JSON body listing per-token `success`/`retryable`/
`latencyMs`. Check the Render **Logs** tab for the matching `[AUDIT]` and
`[BATCH]` lines.

### 12.4 Exercise the Redis 1:1 delivery path

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

### 12.5 Prove the retry behavior (temporary vs permanent failure)

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

### 12.6 Inspect stream/consumer-group state directly

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

## 13. Troubleshooting case study from this deployment

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

---

## 14. Anticipated questions — quick answers

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
- **"Do we need Docker in production too?"** Not for the recommended
  production path (EC2 + systemd runs the binary directly) — see §7. The
  Dockerfile stays useful as a fallback if a containerized production path
  is chosen instead.
