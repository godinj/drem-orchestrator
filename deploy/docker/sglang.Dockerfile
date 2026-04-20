# syntax=docker/dockerfile:1.7
#
# drem-sglang — GPU-backed LLM server, reproducing the operator's host
# install of a heavily customized SGLang stack so the gemma4 26B model
# can be served from a container.
#
# WHY NOT JUST USE lmsysorg/sglang:latest?
#   The published image is a stable release. Two things the model needs
#   are missing from it:
#     1. The `gemma4` tool-call parser  (`--tool-call-parser: invalid
#        choice: 'gemma4'`).
#     2. A new enough `transformers` (5.5.4) to recognize the
#        `gemma4_text` model architecture.
#   Both ship in the operator's host install: a git-built SGLang at
#   commit g90ef8ce54 (0.5.10.post2.dev306) plus six in-tree patches
#   that keep AWQ-quantized Gemma-4 weights compatible with Marlin /
#   Triton MoE kernels.
#
# REPRODUCTION STRATEGY
#   Pin everything by exact version. The operator captured a frozen
#   `pip freeze` of the host venv (207 packages, including the
#   git+sglang URL pinned to the specific commit) — this Dockerfile
#   installs from that lock file unchanged, then re-applies the same
#   six patches against the resulting site-packages tree.
#
#   * GPU base:       nvidia/cuda 12.8 runtime + cuDNN on Ubuntu 24.04.
#                     Matches torch 2.9.1's CUDA 12 ABI (the host venv
#                     pulls nvidia-*-cu12==12.8.x wheels).
#   * Python:         3.13.5 from the deadsnakes PPA. Ubuntu 24.04 ships
#                     3.12 by default; deadsnakes is the standard
#                     third-party PPA for newer CPython versions and
#                     adds maybe 30s to the build vs. ~20min to compile
#                     CPython from source. Trade-off: an external apt
#                     repo, but it's pinned and reproducible. Alternative
#                     considered (pyenv) was rejected as overkill — we
#                     don't need multiple Python versions in the image.
#   * Venv:           /opt/venv, mirroring the host pattern. PATH
#                     prefixed so `python` / `pip` resolve to it.
#   * Patches:        Vendored under deploy/docker/context/sglang-patches/
#                     (canonical copies — the model-tuning host-side
#                     repo is no longer the source of truth for the
#                     container image).
#
# BUILD
#   Heavy: 30+ minute build, 15+ GB on-disk image (CUDA runtime + cuDNN
#   + 207 Python wheels including flash-attn-4 and flashinfer prebuilts).
#   The build itself does not require a GPU (no CUDA kernels are
#   compiled — flash-attn / flashinfer ship as prebuilt wheels). The
#   resulting container does require a GPU at runtime via
#   nvidia-container-toolkit.
#
#   docker build -f deploy/docker/sglang.Dockerfile \
#                -t localhost:5000/drem-sglang:latest .
#   docker push localhost:5000/drem-sglang:latest
#
# ROLLBACK
#   The host-side launcher (start-sglang-gemma4-production.sh in the
#   operator's model-tuning repo) remains the canonical fallback. Stop
#   the container, restart the host launcher — drem clients see the
#   same OpenAI-compatible endpoint on 127.0.0.1:8081 either way.
#
# Listen: 8081 (service-name `sglang` on the drem-net network).

# ---------- 1. GPU base -----------------------------------------------------
# nvidia/cuda :12.8.1-cudnn-runtime-ubuntu24.04 — matches the host's
# nvidia-*-cu12==12.8.x wheels. Concrete tag, not :latest.
FROM nvidia/cuda:12.8.1-cudnn-runtime-ubuntu24.04

# Avoid tzdata / debconf prompts during apt operations.
ENV DEBIAN_FRONTEND=noninteractive \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PIP_NO_CACHE_DIR=1

# ---------- 2. OS packages --------------------------------------------------
# - software-properties-common  for `add-apt-repository` (deadsnakes PPA).
# - build-essential + git       for any source-only wheel that falls back
#                               to a local build (rare given the lock).
# - patch                       for apply-sglang-patches.sh.
# - curl + ca-certificates      for HuggingFace / cubin downloads at
#                               first-launch warmup.
# - libgomp1                    runtime dep of several wheels.
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        software-properties-common \
        build-essential \
        git \
        patch \
        curl \
        ca-certificates \
        libgomp1 && \
    add-apt-repository -y ppa:deadsnakes/ppa && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
        python3.13 \
        python3.13-venv \
        python3.13-dev && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# ---------- 3. Virtualenv ---------------------------------------------------
