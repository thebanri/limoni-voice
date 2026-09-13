# The relay server is part of the root Go module and shares internal packages with the client.
# Build from the repository root:
#
#   docker build -f relay-server/Dockerfile -t limoni-relay .
#
# (Railway / other PaaS: set the build context / root directory to the repository root.)
FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
COPY vendor ./vendor
COPY internal ./internal
COPY relay-server ./relay-server

RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags="-w -s" -o /relay-server ./relay-server

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /relay-server ./relay-server

EXPOSE 27850/tcp
EXPOSE 27850/udp
CMD ["./relay-server"]
