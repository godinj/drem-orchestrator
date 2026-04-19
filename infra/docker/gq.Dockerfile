# syntax=docker/dockerfile:1.7
#
# DEPRECATED — superseded by deploy/docker/gq.Dockerfile.
# See deploy/compose/global.yml + deploy/compose/README.md.
# Kept in place as a Phase 1 cutover fallback; delete once deploy/ is validated
# in production (tracked as a separate operational retirement, not prompt 17).
#
# gq — Gemma Queue proxy: admission control + scheduling in front of SGLang.
#
# Phase 1 seed. DRAFT. Do not build until Alex's plan lands and Kyle reviews.
#
# Build context: repo root (so the build can see go.mod + internal/gq).
# Runtime listens: 8090 (proxy), 8091 (metrics). Binds 0.0.0.0 so the compose
# network can reach it — see docker-compose.yml for env overrides.
#
# Config: optional /etc/gq/gq.toml (bind-mounted from ~/.drem-csuite/gq.toml).
# Built-in defaults apply if absent (internal/gq/config.go:124-166).

# ---------- build stage ----------
FROM golang:1.24.4-alpine AS build

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Cache module downloads separately from source.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# Build a fully static binary so the final stage can be distroless/scratch.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/gq ./cmd/gq

# ---------- runtime stage ----------
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/gq /usr/local/bin/gq

# Compose wires these to the right values; shown here for documentation.
ENV GQ_BIND_ADDR=0.0.0.0:8090 \
    GQ_METRICS_ADDR=0.0.0.0:8091 \
    GQ_UPSTREAM=http://sglang:8081

EXPOSE 8090 8091

# Distroless nonroot UID is 65532:65532. Config file, if bind-mounted, must be
# world-readable (0644) or owned by that UID.
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/gq"]
CMD ["-config=/etc/gq/gq.toml"]
