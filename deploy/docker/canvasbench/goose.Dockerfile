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
RUN python - "$HARNESS_VERSION" "$UPSTREAM_INTEGRITY" <<'PY'
import hashlib
from pathlib import Path
import sys
import tarfile
import urllib.request

version, expected = sys.argv[1:]
if not expected.startswith("sha256:"):
    raise SystemExit("Goose archive must be sha256-pinned")
archive = Path("/tmp/goose.tar.gz")
url = f"https://github.com/aaif-goose/goose/releases/download/v{version}/goose-x86_64-unknown-linux-gnu.tar.gz"
urllib.request.urlretrieve(url, archive)
if hashlib.sha256(archive.read_bytes()).hexdigest() != expected.removeprefix("sha256:"):
    raise SystemExit("Goose archive digest mismatch")
with tarfile.open(archive, "r:gz") as bundle:
    member = next((item for item in bundle.getmembers() if item.name.lstrip("./") == "goose" and item.isfile()), None)
    if member is None:
        raise SystemExit("Goose archive lacks executable")
    source = bundle.extractfile(member)
    if source is None:
        raise SystemExit("Goose executable is unreadable")
    target = Path("/opt/harness/goose-real")
    target.write_bytes(source.read())
    target.chmod(0o555)
archive.unlink()
PY
RUN test "$(/opt/harness/goose-real --version | tr -d ' ')" = "$HARNESS_VERSION" \
    && test -n "$SOURCE_STATE" && test -n "$UPSTREAM_SOURCE" && test -n "$ENV_CONTRACT" && test -n "$NORMALIZER"
COPY deploy/docker/canvasbench/context/goose/goose-wrapper.py /usr/local/bin/goose
RUN chmod 0555 /usr/local/bin/goose
LABEL org.opencontainers.image.source="${UPSTREAM_SOURCE}" \
      org.opencontainers.image.revision="${SOURCE_STATE}" \
      org.opencontainers.image.version="${HARNESS_VERSION}" \
      io.drem.source-state="${SOURCE_STATE}" \
      io.drem.canvasbench.harness="goose" \
      io.drem.canvasbench.upstream="${UPSTREAM_SOURCE}" \
      io.drem.canvasbench.env-contract="${ENV_CONTRACT}" \
      io.drem.canvasbench.normalizer="${NORMALIZER}"
ENV HOME=/home/bench
USER 65532:65532
