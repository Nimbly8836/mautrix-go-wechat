# syntax=docker/dockerfile:1
# Multi-stage build, mirroring the mautrix-* bridge images.

FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates build-base

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# SILK voice codec uses cgo.
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /usr/bin/mautrix-wechat ./cmd/bridge

# ---------------------------------------------------------------------------

FROM alpine:3.20

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache ca-certificates su-exec bash curl yq jq \
    && mkdir -p /data \
    && addgroup -g $GID bridge \
    && adduser -D -u $UID -G bridge bridge

COPY --from=builder /usr/bin/mautrix-wechat /usr/bin/mautrix-wechat
COPY docker-run.sh /usr/bin/docker-run.sh
COPY config.example.yaml /opt/example-config.yaml
RUN chmod +x /usr/bin/docker-run.sh

VOLUME /data
WORKDIR /data

ENTRYPOINT ["/usr/bin/docker-run.sh"]
