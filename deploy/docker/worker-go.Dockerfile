# syntax=docker/dockerfile:1.7
#
# drem-worker-go — Go-language worker image.
#
# Layered on top of drem-worker-base (deploy/docker/worker-base.Dockerfile).
# Adds the Go toolchain and golangci-lint, pre-creates the GOMODCACHE so the
# first agent invocation does not fight ownership, and sets DREM_LANGUAGE=go
# so the project-registry dispatcher can match worker images by language.
#
# Build:
#   docker build -t localhost:5000/drem-worker-go:latest \
#     -f deploy/docker/worker-go.Dockerfile deploy/docker/context/

FROM localhost:5000/drem-worker-base:latest

# ---- version pins ---------------------------------------------------------
# Keep in sync with the root go.mod's `go` directive and the CI lint version.
ARG GO_VERSION=1.24.4
ARG GO_SHA256=77e5da33bb72aeaef1ba4418b6fe511bc4d041873cbf82e5aa6318740df98717
ARG GOLANGCI_LINT_VERSION=1.60.3

USER root

# ---- Go toolchain ---------------------------------------------------------
# Download from the canonical go.dev/dl URL and verify against the pinned
# SHA256. The tarball extracts to /usr/local/go; PATH already includes
# /usr/local/go/bin from the base image.
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
        amd64) goarch=amd64 ;; \
        arm64) goarch=arm64 ;; \
        *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
    esac; \
    url="https://go.dev/dl/go${GO_VERSION}.linux-${goarch}.tar.gz"; \
    curl -fsSL -o /tmp/go.tgz "$url"; \
    if [ "$goarch" = "amd64" ]; then \
        echo "${GO_SHA256}  /tmp/go.tgz" | sha256sum -c -; \
    fi; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm -f /tmp/go.tgz; \
    /usr/local/go/bin/go version

# ---- golangci-lint --------------------------------------------------------
# Installed via the upstream installer script; it drops the binary into the
# supplied -b directory (/usr/local/bin is on PATH for every user).
RUN set -eux; \
    curl -fsSL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh \
        | sh -s -- -b /usr/local/bin "v${GOLANGCI_LINT_VERSION}"; \
    golangci-lint --version

# ---- Go caches ------------------------------------------------------------
# The agent runs as `drem`; create GOPATH + module cache up front with the
# right ownership so `go mod download` does not stumble on permission errors
# during the first run. GOFLAGS keeps `go build` non-interactive under tini.
ENV GOPATH=/home/drem/go \
    GOMODCACHE=/home/drem/go/pkg/mod \
    GOCACHE=/home/drem/.cache/go-build \
    GOFLAGS="-mod=mod"
RUN set -eux; \
    mkdir -p "${GOMODCACHE}" "${GOCACHE}" "${GOPATH}/bin"; \
    chown -R drem:drem /home/drem/go /home/drem/.cache

# ---- language marker ------------------------------------------------------
# Read by the spawner / project-registry dispatcher (PRD user story 33) to
# assert the image matches the project's declared language.
ENV DREM_LANGUAGE=go

USER drem
WORKDIR /home/drem/work

# ENTRYPOINT and CMD are inherited from the base (tini → worker-entrypoint).
