# Local Redis + 1:1 Push Delivery Flow

This project already implements the local development version of a Redis-based 1:1 push delivery worker.

The main idea is simple:

1. Another service pushes a notification request into Redis.
2. This push delivery service keeps reading Redis.
3. Each Redis entry contains exactly one device token.
4. The service sends the push immediately to FCM.
5. If FCM fails temporarily, the message stays in Redis so it can be retried later.

This design is useful for high-priority, time-sensitive, 1:1 notifications.

---

## Where this logic lives

The actual Redis-based intake and business logic is in:

- [src/redis-consumer.go](../src/redis-consumer.go)
- [src/main.go](../src/main.go)
- [src/worker-pool.go](../src/worker-pool.go)

The important validation is in [src/redis-consumer.go](../src/redis-consumer.go):

- `validRedisNotificationRequest(...)` allows exactly one device token.
- `processMessage(...)` sends one push per message.
- Temporary FCM failures do not get acknowledged, so Redis retries them.
- Permanent errors are acknowledged and ignored.

---

## How the local flow works

### 1) Redis stores the work

The orchestration service writes a JSON payload into a Redis stream named `notification_requests`.

For local Windows testing, use PowerShell and the following command. It avoids
Windows command-line escaping issues by sending the JSON through standard input:

```powershell
$container = "<REDIS_CONTAINER_ID>"
$token = "<VALID_FCM_REGISTRATION_TOKEN>"
$payload = @{ deviceTokens = @($token); title = "Price alert"; message = "Your order was filled" } | ConvertTo-Json -Compress
$payload | docker exec -i $container redis-cli -x XADD notification_requests '*' payload
```

Important details:

- The Redis field name must be `payload`.
- The field value must be a JSON string.
- Each stream entry should contain exactly one device token.

This is intentional for 1:1 delivery.

If one Redis entry contained many device tokens, then a partial retry could send a different device again, which breaks the 1:1 guarantee.

### 2) The Go service starts a Redis consumer group

When the app starts, `main()` calls `newRedisConsumerFromEnv()` and then `consumer.start(ctx)`.

This sets up:

- a Redis client
- a stream name (`notification_requests`)
- a consumer group name (`push-delivery`)
- a consumer identity

It also calls:

```go
XGroupCreateMkStream(ctx, c.stream, c.group, "0")
```

This creates the stream and the consumer group if they do not exist yet.

### 3) The service reads messages in parallel

The consumer repeatedly calls:

```go
XReadGroup(..., Streams: []string{c.stream, ">"}, Count: 64, Block: time.Second)
```

That means:

- read new messages from the Redis stream
- assign them to this consumer instance
- process multiple messages in parallel
- avoid blocking forever while waiting for new work

Inside `processMessages(...)`, the app uses a worker pool with a semaphore-like limit:

- `parallelism: 64`
- multiple messages are processed concurrently
- each message is independently sent to FCM

### 4) Each message is validated

Before sending, the message is checked:

- payload must be a string
- JSON must decode to `NotificationRequest`
- `validRedisNotificationRequest()` must return true

This ensures the request is valid and truly 1:1.

### 5) The push is sent

The app loops through the device tokens in the request. In the Redis-based 1:1 path, the list should contain exactly one token.

Then it calls:

```go
sendFCM(token, request.Title, request.Message)
```

That function sends a Firebase Cloud Messaging HTTP request.

### 6) Temporary failures are retried automatically

This is the important part for a time-sensitive service.

If FCM replies with a temporary problem such as:

- timeout
- server issue
- temporary outage
- retryable FCM status

then the service does not ack the Redis message.

That means Redis keeps the message pending and later tries it again.

This is why the app uses `XAUTOCLAIM` and the reclaim loop:

- messages that were not finished are re-collected
- failed work is retried after a short idle period
- no work is lost if the app crashes or a send fails temporarily

### 7) Permanent failures are dropped cleanly

If the token is invalid or malformed, the code treats it as permanent and acknowledges the message.

Examples include:

- `INVALID_ARGUMENT`
- `UNREGISTERED`
- `SENDER_ID_MISMATCH`

That avoids retrying dead tokens forever.

---

## Why Redis is used here

Redis is used because it gives an easy and reliable local queue for urgent work.

It helps with:

- fast local development
- simple message-based orchestration
- durability while work is in flight
- consumer groups for distributed delivery workers
- retry behavior for failed pushes

For a local environment, Redis is the easiest way to simulate production-style ingestion without creating a full messaging platform.

---

## Local setup requirements

You need these pieces to run the service locally.

### 1) Go installed

The project is written in Go, so you need the Go toolchain.

Check:

```bash
go version
```

If this fails, install Go first.

### 2) Redis running locally

The app expects Redis on `localhost:6379` by default.

Start it with Docker:

```bash
docker run --rm -p 6379:6379 redis:7-alpine
```

Why this is required:

- the app calls `redis.NewClient(...)`
- the consumer reads from Redis
- the stream is created at runtime with `XGroupCreateMkStream`

If Redis is not running, the app fails before it can process anything.

### 3) Firebase project ID set

The service uses `PROJECT_ID` to build the FCM endpoint.

Set this in your local environment or `.env` file:

