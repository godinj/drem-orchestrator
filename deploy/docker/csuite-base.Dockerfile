# syntax=docker/dockerfile:1.7
#
# drem-csuite-base — shared base image for the four C-Suite personas
# (Mike, Alex, Ross, Seth).
#
# Phase 3 of the containerization effort (docs/prd-containerization.md
# §Images and user stories 19-22). Every C-Suite persona image layers on
# top of this base and sets CSUITE_AGENT to select its persona prompt.
#
# The base lays down:
#   - The Claude Code CLI (@anthropic-ai/claude-code) pinned to the same
#     line as the worker images (deploy/docker/worker-base.Dockerfile).
#   - The persona prompt bundle under /opt/csuite/prompts/ (mike.md,
#     alex.md, ross.md, seth.md).
#   - A non-root user `drem` (UID 1000) so file ownership matches the
#     typical interactive host account.
#   - The entrypoint script /usr/local/bin/csuite-run which reads
#     CSUITE_AGENT and execs the Claude CLI against the matching prompt.
#
# Build context is deploy/docker/context/ and must contain:
#   - csuite-run.sh (checked into the repo)
#   - The docs/csuite-agents/prompts/ directory is pulled from a COPY of
#     the staged context/csuite-prompts/ dir populated by build-csuite.sh.
#
#   docker build -t localhost:5000/drem-csuite-base:latest \
#     -f deploy/docker/csuite-base.Dockerfile deploy/docker/context/
#   docker push localhost:5000/drem-csuite-base:latest
#
# Per-persona descendants set CSUITE_AGENT only; they share this base's
# entrypoint and prompt bundle.

FROM debian:bookworm-slim

# Keep in sync with deploy/docker/worker-base.Dockerfile so both classes
# of container speak the same Claude CLI version.
ARG CLAUDE_CODE_VERSION=latest
ARG NODE_MAJOR=20
ARG DREM_UID=1000
ARG DREM_GID=1000

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    PATH=/home/drem/.local/bin:/usr/local/bin:/usr/bin:/bin

# ---- base system packages -------------------------------------------------
# C-Suite agents emit structured messages (JSON/markdown), poke the
# orchestrator over HTTPS, and do light Git introspection. jq helps when
# the persona prompts include inline jq filters; tini keeps signal
# handling clean.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        gnupg \
        jq \
        tini \
 && rm -rf /var/lib/apt/lists/*

# ---- Node.js + Claude CLI --------------------------------------------------
RUN set -eux; \
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - ; \
    apt-get install -y --no-install-recommends nodejs ; \
    rm -rf /var/lib/apt/lists/* ; \
    if [ "$CLAUDE_CODE_VERSION" = "latest" ]; then \
        npm install -g @anthropic-ai/claude-code ; \
    else \
        npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" ; \
    fi ; \
    npm cache clean --force

# ---- non-root user ---------------------------------------------------------
RUN /usr/sbin/groupadd --gid "${DREM_GID}" drem \
 && /usr/sbin/useradd  --uid "${DREM_UID}" --gid "${DREM_GID}" \
             --create-home --shell /bin/bash drem

# ---- Claude Code first-run preseed ----------------------------------------
# Claude Code gates the first run on three interactive prompts and blocks
# on stdin until each is answered. In the compose-launched csuite
# containers a TTY is attached but nothing is feeding stdin, so the CLI
# deadlocks at the first prompt ("Syntax theme: Monokai Extended
# (ctrl+t to disable)") and the agent never reaches its orchestrator
# loop. Inbox dispatch to the persona is dead on arrival until the
# prompts are satisfied. See docs/prd-containerization.md §C-Suite
# runtime + the 2026-04-20 incident notes.
#
# Two files together let the CLI skip straight past onboarding, theme
# selection, AND the folder-trust dialog:
#   * ~/.claude.json           -- holds hasCompletedOnboarding (skips
#                                 the welcome/onboarding pane),
#                                 bypasses permissions-mode
#                                 acknowledgement, and pre-populates
#                                 projects["/home/drem"] with
#                                 hasTrustDialogAccepted +
#                                 hasCompletedProjectOnboarding so the
#                                 "Is this a project you trust?" dialog
#                                 does not fire on the working
#                                 directory. The CLI rewrites this file
#                                 on every start (adding userID,
#                                 firstStartTime, etc.) so our seed just
#                                 needs to set the gate keys; everything
#                                 else is filled in by the running CLI.
#   * ~/.claude/settings.json  -- holds the theme choice. 'dark' is the
#                                 CLI default for non-TTY environments.
#
# Both files must be drem-owned because the CLI updates them at
# runtime. The per-project compose.override.yml bind-mounts a host-side
# .credentials.json onto /home/drem/.claude/.credentials.json at
# container start; that file-level mount lands as a sibling of
# settings.json and does not clobber it.
COPY --chown=drem:drem claude-code-onboarding.json /home/drem/.claude.json
RUN install -d -o drem -g drem -m 0700 /home/drem/.claude \
 && chmod 0600 /home/drem/.claude.json
COPY --chown=drem:drem claude-code-settings.json /home/drem/.claude/settings.json
RUN chmod 0644 /home/drem/.claude/settings.json

# ---- persona prompts -------------------------------------------------------
# The build script (deploy/docker/build-csuite.sh) stages
# docs/csuite-agents/prompts/{mike,alex,ross,seth}.md under
# deploy/docker/context/csuite-prompts/ before invoking `docker build`.
# Each persona image (csuite-{mike,alex,ross,seth}.Dockerfile) selects
# one of them via CSUITE_AGENT at runtime.
RUN mkdir -p /opt/csuite/prompts \
 && chown -R drem:drem /opt/csuite
COPY --chown=drem:drem csuite-prompts/ /opt/csuite/prompts/

# ---- entrypoint ------------------------------------------------------------
COPY --chown=root:root csuite-run.sh /usr/local/bin/csuite-run.sh
RUN chmod 0755 /usr/local/bin/csuite-run.sh

# ---- runtime surface -------------------------------------------------------
USER drem
WORKDIR /home/drem

# tini reaps zombies and forwards SIGTERM/SIGINT to the Claude CLI.
ENTRYPOINT ["/usr/bin/tini", "--"]

# Default command: csuite-run reads CSUITE_AGENT and execs Claude.
CMD ["/usr/local/bin/csuite-run.sh"]
