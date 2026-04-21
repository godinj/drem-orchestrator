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
#
# The runtime user is `drem` with uid/gid 1000 — matching the persona
# images and the host operator's `godinj` uid. This is load-bearing:
# files the watcher writes into /csuite/<persona>/inbox/ are owned by
# uid 1000 on the host, so Kyle's host-side Go binary (running as
# uid 1000) can archive or delete them without sudo. Running as root
# would leave root-owned files in the operator's home tree — a
# footgun that blocks Kyle's poll loop. See
# plans/csuite-watcher-outbox-routing.md §7a / commit 7.
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
         ca-certificates \
         gosu \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 1000 drem \
    && useradd --uid 1000 --gid 1000 --no-create-home --shell /usr/sbin/nologin drem \
    && mkdir -p /var/lib/watcher /var/lib/drem /csuite \
    && chown -R drem:drem /var/lib/watcher /var/lib/drem /csuite

COPY --from=build /out/drem-csuite-watcher /usr/local/bin/drem-csuite-watcher

# entrypoint.sh chowns the named volume at /var/lib/watcher before
# dropping to uid 1000. Docker-created named volumes come up owned by
# root on first mount; a watcher process running as uid 1000 would
# fail to create deliveries.db under that volume without the chown.
# Using gosu (not sudo) so the drop-to-drem step doesn't require a
# TTY and doesn't inherit root's env.
COPY deploy/docker/context/csuite-watcher-entrypoint.sh /usr/local/bin/csuite-watcher-entrypoint.sh
RUN chmod 0755 /usr/local/bin/csuite-watcher-entrypoint.sh

# The watcher binds its HTTP bridge on :8090 by default (see
# cmd/csuite-watcher/serve.go). The per-project compose does not need
# to publish it — other services on drem-net reach it by service name.
#
# Runtime configuration (12-factor env-var overrides — see
# cmd/csuite-watcher/serve.go applyServeEnvOverrides). Required so this
# image works without a drem.toml mount in the per-project compose stack:
#
#   DREM_BEARER_TOKEN   required; auth token for the bridge HTTP API.
#                       No default. Container exits 1 if unset and no toml.
#   DREM_LISTEN_ADDR    defaults to :8080 in the binary; the per-project
#                       compose overrides to :8090 to match the historical
#                       bridge port.
#   DREM_DB_PATH        defaults to ~/.drem-csuite/csuite.db; compose
#                       overrides to /var/lib/drem/csuite.db.
#   CSUITE_WATCHER_TOKEN  required for /deliver and /rescan. Shared secret
#                       with the persona containers.
#   CSUITE_WATCHER_DB_PATH  path of the delivery ledger SQLite file.
#                       Compose overrides to /var/lib/watcher/deliveries.db.
#
# Precedence: env > drem.toml [serve] > built-in default.

ENTRYPOINT ["/usr/local/bin/csuite-watcher-entrypoint.sh"]
CMD ["serve"]
