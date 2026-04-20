# syntax=docker/dockerfile:1.7
#
# drem-planner — per-task planner container.
#
# Runs the Anthropic `claude` CLI (npm @anthropic-ai/claude-code) in headless
# mode against a feature worktree, producing a plan.json at the worktree
# root. Spawned on-demand by the orchestrator's dispatchPlan; exits with
# the CLI's exit code so the orch can route on typed exit codes.
#
# Build context is the repo root (needs the entrypoint script under
# deploy/docker/context/):
#
#   docker build -f deploy/docker/planner.Dockerfile \
#     -t localhost:5000/drem-planner:latest .
#   docker push localhost:5000/drem-planner:latest
#
# The runtime image ships git (the planner CLI reads repository state via
# git commands during exploration) and ca-certificates (HTTPS to
# api.anthropic.com). No Go build stage — the container's workload is the
# claude CLI, not a custom binary.
#
# See plans/warm-direct-planner.md for the full design rationale and the
# companion per-task spawn pattern pioneered in plans/merger-spawn-on-demand-impl.md.

FROM debian:bookworm-slim

# Pin the Node major line via NODE_MAJOR so image rebuilds stay deterministic
# even when NodeSource updates its apt repo. Mirrors worker-base.Dockerfile.
ARG NODE_MAJOR=22
ARG CLAUDE_CODE_VERSION=latest

# ---- base packages --------------------------------------------------------
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        gnupg \
        jq \
        tini \
 && rm -rf /var/lib/apt/lists/*

# ---- Node.js + npm (NodeSource) -------------------------------------------
RUN set -eux; \
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - ; \
    apt-get install -y --no-install-recommends nodejs ; \
    rm -rf /var/lib/apt/lists/*

# ---- claude CLI -----------------------------------------------------------
# Installed globally so the root-owned entrypoint can invoke it without PATH
# massaging. Pinning a version is recommended for reproducibility; the
# `latest` default mirrors worker-base.Dockerfile's convention.
RUN set -eux; \
    if [ "$CLAUDE_CODE_VERSION" = "latest" ]; then \
        npm install -g @anthropic-ai/claude-code ; \
    else \
        npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" ; \
    fi ; \
    npm cache clean --force

# ---- git cross-UID guard --------------------------------------------------
# The planner clones / reads from a bare repo bind-mounted from the host
# (operator UID) into this root-owned container. Git 2.35+ blocks cross-UID
# repository access unless safe.directory lists it. Mirrors orch.Dockerfile
# and merger.Dockerfile.
RUN git config --system --add safe.directory '*'

# ---- entrypoint -----------------------------------------------------------
COPY --chown=root:root deploy/docker/context/planner-entrypoint.sh \
     /usr/local/bin/planner-entrypoint
RUN chmod 0755 /usr/local/bin/planner-entrypoint

# /work is the worktree clone; /bare is the bind-mounted bare repo. The
# orchestrator's dispatchPlan passes both via the spawner's BareRepoMount
# field and the container's argv.
VOLUME ["/work"]
WORKDIR /work

# tini reaps zombies and forwards signals cleanly. The entrypoint script
# does the flag-parsing + claude invocation then exits with the CLI's code.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/planner-entrypoint"]
