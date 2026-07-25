# syntax=docker/dockerfile:1.7
ARG GO_BASE_IMAGE
ARG RUNTIME_BASE_IMAGE
FROM ${GO_BASE_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/canvasbench-usage-proxy ./cmd/canvasbench-usage-proxy

FROM ${RUNTIME_BASE_IMAGE}
ARG SOURCE_STATE
ARG UPSTREAM_SOURCE
ARG HARNESS_VERSION
ARG ENV_CONTRACT
LABEL org.opencontainers.image.source="${UPSTREAM_SOURCE}" \
      org.opencontainers.image.revision="${SOURCE_STATE}" \
      org.opencontainers.image.version="${HARNESS_VERSION}" \
      io.drem.source-state="${SOURCE_STATE}" \
      io.drem.canvasbench.harness="usage-proxy" \
      io.drem.canvasbench.upstream="${UPSTREAM_SOURCE}" \
      io.drem.canvasbench.env-contract="${ENV_CONTRACT}"
COPY --from=build /out/canvasbench-usage-proxy /usr/local/bin/canvasbench-usage-proxy
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/canvasbench-usage-proxy"]
