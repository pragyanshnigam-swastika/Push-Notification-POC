# EC2 + systemd deployment guide

This is the recommended first hosted deployment for this service. The Go
binary runs continuously under `systemd`; the Redis consumer starts inside the
same process. Redis is a separate service, preferably managed ElastiCache.

## Before deploying

- Use an EC2 Linux instance and a non-root service account such as `pushsvc`.
- Put EC2 and Redis in the same private VPC. Redis must not be public.
- Permit EC2 outbound TCP 443 to Firebase and TCP 6379 (or your TLS Redis
  port) to Redis. No inbound app port is required for Redis consumption.
- Open inbound 8080 only if the HTTP endpoints are actually required. Put an
  ALB with HTTPS in front of them for external use.
- Store the Firebase service account and Redis password in a secret manager.

## Build a Linux binary from Windows PowerShell

Run this in `src` using a Go version compatible with `go.mod`:

```powershell
$env:GOOS = "linux"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
go build -o push-service .
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

Build the package (`.`), not selected `.go` files: the Redis consumer depends
on types and FCM functions defined in the other source files.

Copy `push-service` to the EC2 instance, for example to
`/opt/push-service/push-service`. Do not copy a real `.env` or service-account
file into source control or a Docker image.

## Runtime configuration

Create `/etc/push-service/push-service.env`, owned by `root:pushsvc` and mode
`0640`. Supply the secret values through your deployment system or Secrets
Manager integration.

```dotenv
PROJECT_ID=your-firebase-project-id
PORT=8080

REDIS_ADDR=your-redis.internal:6379
REDIS_USERNAME=default
REDIS_PASSWORD=replace-with-secret
REDIS_TLS=true
REDIS_STREAM=notification_requests
REDIS_GROUP=push-delivery
# Leave blank: each EC2 process creates a unique hostname/process identity.
REDIS_CONSUMER_NAME=
REDIS_BATCH_SIZE=64
REDIS_PARALLELISM=64
REDIS_RECLAIM_AFTER_SECONDS=10

# Prefer injecting this value from a secret manager.
FIREBASE_SERVICE_ACCOUNT_BASE64=replace-with-base64-service-account-json
```

Use `REDIS_TLS=false` only for local Redis or a deliberately non-TLS POC.
Set `REDIS_RECLAIM_AFTER_SECONDS` above five seconds because FCM calls time
out after five seconds.

## systemd unit

Create `/etc/systemd/system/push-service.service`:

```ini
[Unit]
Description=Push Notification Delivery Service
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=pushsvc
Group=pushsvc
WorkingDirectory=/opt/push-service
ExecStart=/opt/push-service/push-service
EnvironmentFile=/etc/push-service/push-service.env
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

Start and inspect it:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now push-service
sudo systemctl status push-service
sudo journalctl -u push-service -f
curl -fsS http://localhost:8080/healthz
curl -fsS http://localhost:8080/readyz
```

`/healthz` means the process is running. `/readyz` also checks Redis and is
the endpoint to use for a load balancer or deployment readiness check.

## Scaling

Run multiple service instances with the same `REDIS_STREAM` and `REDIS_GROUP`.
Redis consumer groups distribute new entries between them. Each instance has a
unique consumer identity and processes up to `REDIS_PARALLELISM` entries at a
time. Start with 64, measure FCM latency/errors, then tune gradually.

## Producer contract

The upstream service must use a Redis client and execute one `XADD` per device:

```text
XADD notification_requests * payload <JSON string>
```

```json
{"deviceTokens":["FCM_REGISTRATION_TOKEN"],"title":"Price alert","message":"Your order was filled"}
```

- The field is exactly `payload`.
- The JSON contains exactly one non-empty device token.
- `title` and `message` are required.
- An `XADD` success only confirms queue acceptance, not device delivery.
- Delivery is at least once; upstream business logic must tolerate duplicates.
- Include a unique business notification ID in upstream audit data. The current
  payload schema does not persist or deduplicate that ID yet.
