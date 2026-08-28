# Multi-stage build for ultra-lightweight, zero-dependency relay server (~7MB)
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Copy only relay-server module to isolate from desktop TUI client
COPY relay-server/go.mod relay-server/go.sum ./
RUN go mod download

COPY relay-server/*.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /relay-server .

# Minimal scratch container
FROM scratch

COPY --from=builder /relay-server /relay-server

EXPOSE 8080
ENV PORT=8080

ENTRYPOINT ["/relay-server"]
