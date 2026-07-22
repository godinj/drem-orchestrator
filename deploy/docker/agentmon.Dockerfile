# syntax=docker/dockerfile:1.7
#
# drem-agentmon — per-project sidecar that subscribes to every
# drem-labeled container's stdout, parses log lines into structured
# events, and POSTs them to the orchestrator's /internal/logs endpoint.
#
# PRD deviation (intentional, for the first cut): this container mounts
# the Docker socket read-only so the embedded container.DockerRuntime
# can subscribe to events and stream logs directly. The PRD's long-term
# design is "socket only in spawner" — agentmon talks to the spawner
# over JSON-RPC and lets the spawner mediate docker-query traffic. The
# follow-up work (see docs/prd-containerization.md "socket only in
# spawner" and cmd/gq/) routes this access through a docker-query-proxy;
# until that lands, we accept the broader read-only exposure.
#
# Build context is the repo root:
#   docker build -f deploy/docker/agentmon.Dockerfile -t localhost:5000/drem-agentmon:latest .
#   docker push localhost:5000/drem-agentmon:latest

# ---------- build stage ----------
FROM golang:1.25-alpine AS build

ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache git ca-certificates

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/drem-agentmon ./cmd/drem-agentmon

# ---------- runtime stage ----------
FROM gcr.io/distroless/static-debian12

COPY --from=build /out/drem-agentmon /usr/local/bin/drem-agentmon

# DREM_IN_CONTAINER=1 flips the default of --docker from false (host
# developer mode) to true (container mode), so the binary does the
# right thing whether launched as `docker compose up` or by hand in a
# dev shell. All other configuration comes via explicit flags driven
# by the per-project compose environment block documented in
# internal/agentmon/README.md.
ENV DREM_IN_CONTAINER=1

ENTRYPOINT ["/usr/local/bin/drem-agentmon"]
