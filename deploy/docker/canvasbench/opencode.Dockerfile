# syntax=docker/dockerfile:1.7
ARG NODE_BASE_IMAGE
FROM ${NODE_BASE_IMAGE}
ARG SOURCE_STATE
ARG UPSTREAM_SOURCE
ARG HARNESS_VERSION
ARG ENV_CONTRACT
ARG NORMALIZER
WORKDIR /opt/harness
COPY deploy/docker/canvasbench/context/opencode/package.json deploy/docker/canvasbench/context/opencode/package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts --no-audit --no-fund \
    && install -m 0555 node_modules/opencode-linux-x64-baseline/bin/opencode node_modules/opencode-ai/bin/opencode.exe \
    && test "$(node -p "require('./node_modules/opencode-ai/package.json').version")" = "$HARNESS_VERSION" \
    && test -n "$SOURCE_STATE" && test -n "$UPSTREAM_SOURCE" && test -n "$ENV_CONTRACT" && test -n "$NORMALIZER"
COPY deploy/docker/canvasbench/context/opencode/opencode-wrapper.mjs /usr/local/bin/opencode
RUN chmod 0555 /usr/local/bin/opencode
LABEL org.opencontainers.image.source="${UPSTREAM_SOURCE}" \
      org.opencontainers.image.revision="${SOURCE_STATE}" \
      org.opencontainers.image.version="${HARNESS_VERSION}" \
      io.drem.source-state="${SOURCE_STATE}" \
      io.drem.canvasbench.harness="opencode" \
      io.drem.canvasbench.upstream="${UPSTREAM_SOURCE}" \
      io.drem.canvasbench.env-contract="${ENV_CONTRACT}" \
      io.drem.canvasbench.normalizer="${NORMALIZER}"
ENV HOME=/home/bench OPENCODE_DISABLE_AUTOUPDATE=true OPENCODE_DISABLE_PRUNE=true
USER 65532:65532
