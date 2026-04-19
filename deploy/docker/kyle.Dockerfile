# syntax=docker/dockerfile:1.7
#
# drem-kyle — the Drem reporting agent.
#
# Single globally-running container that aggregates state across every
# registered project by polling each orchestrator's HTTP API. Read-only by
# construction — see docs/prd-containerization.md and internal/kyle for the
# rationale. Multi-stage Go build → distroless runtime, matching the pattern
# established by gq.Dockerfile and spawner.Dockerfile.
#
# Build context is the repo root:
#   docker build -f deploy/docker/kyle.Dockerfile -t localhost:5000/drem-kyle:latest .
#   docker push localhost:5000/drem-kyle:latest
#
# Runtime listens: 8090 (HTTP API). The host-wide registry is bind-mounted
# read-only at /etc/drem/projects.toml (see deploy/compose/global.yml).

# ---------- build stage ----------
FROM golang:1.25-alpine AS build

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
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/drem-kyle ./cmd/drem-kyle

# ---------- runtime stage ----------
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/drem-kyle /usr/local/bin/drem-kyle

EXPOSE 8090

USER 65532:65532

ENTRYPOINT ["/usr/local/bin/drem-kyle"]
CMD ["--registry=/etc/drem/projects.toml", "--listen=:8090", "--docker-query-url=http://docker-query-proxy:9000"]
