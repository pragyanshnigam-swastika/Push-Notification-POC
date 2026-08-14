# Multi-stage build so the shipped image is just the compiled binary plus
# CA certificates (needed for outbound TLS to FCM and Upstash Redis).

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
