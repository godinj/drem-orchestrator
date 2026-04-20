# syntax=docker/dockerfile:1.7
#
# drem-orch-dev — development orchestrator image.
#
# Keeps the Go toolchain on disk and rebuilds the orchestrator every
# container start from /src, which the per-project compose template
# bind-mounts read-only when the project was registered with --dev
# (see internal/projects/templates/project-compose.yml.tmpl DevMode
# branch). Faster inner loop than rebuilding drem-orch:latest.
#
# Build context is the repo root:
#   docker build -f deploy/docker/orch-dev.Dockerfile \
#     -t localhost:5000/drem-orch-dev:latest .
#   docker push localhost:5000/drem-orch-dev:latest

FROM golang:1.25-bookworm

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
         ca-certificates \
         git \
         tini \
         gcc \
         libc6-dev \
    && rm -rf /var/lib/apt/lists/*

# Bind-mounted bare repos come in as the operator's UID; the container runs
# as root. Git 2.35+ blocks cross-UID repository access unless safe.directory
# lists it. Match orch.Dockerfile.
RUN git config --system --add safe.directory '*'

# Preload module cache so the first `go build` after bind-mount is fast.
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

# The compose template bind-mounts the live source at /src, overlaying
# this COPY. The preload above still wins for the module cache since
# $GOPATH lives outside /src.
COPY . .

VOLUME ["/var/lib/drem"]

ENTRYPOINT ["/usr/bin/tini", "--"]
# Recompile on every container start. Avoids the "did you forget to rebuild?"
# footgun during active development.
CMD ["sh", "-c", "cd /src && go build -o /usr/local/bin/drem ./cmd/drem && exec /usr/local/bin/drem"]
