# syntax=docker/dockerfile:1.7
#
# drem-docker-query-proxy — the only Kyle-adjacent container that touches
# the host Docker socket. Exposes a narrow, allowlisted HTTP API that Kyle
# calls via /docker/query.
#
# Multi-stage Go build → distroless runtime. Note: unlike kyle.Dockerfile
# this image runs as root because it needs to open /var/run/docker.sock,
# which is typically owned by the host docker group and not mappable into
# a distroless nonroot UID without extra setup.
#
# Build context is the repo root:
#   docker build -f deploy/docker/docker-query-proxy.Dockerfile \
#     -t localhost:5000/drem-docker-query-proxy:latest .
#   docker push localhost:5000/drem-docker-query-proxy:latest
#
# Runtime listens: 9000 (HTTP API). Docker socket bind-mounted read-only
# from the host; see deploy/compose/global.yml.

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
    go build -trimpath -ldflags="-s -w" -o /out/drem-docker-query-proxy ./cmd/drem-docker-query-proxy

# ---------- runtime stage ----------
FROM gcr.io/distroless/static-debian12

COPY --from=build /out/drem-docker-query-proxy /usr/local/bin/drem-docker-query-proxy

EXPOSE 9000

ENTRYPOINT ["/usr/local/bin/drem-docker-query-proxy"]
CMD ["--listen=:9000"]
