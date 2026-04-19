# syntax=docker/dockerfile:1.7
#
# gq — Gemma Queue proxy: admission control + scheduling in front of SGLang.
#
# Phase 1 global infra. Multi-stage Go build → distroless runtime.
#
# Build context is the repo root (needs go.mod + internal/gq):
#   docker build -f deploy/docker/gq.Dockerfile -t localhost:5000/drem-gq:latest .
#   docker push localhost:5000/drem-gq:latest
#
# Runtime listens:
#   - 8090 (proxy)
#   - 8091 (metrics)
# Both bind 0.0.0.0 so the drem-net network can reach them.
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

# Build a fully static binary so the final stage can be distroless/static.
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

# Distroless nonroot UID is 65532:65532. The bind-mounted config file must be
# world-readable (0644) or owned by that UID.
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/gq"]
CMD ["-config=/etc/gq/gq.toml"]