# /opt/venv mirrors the host venv pattern (host uses /home/godinj/venvs/sglang).
# Putting it on PATH ahead of /usr/bin makes `python` and `pip` resolve to
# the venv interpreter inside any subsequent RUN / CMD without an explicit
# `source bin/activate`.
RUN python3.13 -m venv /opt/venv && \
    /opt/venv/bin/pip install --upgrade pip setuptools wheel
ENV PATH=/opt/venv/bin:$PATH \
    VIRTUAL_ENV=/opt/venv

# ---------- 4. Python deps (frozen) ----------------------------------------
# The lock file is the canonical reproducibility artifact. It includes:
#   - sglang @ git+https://...@90ef8ce54  (exact commit)
#   - transformers==5.5.4
#   - torch==2.9.1 + nvidia-*-cu12==12.8.x
#   - flash-attn-4, flashinfer-python, sglang-kernel as prebuilt wheels
# 207 packages total. Single layer to keep cache hits cheap on
# Dockerfile changes that don't touch the lock.
#
# --no-deps is REQUIRED. The lock file already enumerates every
# transitive dep at exact pinned versions (it's a `pip freeze` of a
# working host venv), so dependency resolution would be redundant — but
# worse, sglang's setup.py declares `transformers==5.3.0` while the
# host actually runs `transformers==5.5.4` (newer, needed for the
# `gemma4_text` model architecture). pip's resolver rejects that as a
# conflict; the host venv only works because it was assembled
# out-of-order. With --no-deps we install exactly what `pip freeze`
# captured, mirroring the host bit-for-bit.
#
# RUST TOOLCHAIN: outlines_core==0.1.26 (pinned for API compat with
# outlines==0.1.11 and the sglang patch set) ships cp39–cp312 wheels on
# PyPI but no cp313 wheel. On Python 3.13 pip falls back to the sdist,
# which is a PyO3 extension and needs `cargo`/`rustc`. The host venv has
# rustup-installed Rust 1.93 on PATH; we mirror that here. Installed via
# rustup (official upstream) into /opt/rust inside the same RUN layer as
# pip install, then removed after. Keeps the final image layer clean:
# toolchain never lands in a persisted layer, only outlines_core's
# compiled .so does.
COPY deploy/docker/context/sglang-requirements.txt /build/sglang-requirements.txt
RUN set -eux; \
    export RUSTUP_HOME=/opt/rust CARGO_HOME=/opt/rust; \
    curl -sSf https://sh.rustup.rs | sh -s -- -y --default-toolchain stable --profile minimal --no-modify-path; \
    export PATH=/opt/rust/bin:$PATH; \
    rustc --version; \
    pip install --no-deps -r /build/sglang-requirements.txt; \
    rm -rf /opt/rust

# ---------- 5. Apply Gemma-4 patches ---------------------------------------
# Six patches against the just-installed sglang site-packages tree. The
# patch script is idempotent (--forward) so a rebuild that hits the
# install layer cached but the patch layer fresh is a no-op.
COPY deploy/docker/context/sglang-patches /build/sglang-patches
COPY deploy/docker/context/apply-sglang-patches.sh /build/apply-sglang-patches.sh
RUN chmod +x /build/apply-sglang-patches.sh && \
    PATCH_DIR=/build/sglang-patches /build/apply-sglang-patches.sh

# ---------- 6. Runtime config ----------------------------------------------
# PYTORCH_ALLOC_CONF: cuts allocator fragmentation overhead. Set per the
# host launcher; cheap insurance even when fragmentation isn't the
# bottleneck. PYTORCH_CUDA_ALLOC_CONF (the deprecated name) is NOT set
# — torch warns when it sees both.
#
# Documentation-only ENV: SGLANG_HOST/PORT/MODEL_DIR are wired through
# the compose `command:`, not consumed by sglang.launch_server itself.
ENV PYTORCH_ALLOC_CONF=expandable_segments:True \
    SGLANG_HOST=0.0.0.0 \
    SGLANG_PORT=8081 \
    SGLANG_MODEL_DIR=/models

EXPOSE 8081

# ---------- 7. Entrypoint ---------------------------------------------------
# Run the SGLang HTTP server module directly. The full flag set
# (--tool-call-parser gemma4, --kv-cache-dtype fp8_e5m2, SWA tuning,
# etc.) is supplied by the compose `command:` block in
# deploy/compose/global.yml so flag changes don't require a rebuild.
#
# We deliberately do NOT replicate the host launcher's GPU clock / VRAM
# drain prelude (`nvidia-smi -lgc 2100`, `pkill -9 sglang::scheduler`,
# the VRAM-usage poll loop). Those are host-only concerns: only one
# container will hold the GPU at a time under nvidia-container-toolkit
# semantics; the drain loop fights a problem that doesn't exist
# inside the container model.
ENTRYPOINT ["python", "-m", "sglang.launch_server"]
