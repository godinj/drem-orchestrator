# syntax=docker/dockerfile:1.7
ARG PYTHON_BASE_IMAGE
FROM ${PYTHON_BASE_IMAGE}
ARG SOURCE_STATE
ARG UPSTREAM_SOURCE
ARG HARNESS_VERSION
ARG ENV_CONTRACT
ARG NORMALIZER
COPY deploy/docker/canvasbench/mini-swe-agent-requirements.lock /tmp/requirements.lock
RUN python -m pip install --no-cache-dir --require-hashes -r /tmp/requirements.lock \
    && test "$(python -c 'import importlib.metadata; print(importlib.metadata.version("mini-swe-agent"))')" = "$HARNESS_VERSION" \
    && test -n "$SOURCE_STATE" && test -n "$UPSTREAM_SOURCE" && test -n "$ENV_CONTRACT" && test -n "$NORMALIZER" \
    && mv /usr/local/bin/mini /usr/local/bin/mini-real \
    && rm /tmp/requirements.lock
COPY deploy/docker/canvasbench/context/mini-swe-agent/mini-wrapper.sh /usr/local/bin/mini
RUN chmod 0555 /usr/local/bin/mini
LABEL org.opencontainers.image.source="${UPSTREAM_SOURCE}" \
      org.opencontainers.image.revision="${SOURCE_STATE}" \
      org.opencontainers.image.version="${HARNESS_VERSION}" \
      io.drem.source-state="${SOURCE_STATE}" \
      io.drem.canvasbench.harness="mini-swe-agent" \
      io.drem.canvasbench.upstream="${UPSTREAM_SOURCE}" \
      io.drem.canvasbench.env-contract="${ENV_CONTRACT}" \
      io.drem.canvasbench.normalizer="${NORMALIZER}"
ENV HOME=/home/bench
USER 65532:65532
