# syntax=docker/dockerfile:1.7
#
# drem-orch — production orchestrator image.
#
# Hosts cmd/drem (the same binary driving the TUI and the `drem project`
# CLI) stripped down to its headless orchestrator mode. This image is
# pulled by the per-project compose template (see
# internal/projects/templates/project-compose.yml.tmpl, OrchImage field).
#
# Multi-stage build:
#   - Stage 1 compiles on golang:1.25-bookworm with CGO enabled because
#     the orchestrator uses mattn/go-sqlite3 for its GORM database.
#   - Stage 2 ships debian:bookworm-slim (NOT distroless/static) since
#     the binary is glibc-linked via CGO, plus git for the merge/worker
#     helpers that shell out to it.
#
# Build context is the repo root (needs go.mod + cmd/drem):
#   docker build -f deploy/docker/orch.Dockerfile \
#     -t localhost:5000/drem-orch:latest .
#   docker push localhost:5000/drem-orch:latest

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

# Production-trajectory reliability gate. These tests deliberately execute in
# the Linux/CGO builder used for the shipped image so a host-only passing suite
# cannot hide JSON-shape or branch-admission regressions from deployment.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 GOOS=linux \
    go test ./internal/branchpolicy ./internal/orchestrator ./internal/orchhttp \
      -run 'TestAcceptRejectsRegistryActionMentionOnlyInTestName|TestAcceptAllowsSourceBackedRegistryActionAssertion|TestRejectedTestContractPersistsAcceptanceForBoundedRework|TestMarkerRegistryIncidentReplayDispatchesCorrectionAndReachesFrozenArtifact|TestProductionIncidentReplayCorpus|TestCreateTaskSpecRejectsRegistryContractWithoutExplicitRuntimeTestInstruction|TestCreateTaskSpecAcceptsSourceBackedRegistryRuntimeTestInstruction' \
      -count=1

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 GOOS=linux \
    go build -trimpath -ldflags="-s -w" \
    -o /out/drem ./cmd/drem

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 GOOS=linux \
    go build -trimpath -ldflags="-s -w" \
    -o /out/drem-ops-relay ./cmd/drem-ops-relay

# ---------- runtime stage ----------
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
         ca-certificates \
         curl \
         gcc \
         git \
         libc6-dev \
         pkg-config \
         tini \
    && rm -rf /var/lib/apt/lists/*

# curl is required by the compose healthcheck declared in
# internal/projects/templates/project-compose.yml.tmpl — Bug E W5.1 hits
# GET /projects (the cheapest endpoint with a useful error signal) every
# 30s and restarts the container on three consecutive failures. The
# binary adds ~200 KB + deps already satisfied by ca-certificates, so
# the image-size cost is negligible compared to the observability gain.

# The bare repo is bind-mounted from the host (owned by the operator's UID,
# typically 1000) into this root-owned container. Git 2.35+ refuses cross-UID
# repository access unless safe.directory lists it; setting it system-wide to
# "*" bypasses the check for every mount. This is safe because the container
# is operator-scoped — nothing inside it belongs to another principal.
RUN git config --system --add safe.directory '*' \
    && git config --system user.name drem-orchestrator \
    && git config --system user.email drem-orchestrator@localhost

COPY --from=build /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"

COPY --from=build /out/drem /usr/local/bin/drem
COPY --from=build /out/drem-ops-relay /usr/local/bin/drem-ops-relay

# The orchestrator expects /var/lib/drem (volume-mounted by the per-project
# compose) for the SQLite database and prompts cache, and /bare for the
# project's bare repository.
VOLUME ["/var/lib/drem"]
WORKDIR /var/lib/drem

# tini forwards SIGTERM to the orchestrator for graceful shutdown.
# No CMD: the per-project compose sets DREM_HEADLESS=1 + DREM_BARE_REPO
# and the binary runs the orchestrator + HTTP API. Callers that need
# a subcommand (e.g. `drem project list`) override via `command:`.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/drem"]
