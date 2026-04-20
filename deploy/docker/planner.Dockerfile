# syntax=docker/dockerfile:1.7
#
# drem-planner — warm HTTP server.
#
# Hosts cmd/drem-planner (plans/warm-planner-pivot.md) as a long-lived
# service on drem-net. Orch POSTs /plan; the handler execs the `claude`
# CLI as a subprocess per request and returns the resulting plan.json
# inline in the response. Unlike the earlier one-shot image this one
# ships both the compiled Go binary AND the claude CLI so the handler
# can shell out without any host-side dependencies.
#
# Multi-stage build:
#   - Stage 1 compiles on golang:1.25-bookworm. CGO is on so the binary
#     picks up sqlite etc. transitively when linked against the shared
#     /internal packages; the glibc-linked binary matches the
#     debian:bookworm-slim runtime.
#   - Stage 2 ships debian:bookworm-slim + node + @anthropic-ai/claude-code
#     + the Go binary + a non-root `drem` user (UID 1000) so the bind-
#     mounted ~/.claude/.credentials.json resolves at /home/drem/.claude
#     without any CLAUDE_CONFIG_DIR override. safe.directory='*' keeps
#     git happy if a future pathway touches a bind-mounted bare repo.
#
# Build context is the repo root (needs go.mod + cmd/drem-planner):
#   docker build -f deploy/docker/planner.Dockerfile \
#     -t localhost:5000/drem-planner:latest .
#   docker push localhost:5000/drem-planner:latest
#
# Auth is subscription-only. No ANTHROPIC_API_KEY fallback. The host
# operator runs `claude login` once; the per-project compose bind-mounts
# ~/.claude/.credentials.json into /home/drem/.claude read-only, and
# the planner's /healthz validates the file is readable + `claude
# --version` returns on every probe.

ARG NODE_MAJOR=22
ARG CLAUDE_CODE_VERSION=latest

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

# ---- claude CLI -----------------------------------------------------------
# Installed globally so the drem user can invoke it without PATH massaging.
# CLAUDE_CODE_VERSION=latest on rebuilds; pin in a downstream release.
RUN set -eux; \
    if [ "$CLAUDE_CODE_VERSION" = "latest" ]; then \
        npm install -g @anthropic-ai/claude-code ; \
    else \
        npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" ; \
    fi ; \
    npm cache clean --force

# ---- git cross-UID guard --------------------------------------------------
# Any future bind-mounted repo will originate from the host operator UID.
# Git 2.35+ blocks cross-UID repository access unless safe.directory lists
# it. Match orch.Dockerfile + merger.Dockerfile.
RUN git config --system --add safe.directory '*'

# ---- non-root drem user (UID 1000) ---------------------------------------
# Matches the host operator's UID so ~/.claude/.credentials.json bind-mounts
# in as a file the drem user can read. $HOME resolves to /home/drem so the
# claude CLI finds ~/.claude without any CLAUDE_CONFIG_DIR override — same
# pattern as the csuite agents use (see ~/.drem/projects/drem-orchestrator/
# compose.override.yml).
RUN useradd --uid 1000 --home-dir /home/drem --shell /bin/bash --create-home drem \
    && mkdir -p /home/drem/.claude \
    && chown -R drem:drem /home/drem

# ---- binary ---------------------------------------------------------------
COPY --from=build /out/drem-planner /usr/local/bin/drem-planner

USER drem
WORKDIR /home/drem

EXPOSE 8090

# tini forwards SIGTERM to the planner for graceful shutdown.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/drem-planner"]
