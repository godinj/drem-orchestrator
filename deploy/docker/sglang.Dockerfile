# syntax=docker/dockerfile:1.7
#
# sglang — LLM server. Phase 1 global infra.
#
# Strategy: re-tag the official lmsysorg/sglang image rather than rebuild from
# source. SGLang's published image already includes CUDA runtime, flash
# attention kernels, and the Python server. We only need GPU access plus a
# bind-mounted model dir; nothing else is drem-specific.
#
# Operator notes:
#   - Requires nvidia-container-toolkit on the host.
#   - Pin a concrete tag before promoting past "draft". `:latest` is not
#     reproducible; move to `:v<x>.<y>.<z>` once a canonical version is chosen.
#   - Model dir lives on host. Bind-mount via compose (NOT baked into image)
#     so the 50+ GB weights never enter a layer.
#
# Build from repo root:
#   docker build -f deploy/docker/sglang.Dockerfile -t localhost:5000/drem-sglang:latest .
#   docker push localhost:5000/drem-sglang:latest
#
# Listen: 8081 (service-name `sglang` on the drem-net network).

ARG SGLANG_TAG=latest
FROM lmsysorg/sglang:${SGLANG_TAG}

# Override at compose-time; these are documentation only.
ENV SGLANG_HOST=0.0.0.0 \
    SGLANG_PORT=8081 \
    SGLANG_MODEL_DIR=/models

EXPOSE 8081

# The upstream image's entrypoint already launches the server; we only inject
# the listen address + model path. Concrete model + flags come from compose
# `command:` so they can change per environment without rebuilding.
#
# Example compose command (goes in deploy/compose/global.yml, not here):
#   command: >
#     python -m sglang.launch_server
#     --host 0.0.0.0 --port 8081
#     --model-path /models/gemma4-26b
#     --tp 1
#
# Leaving the upstream ENTRYPOINT in place so operators can override `command:`
# cleanly rather than fighting a second entrypoint.
