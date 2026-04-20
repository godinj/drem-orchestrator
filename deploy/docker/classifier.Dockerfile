# syntax=docker/dockerfile:1.7
#
# drem-classifier — warm direct-classifier HTTP server.
#
# Hosts cmd/drem-classifier (see plans/warm-direct-classifier.md) as a long-
# lived service on drem-net. The orch container POSTs classify jobs here
# instead of running the classifier inline; this bounds the orch process'
# thread count and isolates classifier failure modes behind compose's
# restart-policy + /healthz gate.
#
# Multi-stage build:
#   - Stage 1 compiles on golang:1.25-bookworm. The classifier path has no
#     CGO call sites of its own, but go build at the workspace root pulls
#     in go-sqlite3 transitively (via internal/agent), so CGO stays on and
#     the glibc-linked binary matches the debian:bookworm-slim runtime.
#   - Stage 2 ships debian:bookworm-slim with ca-certificates for HTTPS
#     upstream probes, tini for graceful SIGTERM forwarding, and wget so
#     the compose-level healthcheck can hit /healthz. git is added (not
#     because the binary shells out, but to match orch.Dockerfile semantics
#     so the safe.directory='*' escape hatch works if any future classifier
#     pathway touches a bind-mounted repo).
#
# Build context is the repo root (needs go.mod + cmd/drem-classifier):
#   docker build -f deploy/docker/classifier.Dockerfile \
#     -t localhost:5000/drem-classifier:latest .
#   docker push localhost:5000/drem-classifier:latest

# ---------- build stage ----------
FROM golang:1.25-bookworm AS build

RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 GOOS=linux \
    go build -trimpath -ldflags="-s -w" \
    -o /out/drem-classifier ./cmd/drem-classifier

# ---------- runtime stage ----------
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
         ca-certificates \
         git \
         tini \
         wget \
    && rm -rf /var/lib/apt/lists/*

# Match orch.Dockerfile: git config --system --add safe.directory '*' so any
# future bind-mount pathway doesn't trip git's cross-UID repository guard.
# See orch.Dockerfile for the rationale.
RUN git config --system --add safe.directory '*'

COPY --from=build /out/drem-classifier /usr/local/bin/drem-classifier

# The classifier has no persistent state; every request is self-contained.
EXPOSE 8090

# tini forwards SIGTERM to the classifier for graceful shutdown.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/drem-classifier"]
