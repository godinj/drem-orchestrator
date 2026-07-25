# syntax=docker/dockerfile:1.7
ARG PYTHON_BASE_IMAGE
FROM ${PYTHON_BASE_IMAGE}
ARG SOURCE_STATE
ARG UPSTREAM_SOURCE
ARG UPSTREAM_INTEGRITY
ARG HARNESS_VERSION
ARG ENV_CONTRACT
ARG NORMALIZER
WORKDIR /opt/harness
RUN apt-get update \
    && apt-get install -y --no-install-recommends git \
    && rm -rf /var/lib/apt/lists/*
COPY deploy/docker/canvasbench/swe-agent-requirements.lock /tmp/requirements.lock
RUN python -m venv /opt/venv \
    && /opt/venv/bin/pip install --no-cache-dir --require-hashes -r /tmp/requirements.lock \
    && python - "$UPSTREAM_SOURCE" "$UPSTREAM_INTEGRITY" <<'PY'
import hashlib
from pathlib import Path
import sys
from urllib.request import urlopen

source, expected = sys.argv[1:]
archive = Path("/tmp/swe-agent.tar.gz")
archive.write_bytes(urlopen(source, timeout=120).read())
actual = "sha256:" + hashlib.sha256(archive.read_bytes()).hexdigest()
if actual != expected:
    raise SystemExit(f"SWE-agent archive integrity mismatch: {actual}")
PY
RUN mkdir -p /opt/swe-agent \
    && tar -xzf /tmp/swe-agent.tar.gz --strip-components=1 -C /opt/swe-agent \
    && test "$(cd /opt/swe-agent && PYTHONPATH=/opt/swe-agent /opt/venv/bin/python -c 'import sweagent; print(sweagent.__version__)')" = "$HARNESS_VERSION" \
    && test -n "$SOURCE_STATE" && test -n "$ENV_CONTRACT" && test -n "$NORMALIZER" \
    && rm -f /tmp/requirements.lock /tmp/swe-agent.tar.gz
COPY deploy/docker/canvasbench/context/swe-agent/swe-agent-wrapper.py /usr/local/bin/sweagent
RUN chmod 0555 /usr/local/bin/sweagent \
    && mkdir -p /home/bench \
    && chown 65532:65532 /home/bench
LABEL org.opencontainers.image.source="${UPSTREAM_SOURCE}" \
      org.opencontainers.image.revision="${SOURCE_STATE}" \
      org.opencontainers.image.version="${HARNESS_VERSION}" \
      io.drem.source-state="${SOURCE_STATE}" \
      io.drem.canvasbench.harness="swe-agent" \
      io.drem.canvasbench.upstream="${UPSTREAM_SOURCE}" \
      io.drem.canvasbench.env-contract="${ENV_CONTRACT}" \
      io.drem.canvasbench.normalizer="${NORMALIZER}"
ENV HOME=/home/bench \
    PYTHONPATH=/opt/swe-agent \
    SWE_AGENT_CONFIG_DIR=/opt/swe-agent/config \
    SWE_AGENT_TOOLS_DIR=/opt/swe-agent/tools \
    SWE_AGENT_TRAJECTORY_DIR=/tmp/swe-agent-trajectories
USER 65532:65532
