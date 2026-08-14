# Free POC Deployment: Render + Upstash

This is the zero-cost deployment path for demoing this POC publicly. It
replaces the AWS EC2 + ElastiCache path in
[ec2-systemd-deployment.md](./ec2-systemd-deployment.md) with two services
that both have a real, indefinite free tier and **do not require a credit
card**:

- **[Render](https://render.com)** — hosts the Go service as a Docker-based
  Web Service.
- **[Upstash](https://upstash.com)** — hosts Redis for the 1:1 delivery
  intake path described in
  [local-redis-1to1-delivery.md](./local-redis-1to1-delivery.md).

> Fly.io was considered first, but as of 2024 it dropped its free allowance
> for new accounts: signups now require a credit card and get only a 7-day /
> 2-VM-hour trial before billing starts. Render's free Web Service tier has
> no such catch, which is why this guide uses it instead.

## What "free" actually means here — read this first

Both free tiers are real, but each has a shape that matters for how you use
this app:

- **Render free Web Services spin down after ~15 minutes with no inbound
  HTTP traffic**, and the container process — including its background
  goroutines — stops completely. The next request triggers a cold start
  (roughly 30–60 seconds) before the service responds again.
- **Upstash free Redis is capped at 500,000 commands/month.** This app's
  Redis consumer polls constantly while it's running: `consumeLoop` issues a
  blocking `XREADGROUP` roughly once per second, and `reclaimLoop` issues an
  `XAUTOCLAIM` once per second too — about 2 commands/second, non-stop,
  whenever the process is alive. Left running 24/7 that's over 5 million
  commands/month, ten times the free quota.
- Render's spin-down is actually what saves you here: because the whole
  process stops when idle, the Redis polling only burns Upstash quota while
  you're actively demoing. **Do not** put an uptime pinger (e.g. UptimeRobot)
  in front of this service to keep it "always on" — that keeps the Redis
  polling running continuously and will exhaust the Upstash free quota
  within a couple of days.
- A demo session of a few minutes costs a few hundred to a few thousand
  Upstash commands — nowhere near the monthly cap. Just don't leave it
  running unattended for hours at a time.

## Prerequisites

- This repository pushed to GitHub (Render deploys from a GitHub repo).
- A Firebase service account JSON for your Firebase project (same one the
  local setup in the main README uses).
- The `Dockerfile` and `.dockerignore` already added at the repo root (this
  guide assumes they're in place).

## Step 1 — Create the Upstash Redis database

1. Go to [upstash.com](https://upstash.com) and sign up (GitHub OAuth works,
   no card required).
2. **Create Database** → choose **Redis** → pick a region geographically
   close to Render's region (Render's free tier defaults to Oregon, US
   West — pick an Upstash region in the US for lowest latency).
3. Open the new database's **Details** tab and copy:
   - **Endpoint** (host) and **Port**
   - **Password**
   - Confirm TLS is enabled (Upstash always requires TLS on its standard
     Redis port).
4. You now have everything needed for these env vars:
   ```
   REDIS_ADDR=<endpoint>:<port>
   REDIS_USERNAME=default
   REDIS_PASSWORD=<password>
   REDIS_TLS=true
   ```

No code changes are needed for this — `src/redis-consumer.go` already reads
`REDIS_ADDR`, `REDIS_USERNAME`, `REDIS_PASSWORD`, and `REDIS_TLS` from the
environment.

## Step 2 — Base64-encode your Firebase service account

Render env vars are single-line, so the JSON credential needs to go in as
base64 via `FIREBASE_SERVICE_ACCOUNT_BASE64` (already a supported option in
`src/main.go`).

macOS/Linux:
```bash
base64 -w0 service-account.json
```

Windows PowerShell:
```powershell
[Convert]::ToBase64String([IO.File]::ReadAllBytes("service-account.json"))
```

Save the output — you'll paste it into Render as `FIREBASE_SERVICE_ACCOUNT_BASE64`.

## Step 3 — Push the Dockerfile to GitHub

The repo root now has:

- `Dockerfile` — multi-stage build: compiles the Go binary in a `golang:1.26`
  builder stage, then copies just the binary and CA certificates into a
  small `alpine:3.20` runtime image.
- `.dockerignore` — keeps `.env`, `service-account.json`, docs, and tests out
  of the build context.

Commit and push these if you haven't already; Render builds directly from
what's in the repo.

You can sanity-check the build locally if you have Docker installed:
```bash
docker build -t push-service .
docker run --rm -p 8080:8080 \
  -e PROJECT_ID=your-firebase-project-id \
  -e FIREBASE_SERVICE_ACCOUNT_BASE64=<value from step 2> \
  -e REDIS_ADDR=<endpoint>:<port> \
  -e REDIS_USERNAME=default \
  -e REDIS_PASSWORD=<password> \
  -e REDIS_TLS=true \
  push-service
```

## Step 4 — Create the Render Web Service

1. Sign up at [render.com](https://render.com) (GitHub OAuth, no card needed
   for the free tier).
2. **New +** → **Web Service** → connect your GitHub account and select this
   repository.
3. Render should auto-detect the root `Dockerfile` and set **Runtime:
   Docker**. If asked for a Dockerfile path, it's `./Dockerfile` with build
   context `.` (the repo root) — this matters because the Dockerfile's
   `COPY src/...` lines expect the repo root as the build context, not the
   `src` directory.
4. **Instance Type:** Free.
5. Under **Environment**, add these variables:

   | Key | Value |
   |---|---|
   | `PROJECT_ID` | your Firebase project ID |
   | `PORT` | `8080` |
   | `FIREBASE_SERVICE_ACCOUNT_BASE64` | value from Step 2 |
   | `REDIS_ADDR` | `<endpoint>:<port>` from Step 1 |
   | `REDIS_USERNAME` | `default` |
   | `REDIS_PASSWORD` | value from Step 1 |
   | `REDIS_TLS` | `true` |
   | `REDIS_STREAM` | `notification_requests` |
   | `REDIS_GROUP` | `push-delivery` |
   | `REDIS_BATCH_SIZE` | `16` |
   | `REDIS_PARALLELISM` | `16` |
   | `REDIS_RECLAIM_AFTER_SECONDS` | `15` |

   (Batch size/parallelism are turned down from the local defaults of 64 —
   there's no need for that much concurrency against a free-tier Redis and
   a POC demo audience.)

6. Click **Create Web Service**. Render builds the Docker image and deploys
   it; watch the build logs for `Redis notification consumer started...`
   and `listening on :8080`, which confirm both the Redis connection and the
   HTTP server came up cleanly.

## Step 5 — Verify the deployment

Render gives you a public URL like `https://<service-name>.onrender.com`.

```bash
curl -fsS https://<service-name>.onrender.com/healthz
curl -fsS https://<service-name>.onrender.com/readyz
```

`/healthz` confirms the process is up. `/readyz` also pings Redis — if this
fails, double check the `REDIS_*` env vars against the Upstash dashboard.

Test the HTTP notify path:
```bash
curl -X POST https://<service-name>.onrender.com/notify \
  -H "Content-Type: application/json" \
  -d '{"deviceTokens":["<A_REAL_FCM_TOKEN>"],"title":"Test","message":"Hello from Render"}'
```

Test the Redis 1:1 delivery path by publishing one entry to the Upstash
stream. From the Upstash console's **CLI** tab (or any `redis-cli` pointed at
the Upstash endpoint with TLS):
```bash
redis-cli -u rediss://default:<password>@<endpoint>:<port> \
  XADD notification_requests '*' payload '{"deviceTokens":["<A_REAL_FCM_TOKEN>"],"title":"Price alert","message":"Your order was filled"}'
```
Then check the Render service logs for a `[REDIS] id=... success=true` line.

## What you now have

- A publicly reachable HTTP API (`/notify`, `/topics/*`) running on Render's
  free tier.
- A working Redis Streams 1:1 delivery path backed by Upstash's free tier.
- No AWS account, no credit card, no ongoing cost — as long as you don't
  keep the service pinned awake 24/7 (see the quota note above).

## Moving beyond the POC

When this is ready to be a real, always-on service, revisit
[ec2-systemd-deployment.md](./ec2-systemd-deployment.md) (or a paid Render
plan + a paid Upstash/ElastiCache tier) — at that point the 15-minute
spin-down and the 500K-command cap stop being acceptable, and "always
running" becomes a deliberate infrastructure decision rather than a side
effect of a free trial.
