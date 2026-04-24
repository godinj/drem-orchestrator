# syntax=docker/dockerfile:1.7
#
# drem-csuite-base — shared base image for the four C-Suite personas
# (Mike, Alex, Seth, Kyle).
#
# Phase 3 of the containerization effort (docs/prd-containerization.md
# §Images and user stories 19-22). Every C-Suite persona image layers on
# top of this base and sets CSUITE_AGENT to select its persona prompt.
#
# The base lays down:
#   - The Claude Code CLI and OpenAI Codex CLI pinned to the same line as
#     the worker images (deploy/docker/worker-base.Dockerfile).
#   - dremctl, the HTTP-only orchestrator CLI for persona operations.
#   - The persona prompt bundle under /opt/csuite/prompts/ (mike.md,
#     alex.md, seth.md, kyle.md).
#   - A non-root user `drem` (UID 1000) so file ownership matches the
#     typical interactive host account.
#   - The csuite-persona poller binary (cmd/csuite-persona), pre-built
#     on the host by deploy/docker/build-csuite.sh, plus the
#     csuite-entrypoint.sh wrapper that execs it. See Wave 2 pivot in
#     plans/csuite-persona-pivot.md — the poller replaced the prior
#     `exec claude --print` model.
#
# Build context is deploy/docker/context/ and must contain:
#   - csuite-entrypoint.sh (Wave-2 entrypoint; checked into the repo).
#   - csuite-run.sh (legacy Wave-1 entrypoint; retained in the image so
#     operators can shell in and exec it for side-by-side debugging).
#   - csuite-persona (pre-built Go binary staged by build-csuite.sh).
#   - dremctl (pre-built Go binary staged by build-csuite.sh).
#   - The docs/csuite-agents/prompts/ directory is pulled from a COPY of
#     the staged context/csuite-prompts/ dir populated by build-csuite.sh.
#
#   docker build -t localhost:5000/drem-csuite-base:latest \
#     -f deploy/docker/csuite-base.Dockerfile deploy/docker/context/
#   docker push localhost:5000/drem-csuite-base:latest
#
# Per-persona descendants set CSUITE_AGENT only; they share this base's
# entrypoint, poller binary, and prompt bundle.

FROM debian:bookworm-slim

# Keep in sync with deploy/docker/worker-base.Dockerfile so both classes
# of container speak the same Claude CLI version. The pin tracks the
# host operator's `claude` version — bumping here without also bumping
# the host CLI can silently diverge agent behaviour from operator
# expectations. Rebuild both images on every bump.
ARG CLAUDE_CODE_VERSION=2.1.116
ARG CODEX_VERSION=latest
ARG NODE_MAJOR=20
ARG DREM_UID=1000
ARG DREM_GID=1000

# DISABLE_AUTOUPDATER silences the Claude CLI's in-process self-update
# path. Auto-update inside the csuite containers fails every run
# because /usr/lib/node_modules is root-owned while the CLI runs as
# drem (UID 1000); the image is immutable anyway, so the update would
# be lost at the next restart even if it succeeded. The pin above is
# the one way versions change; auto-update would defeat it.
ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    DISABLE_AUTOUPDATER=1 \
    CSUITE_PROTO_SH=/opt/csuite/bin/csuite-proto.sh \
    PATH=/home/drem/.local/bin:/opt/csuite/bin:/usr/local/bin:/usr/bin:/bin

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

