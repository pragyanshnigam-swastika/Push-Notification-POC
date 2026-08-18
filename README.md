# Push Notification Service

This repository contains a Go HTTP service that sends push notifications through Firebase Cloud Messaging (FCM) using OAuth2 credentials.

## Required Environment Variables

Create a local `.env` file from the example template and fill in the values:

```bash
PROJECT_ID=<your-firebase-project-id>
PORT=8080
```

The service account credentials are required at runtime. You should supply them through one of the following environment-driven options:

```bash
FIREBASE_SERVICE_ACCOUNT_JSON={"type":"service_account",...}
# or
FIREBASE_SERVICE_ACCOUNT_BASE64=<base64-encoded-json>
# or
GOOGLE_APPLICATION_CREDENTIALS=/path/to/service-account.json
```

The repository should not commit the real `.env` file or your Firebase service-account JSON.

## Run Locally

```bash
go run .
```

The service listens on the port from `PORT` or defaults to `8080`.

## Redis-based 1:1 delivery intake

The service consumes high-priority requests from a local Redis Stream. A Redis
consumer group gives blocking reads, load sharing across delivery-service
instances, and recovery of work from a stopped instance. Delivery is **at
least once**: an entry is acknowledged only after it succeeds (or is known to
be permanently invalid). A temporary FCM failure remains pending and is
retried after ten seconds, so the orchestration service should make each push
safe to deliver more than once.

1. Start Redis locally: `docker run --rm -p 6379:6379 redis:7-alpine`
2. Copy `src/.env.example` to `src/.env`, then set the Firebase credentials.
3. Run the service from `src`: `go run .`
4. In a separate PowerShell terminal, publish one JSON-encoded
   `NotificationRequest` per stream entry. `redis-cli -x` reads the JSON from
   standard input, avoiding Windows command-line quoting problems:

```powershell
$container = "<REDIS_CONTAINER_ID>"
$token = "<VALID_FCM_REGISTRATION_TOKEN>"
$payload = @{ deviceTokens = @($token); title = "Price alert"; message = "Your order was filled" } | ConvertTo-Json -Compress
$payload | docker exec -i $container redis-cli -x XADD notification_requests '*' payload
```

The Redis field must be named `payload`; its value is the JSON request. For a
strict 1:1 workflow, publish exactly one device token in each entry.

## Deployment

- [Free POC deployment (Render + Upstash)](docs/free-poc-deployment.md) — no credit card, no AWS account.
- [POC deployment deep dive](docs/poc-deployment-explained.md) — architecture, purpose of every component, runtime lifecycle, cost, and testing for the live Render + Upstash deployment.
- [Production deployment guide](docs/production-deployment-explained.md) — recommended EC2 + systemd + ElastiCache architecture, purpose of every component, the full build-out walkthrough, cost, security, and migration checklist.
- [FCM & Google API reference](docs/fcm-api-reference.md) — exact payload/quota/rate limits, error codes, and performance figures for every Google API this service calls, sourced and linked for verification.
- [FCM internals & integration Q&A](docs/fcm-internals-qa/) — broadcast vs. direct messaging, retry/failure handling, analytics (dashboard vs. your own database), subscribe-then-send timing, deep linking, and pricing.

## API Endpoints

- `POST /notify`
- `POST /topics/subscribe`
- `POST /notify/topic`
- `POST /topics/unsubscribe`

## Notes

- `PROJECT_ID` must match the Firebase project used by the service account.
- The Firebase service account JSON must be supplied in the deployment/runtime environment.
- Device tokens and topic registration data are request payload values and must come from clients.
