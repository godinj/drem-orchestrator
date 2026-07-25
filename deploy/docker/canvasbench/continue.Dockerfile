# syntax=docker/dockerfile:1.7
ARG NODE_BASE_IMAGE
FROM ${NODE_BASE_IMAGE}
ARG SOURCE_STATE
ARG UPSTREAM_SOURCE
ARG UPSTREAM_INTEGRITY
ARG HARNESS_VERSION
ARG ENV_CONTRACT
ARG NORMALIZER
WORKDIR /opt/harness
COPY deploy/docker/canvasbench/context/continue/package.json deploy/docker/canvasbench/context/continue/package-lock.json ./
RUN npm ci --ignore-scripts --omit=dev --no-audit --no-fund \
    && node -e 'const [version, integrity] = process.argv.slice(1); const pkg = require("./node_modules/@continuedev/cli/package.json"); const lock = require("./package-lock.json"); if (pkg.version !== version || lock.packages["node_modules/@continuedev/cli"].integrity !== integrity) process.exit(1)' "$HARNESS_VERSION" "$UPSTREAM_INTEGRITY" \
    && cp node_modules/@continuedev/cli/dist/cn.js /opt/harness/cn-real.js \
    && chmod 0555 /opt/harness/cn-real.js \
    && test -n "$SOURCE_STATE" && test -n "$UPSTREAM_SOURCE" && test -n "$ENV_CONTRACT" && test -n "$NORMALIZER"
COPY deploy/docker/canvasbench/context/continue/continue-wrapper.py /usr/local/bin/cn
RUN chmod 0555 /usr/local/bin/cn
LABEL org.opencontainers.image.source="${UPSTREAM_SOURCE}" \
      org.opencontainers.image.revision="${SOURCE_STATE}" \
      org.opencontainers.image.version="${HARNESS_VERSION}" \
      io.drem.source-state="${SOURCE_STATE}" \
      io.drem.canvasbench.harness="continue" \
      io.drem.canvasbench.upstream="${UPSTREAM_SOURCE}" \
      io.drem.canvasbench.env-contract="${ENV_CONTRACT}" \
      io.drem.canvasbench.normalizer="${NORMALIZER}"
ENV HOME=/home/bench
USER 65532:65532
