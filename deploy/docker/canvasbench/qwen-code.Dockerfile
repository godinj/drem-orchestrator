# syntax=docker/dockerfile:1.7
ARG NODE_BASE_IMAGE
FROM ${NODE_BASE_IMAGE}
ARG SOURCE_STATE
ARG UPSTREAM_SOURCE
ARG HARNESS_VERSION
ARG ENV_CONTRACT
ARG NORMALIZER
WORKDIR /opt/harness
COPY deploy/docker/canvasbench/context/qwen-code/package.json deploy/docker/canvasbench/context/qwen-code/package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts --no-audit --no-fund \
    && test "$(node -p "require('./node_modules/@qwen-code/qwen-code/package.json').version")" = "$HARNESS_VERSION" \
    && test -n "$SOURCE_STATE" && test -n "$UPSTREAM_SOURCE" && test -n "$ENV_CONTRACT" && test -n "$NORMALIZER"
COPY deploy/docker/canvasbench/context/qwen-code/qwen-wrapper.sh /usr/local/bin/qwen
RUN chmod 0555 /usr/local/bin/qwen
LABEL org.opencontainers.image.source="${UPSTREAM_SOURCE}" \
      org.opencontainers.image.revision="${SOURCE_STATE}" \
      org.opencontainers.image.version="${HARNESS_VERSION}" \
      io.drem.source-state="${SOURCE_STATE}" \
      io.drem.canvasbench.harness="qwen-code" \
      io.drem.canvasbench.upstream="${UPSTREAM_SOURCE}" \
      io.drem.canvasbench.env-contract="${ENV_CONTRACT}" \
      io.drem.canvasbench.normalizer="${NORMALIZER}"
ENV HOME=/home/bench QWEN_CODE_SUPPRESS_YOLO_WARNING=1
USER 65532:65532