```bash
PROJECT_ID=your-firebase-project-id
PORT=8080
```

Why this is required:

- FCM API URL is built with the project ID
- without it, the service cannot send to Firebase

### 4) Firebase service account credentials

The project loads credentials from one of these options:

```bash
FIREBASE_SERVICE_ACCOUNT_JSON={...}
# or
FIREBASE_SERVICE_ACCOUNT_BASE64=...
# or
GOOGLE_APPLICATION_CREDENTIALS=/path/to/service-account.json
```

The repo also checks for a local file named `src/service-account.json`.

Why this is required:

- FCM needs authenticated access
- the app creates an OAuth2 HTTP client from the Firebase service account
- without a valid credential, the push request will fail

### 5) Local service account file if needed

You may keep credentials in a file such as:

```bash
src/service-account.json
```

This matches the code in [src/main.go](../src/main.go), which checks for that path automatically.

---

## Step-by-step local test flow

### Step 1: Start Redis

```bash
docker run --rm -p 6379:6379 redis:7-alpine
```

This gives you the queue and the stream used by the consumer.

### Step 2: Configure environment values

Create a `.env` file in `src` or export variables in your terminal.

Example:

```bash
export PROJECT_ID=your-firebase-project-id
export PORT=8080
export GOOGLE_APPLICATION_CREDENTIALS=/absolute/path/to/service-account.json
```

If you are using a file-based auth setup, the service will read it automatically.

### Step 3: Run the Go service

From the project root or from the `src` directory:

```bash
cd src
go run .
```

This starts:

- the HTTP server
- the Redis consumer
- the Redis consumer group setup
- background retry logic

You should see log lines like:

- `Redis notification consumer started...`
- `listening on :8080`

### Step 4: Publish one message to Redis

Use PowerShell with the Docker Redis container:

```powershell
$container = "<REDIS_CONTAINER_ID>"
$token = "<VALID_FCM_REGISTRATION_TOKEN>"
$payload = @{ deviceTokens = @($token); title = "Price alert"; message = "Your order was filled" } | ConvertTo-Json -Compress
$payload | docker exec -i $container redis-cli -x XADD notification_requests '*' payload
```

Why this matters:

- each entry should represent exactly one push
- the consumer reads one request at a time
- the push is sent to the single device token in that request

### Step 5: Watch the logs

The service logs:

- whether the Redis message was processed
- whether the FCM call succeeded
- whether a failure was considered retryable
- latency values

Examples of important log messages:

- `Redis notification consumer started`
- `FCM Response [...]`
- `id=... token=... success=true`
- `id=... token=... success=false retryable=true`

### Step 6: Validate retry behavior

To test the retry path, use a bad or expired token or intentionally break the FCM call.

The consumer checks whether the error is retryable:

- temporary failures remain pending
- permanent errors are acknowledged

This is the key production-grade behavior for time-sensitive delivery.

---

## Production-grade behavior in plain English

This project is built for a very specific kind of messaging:

- one target device per message
- high priority
- fast send
- retry on temporary failure
- skip permanent failures

The design keeps the message boundary at one device token per Redis stream entry because that makes retries safe.

If a single stream entry had 100 tokens, and the service failed after sending 10 of them, a retry would re-send all 100, which is not acceptable for a strict 1:1 notification system.

This is why the validation enforces:

```go
len(request.DeviceTokens) == 1
```

---

## Local testing checklist

Before testing, make sure all of the following are true:

- Redis is running on localhost:6379
- Go is installed
- Firebase project ID is set
- Firebase credentials are available
- the app is started with `go run .`
- the Redis stream name is `notification_requests`
- each payload contains exactly one device token

---

## Useful commands summary

### Start Redis

```bash
docker run --rm -p 6379:6379 redis:7-alpine
```

### Run the service

```bash
cd src
go run .
```

### Publish a test notification

```powershell
$container = "<CONTAINER_ID>"
$token = "<VALID_FCM_REGISTRATION_TOKEN>"

1..10 | ForEach-Object {
    $payload = @{
        deviceTokens = @($token)
        title        = "Load test #$_"
        message      = "High-priority test notification #$_"
    } | ConvertTo-Json -Compress

    $payload | docker exec -i $container redis-cli -x XADD notification_requests '*' payload | Out-Null
}
```

### Check Redis stream

```bash
docker exec -it <CONTAINER_ID> redis-cli XLEN notification_requests
docker exec -it <CONTAINER_ID> redis-cli XREADGROUP GROUP push-delivery consumer-1 STREAMS notification_requests 0
```

### Check if the consumer group exists

```bash
redis-cli XINFO GROUPS notification_requests
```

---

## Notes for real-world testing

For real end-to-end testing, you need a valid device token from a real mobile app or a test app registered in Firebase.

For local development, you can also use invalid tokens to test retry logic and permanent failure handling.

The important thing is not only sending the message, but checking that:

- valid pushes are delivered
- temporary failures stay in Redis for retry
- dead tokens are not retried forever

---

## Final summary

This local implementation works by combining:

- Redis Streams for reliable intake
- a consumer group for pulling work
- a Go worker model for parallel processing
- FCM for actual push delivery
- ack/retry rules for safe delivery semantics

It is a strong local prototype for a production-style 1:1 push notification service.
