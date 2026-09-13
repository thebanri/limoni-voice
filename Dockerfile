# Multi-stage build for the ultra-lightweight, zero-dependency relay server (~8MB)
# Build context must be the repository root (the relay shares internal packages with the client).
FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
COPY vendor ./vendor
COPY internal ./internal
COPY relay-server ./relay-server

RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags="-w -s" -o /relay-server ./relay-server

# Minimal scratch container
FROM scratch

COPY --from=builder /relay-server /relay-server

# WebSocket signalling (TCP) and low-latency UDP relay share the same port number
EXPOSE 27850/tcp
EXPOSE 27850/udp
ENV PORT=27850

ENTRYPOINT ["/relay-server"]
