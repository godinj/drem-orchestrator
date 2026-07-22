# syntax=docker/dockerfile:1.7
#
# drem-spawner — owns the Docker socket on behalf of the rest of the stack.
#
# The only process mounted with /var/run/docker.sock. Every other service
# (orchestrator, agentmon) talks to it over a JSON-RPC 2.0 Unix socket at
# /var/run/drem/spawner.sock. Multi-stage Go build → distroless runtime,
# matching the pattern established by gq.Dockerfile and sglang.Dockerfile.
#
# Build context is the repo root (needs go.mod + internal/spawner):
#   docker build -f deploy/docker/spawner.Dockerfile -t localhost:5000/drem-spawner:latest .
#   docker push localhost:5000/drem-spawner:latest
#
# Mount expectations (from deploy/compose/global.yml):
#   - /var/run/docker.sock (host → container, rw)
#   - drem-spawner-sock named volume at /var/run/drem (shared with callers)

# ---------- build stage ----------
FROM golang:1.25-alpine AS build

ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Cache module downloads separately from source.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# Static binary so the final stage can be distroless/static.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/drem-spawner ./cmd/drem-spawner

# ---------- runtime stage ----------
FROM gcr.io/distroless/static-debian12

COPY --from=build /out/drem-spawner /usr/local/bin/drem-spawner

# The spawner needs RW access to the Docker socket AND to create its own
# Unix socket under /var/run/drem. The named volume mounted there is
# shared read-write with consumer services over drem-net — filesystem
# permissions (0600 on the socket) are the only auth layer on the first
# cut, per PRD §"Networking and security". Running as root keeps both
# sockets accessible without fighting UID mapping in the named volume.

ENTRYPOINT ["/usr/local/bin/drem-spawner"]
CMD ["--socket=/var/run/drem/spawner.sock", "--runtime=docker"]
