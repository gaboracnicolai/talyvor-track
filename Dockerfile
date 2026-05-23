# syntax=docker/dockerfile:1.6
#
# Multi-stage Go build for Talyvor Track.
#
# Stage 1 (builder): pulls modules and compiles a static binary on
# golang:1.25-alpine. Module cache + build cache mounts keep
# layer-cache rebuilds fast.
#
# Stage 2 (runtime): alpine:3.19 — ~7 MB base plus our binary, with
# wget pre-installed for the compose healthcheck. Runs as a non-root
# user. No tools, no shell helpers, nothing else.

FROM golang:1.25-alpine AS builder
WORKDIR /src

# Module manifests change less often than source — copy them first so
# `go mod download` lands in its own cacheable layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# CGO_ENABLED=0 keeps the binary fully static so we can copy it into
# alpine without pulling glibc. -trimpath strips local-build paths so
# the binary doesn't leak the builder's filesystem layout.
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-w -s -X main.version=${VERSION}" \
        -o /out/track ./cmd/track

# ---

FROM alpine:3.19 AS runtime

RUN apk add --no-cache wget ca-certificates tzdata && \
    addgroup -S track && adduser -S -G track track

WORKDIR /app
COPY --from=builder /out/track /usr/local/bin/track

# Migrations ship alongside the binary so an operator can apply them
# manually (`docker compose exec track sh` then psql). The postgres
# service auto-runs them on first boot via /docker-entrypoint-initdb.d
# in docker-compose.yaml, but having them in the image is a useful
# escape hatch.
COPY --from=builder /src/migrations /app/migrations

USER track
EXPOSE 3000

ENV TRACK_LISTEN_ADDR=:3000

HEALTHCHECK --interval=10s --timeout=5s --retries=3 \
    CMD wget -qO- http://localhost:3000/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/track"]
