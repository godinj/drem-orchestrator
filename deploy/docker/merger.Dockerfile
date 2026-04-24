# syntax=docker/dockerfile:1.7
#
# drem-merger — per-task merge container.
#
# Clones the integration branch into /work, merges a feature branch, runs
# the project's test command, and on success pushes integration + deletes
# the feature branch. See internal/merger and cmd/drem-merger for the full
# flow.
#
# Build context is the repo root (needs go.mod + internal/merger):
#   docker build -f deploy/docker/merger.Dockerfile \
#     -t localhost:5000/drem-merger:latest .
#   docker push localhost:5000/drem-merger:latest
#
# The runtime image ships git because the binary shells out to it for every
# clone / fetch / merge / push. ca-certificates is required for HTTPS remotes
# and for POSTing merge_result records to the orchestrator.

# ---------- build stage ----------
#
# Build on Debian so the glibc-linked binary matches the debian:bookworm-slim
# runtime. CGO is required by go-sqlite3 for the gitref registry shim.
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
    -o /out/drem-merger ./cmd/drem-merger

# ---------- runtime stage ----------
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
         git \
         ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# The merger clones / pushes against a bare repo bind-mounted from the host
# (operator UID) into this root-owned container. Git 2.35+ blocks cross-UID
# repository access unless safe.directory lists it. See orch.Dockerfile.
RUN git config --system --add safe.directory '*' \
    && git config --system user.name "${DREM_GIT_USER_NAME:-drem-merger}" \
    && git config --system user.email "${DREM_GIT_USER_EMAIL:-drem-merger@localhost}"

COPY --from=build /out/drem-merger /usr/local/bin/drem-merger

# /work is the ephemeral clone workspace; /bare is the bind-mounted bare
# repository. The merger pool caller passes these as flag values.
VOLUME ["/work"]
WORKDIR /work

ENTRYPOINT ["/usr/local/bin/drem-merger"]
