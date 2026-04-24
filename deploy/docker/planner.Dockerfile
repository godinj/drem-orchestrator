# syntax=docker/dockerfile:1.7
#
# drem-planner — warm HTTP server.
#
# Hosts cmd/drem-planner (plans/warm-planner-pivot.md) as a long-lived
# service on drem-net. Orch POSTs /plan; the handler execs the `codex`
# CLI as a subprocess per request and returns the resulting plan.json
# inline in the response. Unlike the earlier one-shot image this one
# ships both the compiled Go binary AND the Codex CLI so the handler
# can shell out without any host-side dependencies.
#
# Multi-stage build:
#   - Stage 1 compiles on golang:1.25-bookworm. CGO is on so the binary
#     picks up sqlite etc. transitively when linked against the shared
#     /internal packages; the glibc-linked binary matches the
#     debian:bookworm-slim runtime.
#   - Stage 2 ships debian:bookworm-slim + node + @openai/codex
#     + the Go binary + a non-root `drem` user (UID 1000) so the bind-
#     mounted ~/.codex/auth.json resolves at /home/drem/.codex/auth.json.
#     safe.directory='*' keeps
#     git happy if a future pathway touches a bind-mounted bare repo.
#
# Build context is the repo root (needs go.mod + cmd/drem-planner):
#   docker build -f deploy/docker/planner.Dockerfile \
#     -t localhost:5000/drem-planner:latest .
#   docker push localhost:5000/drem-planner:latest
#
# Auth comes from the host operator's Codex login. The per-project compose
# bind-mounts ~/.codex/auth.json into /home/drem/.codex read-only.

ARG NODE_MAJOR=22
ARG CLAUDE_CODE_VERSION=latest
ARG CODEX_VERSION=latest

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
    -o /out/drem-planner ./cmd/drem-planner

# ---------- runtime stage ----------
FROM debian:bookworm-slim

ARG NODE_MAJOR
ARG CLAUDE_CODE_VERSION
ARG CODEX_VERSION

# ---- base packages --------------------------------------------------------
# curl + gnupg for NodeSource repo; jq + tini for runtime. wget for the
# compose-level healthcheck's /healthz probe.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
         ca-certificates \
         curl \
         git \
         gnupg \
         jq \
         tini \
         wget \
    && rm -rf /var/lib/apt/lists/*

# ---- Node.js + npm (NodeSource) -------------------------------------------
RUN set -eux; \
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - ; \
    apt-get install -y --no-install-recommends nodejs ; \
    rm -rf /var/lib/apt/lists/*

# ---- Codex CLI ------------------------------------------------------------
# Installed globally so the drem user can invoke it without PATH massaging.
RUN set -eux; \
    if [ "$CODEX_VERSION" = "latest" ]; then \
        npm install -g @openai/codex ; \
    else \
        npm install -g "@openai/codex@${CODEX_VERSION}" ; \
    fi ; \
    npm cache clean --force

# ---- git cross-UID guard --------------------------------------------------
# Any future bind-mounted repo will originate from the host operator UID.
# Git 2.35+ blocks cross-UID repository access unless safe.directory lists
# it. Match orch.Dockerfile + merger.Dockerfile.
RUN git config --system --add safe.directory '*'

# ---- non-root drem user (UID 1000) ---------------------------------------
# Matches the host operator's UID so ~/.codex/auth.json bind-mounts in as a
# file the drem user can read.
RUN useradd --uid 1000 --home-dir /home/drem --shell /bin/bash --create-home drem \
    && mkdir -p /home/drem/.codex \
    && chown -R drem:drem /home/drem

# ---- binary ---------------------------------------------------------------
COPY --from=build /out/drem-planner /usr/local/bin/drem-planner

USER drem
WORKDIR /home/drem

EXPOSE 8090

# tini forwards SIGTERM to the planner for graceful shutdown.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/drem-planner"]
