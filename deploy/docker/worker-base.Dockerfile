# syntax=docker/dockerfile:1.7
#
# drem-worker-base — shared base image for per-language worker containers.
#
# Phase 2 of the containerization effort (docs/prd-containerization.md §Images,
# user stories 8, 10, 17, 18, 32, 33, 45). The spawner launches an ephemeral
# container from one of the language-specific descendants of this image for
# every Opus coder and G4 worker task; no host-side worktree is ever created.
#
# This base lays down everything that does not depend on the target language:
#
#   - The agent harnesses: @anthropic-ai/claude-code, @openai/codex, and opencode.
#   - The crash-recovery daemon (drem-watchdog, baked in from the build context).
#   - The worker entrypoint script that clones /bare, starts the watchdog, and
#     execs the configured harness.
#   - A non-root user (drem, UID 1000) whose home serves as the container-local
#     checkout root.
#
# The bare repository is mounted at /bare by the spawner (read-write because
# the watchdog pushes). The container-local clone lives at /home/drem/work.
#
# Build context is deploy/docker/context/ and must contain a pre-built
# drem-watchdog binary plus worker-entrypoint.sh. Use deploy/docker/build-workers.sh.
#
#   docker build -t localhost:5000/drem-worker-base:latest \
#     -f deploy/docker/worker-base.Dockerfile deploy/docker/context/
#   docker push localhost:5000/drem-worker-base:latest
#
# Per-language descendants (worker-go.Dockerfile, worker-cpp.Dockerfile) layer
# their toolchains on top and set DREM_LANGUAGE accordingly.

FROM debian:bookworm-slim

# Pin every tool version here so bumps are a one-line change.
# CLAUDE_CODE_VERSION: pinned to the host operator's CLI line so worker
# behaviour never silently diverges from operator expectations. Keep in
# sync with deploy/docker/csuite-base.Dockerfile; rebuild both on every
# bump.
ARG CLAUDE_CODE_VERSION=2.1.116
ARG CODEX_VERSION=latest
# OPENCODE_VERSION: passed through to the opencode install script; accepted
# forms are documented at https://opencode.ai/install. "latest" is the default
# of the upstream installer.
ARG OPENCODE_VERSION=latest
ARG NODE_MAJOR=20
ARG DREM_UID=1000
ARG DREM_GID=1000

# DISABLE_AUTOUPDATER silences the Claude CLI's in-process self-update
# path. Workers are one-shot containers that rarely live long enough
# to auto-update, and even if they did the update would be lost at
# the next spawn; the image pin above is the one way versions change.
ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    DISABLE_AUTOUPDATER=1 \
    PATH=/usr/local/go/bin:/home/drem/.local/bin:/usr/local/bin:/usr/bin:/bin

# ---- base system packages -------------------------------------------------
# curl+gpg bootstrap NodeSource; tini wraps PID 1 so signal handling is clean
# for the foreground harness process; sudo is used only by the entrypoint's
# pre-exec ownership fixups (never by the agent itself).
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        gnupg \
        jq \
        openssh-client \
        sudo \
        tini \
        unzip \
        xz-utils \
 && rm -rf /var/lib/apt/lists/*

# ---- Node.js + npm (NodeSource) -------------------------------------------
# The claude CLI is shipped as an npm package. Pin the Node major line via
# NODE_MAJOR so image rebuilds are deterministic even when NodeSource updates
# its apt repo.
RUN set -eux; \
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - ; \
    apt-get install -y --no-install-recommends nodejs ; \
    rm -rf /var/lib/apt/lists/*

# ---- claude CLI + Codex CLI -----------------------------------------------
# Installed globally so every user (including the non-root `drem` user) can
# invoke it without PATH massaging.
RUN set -eux; \
    if [ "$CLAUDE_CODE_VERSION" = "latest" ]; then \
        npm install -g @anthropic-ai/claude-code ; \
    else \
        npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" ; \
    fi ; \
    if [ "$CODEX_VERSION" = "latest" ]; then \
        npm install -g @openai/codex ; \
    else \
        npm install -g "@openai/codex@${CODEX_VERSION}" ; \
    fi ; \
    npm cache clean --force

# ---- non-root user ---------------------------------------------------------
# UID 1000 mirrors the typical interactive host account so files written into
# a bind-mounted bare repo have host-friendly ownership. Passwordless sudo is
# intentional: the worker container is ephemeral and single-tenant.
RUN /usr/sbin/groupadd --gid "${DREM_GID}" drem \
 && /usr/sbin/useradd  --uid "${DREM_UID}" --gid "${DREM_GID}" \
             --create-home --shell /bin/bash drem \
 && echo 'drem ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/drem \
 && chmod 0440 /etc/sudoers.d/drem \
 && mkdir -p /home/drem/work /home/drem/.claude /home/drem/.codex /home/drem/.drem \
 && chown -R drem:drem /home/drem

# ---- opencode CLI ----------------------------------------------------------
# Installed as the drem user so the binary lands in /home/drem/.opencode/bin.
# The official installer writes there and adds it to PATH; we set PATH above
# to include ~/.local/bin for consistency. Opencode itself lives under the
# user's home, so running this as root would stash it in /root and hide it
# from the drem user at runtime.
USER drem
RUN set -eux; \
    export OPENCODE_VERSION="${OPENCODE_VERSION}" ; \
    curl -fsSL https://opencode.ai/install | bash
ENV PATH=/home/drem/.opencode/bin:/usr/local/go/bin:/home/drem/.local/bin:/usr/local/bin:/usr/bin:/bin

# ---- baked-in binaries and entrypoint --------------------------------------
# Switch back to root for copies into /usr/local; chmod them executable at
# build time so the spawner does not need to know about file modes.
USER root
COPY --chown=root:root drem-watchdog         /usr/local/bin/drem-watchdog
COPY --chown=root:root worker-entrypoint.sh  /usr/local/bin/worker-entrypoint
RUN chmod 0755 /usr/local/bin/drem-watchdog /usr/local/bin/worker-entrypoint

# ---- runtime surface -------------------------------------------------------
USER drem
WORKDIR /home/drem/work

# tini reaps zombies and forwards SIGTERM/SIGINT to the harness process.
ENTRYPOINT ["/usr/bin/tini", "--"]

# Default CMD is the worker entrypoint; compose/spawner may override it for
# debugging (e.g. `docker run ... bash`).
CMD ["/usr/local/bin/worker-entrypoint"]
