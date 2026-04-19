# syntax=docker/dockerfile:1.7
#
# drem-csuite-watcher — HTTP bridge + per-project turn driver.
#
# Hosts the existing cmd/csuite-watcher binary inside the per-project
# compose stack (deploy/compose per-project template). It exposes the
# C-Suite HTTP bridge and pokes the persona containers when the
# orchestrator raises a new turn event.
#
# Multi-stage Go build → distroless/static runtime, matching the pattern
# established by gq.Dockerfile and spawner.Dockerfile.
#
# Build context is the repo root (needs go.mod + cmd/csuite-watcher):
#   docker build -f deploy/docker/csuite-watcher.Dockerfile \
#     -t localhost:5000/drem-csuite-watcher:latest .
#   docker push localhost:5000/drem-csuite-watcher:latest

# ---------- build stage ----------
#
# Go 1.25 matches the directive in go.mod. CGO is required by the
# underlying sqlite driver that cmd/csuite-watcher's eventbus uses, so
# build on debian rather than distroless.
FROM golang:1.25-bookworm AS build

RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# Cache module downloads separately from source.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 GOOS=linux \
    go build -trimpath -ldflags="-s -w" \
    -o /out/drem-csuite-watcher ./cmd/csuite-watcher

# ---------- runtime stage ----------
#
# Debian slim (not distroless) because the watcher binary links against
# glibc via the sqlite driver. ca-certificates is required for HTTPS
# calls to the orchestrator when DREM_ORCH_URL is https://.
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
         ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/drem-csuite-watcher /usr/local/bin/drem-csuite-watcher

# The watcher binds its HTTP bridge on :8090 by default (see
# cmd/csuite-watcher/serve.go). The per-project compose does not need
# to publish it — other services on drem-net reach it by service name.

ENTRYPOINT ["/usr/local/bin/drem-csuite-watcher"]
CMD ["serve"]