# ---- Node.js + Claude/Codex CLI -------------------------------------------
RUN set -eux; \
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - ; \
    apt-get install -y --no-install-recommends nodejs ; \
    rm -rf /var/lib/apt/lists/* ; \
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
RUN /usr/sbin/groupadd --gid "${DREM_GID}" drem \
 && /usr/sbin/useradd  --uid "${DREM_UID}" --gid "${DREM_GID}" \
             --create-home --shell /bin/bash drem

# Claude/Codex Bash tools commonly invoke `bash -lc`, and Debian's
# /etc/profile resets PATH for login shells. Keep /opt/csuite/bin visible
# there too so wrappers such as host-exec work from model tool calls.
RUN printf '%s\n' \
        'export PATH="/home/drem/.local/bin:/opt/csuite/bin:/usr/local/bin:/usr/bin:/bin${PATH:+:$PATH}"' \
        >/etc/profile.d/csuite-path.sh \
 && chmod 0644 /etc/profile.d/csuite-path.sh

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
RUN install -d -o drem -g drem -m 0700 /home/drem/.claude /home/drem/.codex \
 && chmod 0600 /home/drem/.claude.json
COPY --chown=drem:drem claude-code-settings.json /home/drem/.claude/settings.json
RUN chmod 0644 /home/drem/.claude/settings.json

# ---- persona prompts -------------------------------------------------------
# The build script (deploy/docker/build-csuite.sh) stages
# docs/csuite-agents/prompts/{mike,alex,seth,kyle}.md under
# deploy/docker/context/csuite-prompts/ before invoking `docker build`.
# Each persona image (csuite-{mike,alex,seth,kyle}.Dockerfile) selects
# one of them via CSUITE_AGENT at runtime.
RUN mkdir -p /opt/csuite/prompts \
 && chown -R drem:drem /opt/csuite
COPY --chown=drem:drem csuite-prompts/ /opt/csuite/prompts/

# ---- host-exec wrapper -----------------------------------------------------
# POST-to-the-host-daemon CLI used by personas to run commands on the
# host (drem, git, docker, filesystem mutations). Canonical source is
# plans/host-exec-artifacts/host-exec; build-csuite.sh stages it into
# the build context. Daemon, allowlist, and security fences are
# documented in plans/host-exec-daemon-option-a.md. The wrapper reads
# /etc/drem/host-exec.token (bind-mounted read-only via the per-persona
# compose block) and POSTs to $HOST_EXEC_URL, which defaults to
# http://host.docker.internal:8091 — the compose block adds
# `extra_hosts: host.docker.internal:host-gateway` so the default
# resolves without an env override.
COPY --chown=root:root host-exec /opt/csuite/bin/host-exec

# ---- disk protocol helper --------------------------------------------------
# The persona containers do not bind-mount the full repo, so prompts must not
# rely on scripts/csuite-proto.sh being present relative to a checkout. The
# build script stages the canonical helper from scripts/csuite-proto.sh here.
COPY --chown=root:root csuite-proto.sh /opt/csuite/bin/csuite-proto.sh
RUN chmod 0755 /opt/csuite/bin/host-exec /opt/csuite/bin/csuite-proto.sh

# ---- Wave-2 persona poller + entrypoint -----------------------------------
# csuite-persona is the headless inbox-driven poller that replaced the
# prior `exec claude --print` design. It polls
# /home/drem/.drem-csuite/<persona>/inbox on a 2s tick, invokes
# `claude -p` once per message, and writes the reply to the outbox.
# Signal handling and the claude-credentials path are documented in
# internal/csuite/persona/persona.go. The binary is pre-built on the
# host by build-csuite.sh (CGO_ENABLED=0 → no glibc coupling) so the
# image stays free of a Go toolchain.
COPY --chown=root:root csuite-persona /usr/local/bin/csuite-persona
COPY --chown=root:root dremctl /usr/local/bin/dremctl
COPY --chown=root:root csuite-entrypoint.sh /usr/local/bin/csuite-entrypoint
# csuite-run.sh is the legacy Wave-1 entrypoint. We keep it in the
# image so operators can shell in and exec it when diagnosing a broken
# persona poller, but it is no longer the default CMD.
COPY --chown=root:root csuite-run.sh /usr/local/bin/csuite-run.sh
RUN chmod 0755 \
        /usr/local/bin/csuite-persona \
        /usr/local/bin/dremctl \
        /usr/local/bin/csuite-entrypoint \
        /usr/local/bin/csuite-run.sh

# ---- runtime surface -------------------------------------------------------
USER drem
WORKDIR /home/drem

# tini reaps zombies and forwards SIGTERM/SIGINT to the poller, which
# cancels its context at the next tick boundary and exits 0 once the
# in-flight claude -p invocation returns.
ENTRYPOINT ["/usr/bin/tini", "--"]

# Default command: csuite-entrypoint reads CSUITE_AGENT and execs the
# csuite-persona poller with the right flags. The poller derives every
# other path from the persona name (see internal/csuite/persona).
CMD ["/usr/local/bin/csuite-entrypoint"]
