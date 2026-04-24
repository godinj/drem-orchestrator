#!/usr/bin/env bash
#
# csuite-entrypoint — Wave-2 PID-1-side script for the four C-Suite
# container images (mike, alex, seth, kyle).
#
# Replaces deploy/docker/context/csuite-run.sh (which was the Wave-1
# interactive-model entrypoint). Instead of launching a long-lived
# interactive model process, this wrapper execs the csuite-persona
# headless poller (cmd/csuite-persona) which polls the persona's inbox
# and spawns `opencode run` once per message. See
# plans/csuite-persona-pivot.md and docs/containerization/install.md
# "C-Suite personas: the persona poller runtime" for the design.
#
# Environment contract (set by the per-project compose template; unchanged
# from csuite-run.sh):
#
#   CSUITE_AGENT      persona name: mike, alex, seth, or kyle (required)
#   DREM_PROJECT      project name (informational; logged only)
#   DREM_ORCH_URL     orchestrator HTTP URL inside drem-net (unused by
#                     the poller itself; available to any model invocation
#                     for tool-level HTTP calls)
#
# Auth is supplied by bind-mounted CLI state; the compose template mounts
# /home/drem/.codex/auth.json for Codex and keeps the legacy Claude
# credential mount for rollback compatibility.

set -euo pipefail

if [[ -z "${CSUITE_AGENT:-}" ]]; then
    echo "csuite-entrypoint: CSUITE_AGENT is not set" >&2
    exit 64
fi

case "${CSUITE_AGENT}" in
    mike|alex|seth|kyle) ;;
    *)
        echo "csuite-entrypoint: unknown CSUITE_AGENT=${CSUITE_AGENT} (want mike|alex|seth|kyle)" >&2
        exit 64
        ;;
esac

echo "csuite-entrypoint: starting persona=${CSUITE_AGENT} project=${DREM_PROJECT:-unset} orch=${DREM_ORCH_URL:-unset}"

sync_opencode_codex_auth() {
    local auth_file="${OPENCODE_MULTI_AUTH_CODEX_AUTH_FILE:-/home/drem/.codex/auth.json}"

    if [[ ! -r "${auth_file}" ]]; then
        echo "csuite-entrypoint: codex auth file not readable at ${auth_file}; OpenCode may fail auth" >&2
        return 0
    fi

    export OPENCODE_MULTI_AUTH_CODEX_AUTH_FILE="${auth_file}"
    export OPENCODE_MULTI_AUTH_PREFER_CODEX_LATEST="${OPENCODE_MULTI_AUTH_PREFER_CODEX_LATEST:-1}"

    node <<'NODE'
const fs = require('fs');
const os = require('os');
const path = require('path');

const authPath = process.env.OPENCODE_MULTI_AUTH_CODEX_AUTH_FILE || path.join(os.homedir(), '.codex', 'auth.json');
const now = Date.now();

function decodeJWT(token) {
  try {
    const parts = String(token || '').split('.');
    if (parts.length !== 3) return null;
    const payload = parts[1].replace(/-/g, '+').replace(/_/g, '/');
    const padded = payload.padEnd(payload.length + ((4 - (payload.length % 4)) % 4), '=');
    return JSON.parse(Buffer.from(padded, 'base64').toString('utf8'));
  } catch {
    return null;
  }
}

function ensureDir(dir, mode = 0o700) {
  fs.mkdirSync(dir, { recursive: true, mode });
}

function readJSON(file, fallback) {
  try {
    return JSON.parse(fs.readFileSync(file, 'utf8'));
  } catch {
    return fallback;
  }
}

function writeJSON(file, value, mode = 0o600) {
  ensureDir(path.dirname(file));
  fs.writeFileSync(file, JSON.stringify(value, null, 2));
  fs.chmodSync(file, mode);
}

function aliasFrom(email, accountId) {
  const raw = (email && email.split('@')[0]) || (accountId && accountId.slice(0, 8)) || 'codex';
  return raw.replace(/[^A-Za-z0-9._-]/g, '-').replace(/^-+|-+$/g, '') || 'codex';
}

const codexAuth = readJSON(authPath, null);
const tokens = codexAuth && typeof codexAuth.tokens === 'object' ? codexAuth.tokens : codexAuth;
const accessToken = tokens && (tokens.access_token || tokens.accessToken || tokens.access);
const refreshToken = tokens && (tokens.refresh_token || tokens.refreshToken || tokens.refresh);
const idToken = tokens && (tokens.id_token || tokens.idToken || tokens.id);

if (!accessToken || !refreshToken) {
  console.error(`csuite-entrypoint: ${authPath} is missing access/refresh tokens; OpenCode may fail auth`);
  process.exit(0);
}

const accessClaims = decodeJWT(accessToken);
const idClaims = idToken ? decodeJWT(idToken) : null;
const authClaims = (accessClaims && accessClaims['https://api.openai.com/auth']) || {};
const idAuthClaims = (idClaims && idClaims['https://api.openai.com/auth']) || {};
const profile = (idClaims && idClaims['https://api.openai.com/profile']) || {};
const email = profile.email || idClaims?.email || accessClaims?.email;
const accountId = tokens.account_id || tokens.accountId || authClaims.chatgpt_account_id || idAuthClaims.chatgpt_account_id;
const accountUserId = authClaims.chatgpt_account_user_id || idAuthClaims.chatgpt_account_user_id;
const userId = authClaims.user_id || authClaims.chatgpt_user_id || idAuthClaims.user_id || idAuthClaims.chatgpt_user_id;
const planType = authClaims.chatgpt_plan_type || idAuthClaims.chatgpt_plan_type;
const expiresAt = typeof accessClaims?.exp === 'number' ? accessClaims.exp * 1000 : now + 30 * 60 * 1000;
const alias = aliasFrom(email, accountId);

const storePath = process.env.OPENCODE_MULTI_AUTH_STORE_FILE || path.join(os.homedir(), '.config', 'opencode-multi-auth', 'accounts.json');
const store = readJSON(storePath, {
  version: 2,
  accounts: {},
  activeAlias: null,
  rotationIndex: 0,
  lastRotation: now,
  rotationStrategy: 'round-robin',
  settings: { rotationStrategy: 'round-robin' }
});

store.version = 2;
store.accounts = store.accounts && typeof store.accounts === 'object' ? store.accounts : {};
store.accounts[alias] = {
  ...(store.accounts[alias] || {}),
  alias,
  accessToken,
  refreshToken,
  idToken,
  accountId,
  accountUserId,
  userId,
  planType,
  expiresAt,
  email,
  lastRefresh: codexAuth.last_refresh || codexAuth.lastRefresh,
  lastSeenAt: now,
  source: 'codex',
  usageCount: store.accounts[alias]?.usageCount || 0,
  enabled: true
};
store.activeAlias = alias;
store.rotationIndex = typeof store.rotationIndex === 'number' ? store.rotationIndex : 0;
store.lastRotation = typeof store.lastRotation === 'number' ? store.lastRotation : now;
store.rotationStrategy = store.rotationStrategy || 'round-robin';
store.settings = store.settings || { rotationStrategy: store.rotationStrategy };
writeJSON(storePath, store);

const opencodeAuthPath = path.join(os.homedir(), '.local', 'share', 'opencode', 'auth.json');
const opencodeAuth = readJSON(opencodeAuthPath, {});
opencodeAuth.openai = {
  type: 'oauth',
  access: accessToken,
  refresh: refreshToken,
  expires: expiresAt
};
writeJSON(opencodeAuthPath, opencodeAuth);

console.log(`csuite-entrypoint: synced Codex OAuth into OpenCode multi-auth alias=${alias}`);
NODE
}

sync_opencode_codex_auth

# Defaults inside the container:
#   inbox:   /home/drem/.drem-csuite/${CSUITE_AGENT}/inbox
#   outbox:  /home/drem/.drem-csuite/${CSUITE_AGENT}/outbox
#   state:   /home/drem/.drem-csuite/${CSUITE_AGENT}/state.md
#   archive: /home/drem/.drem-csuite/${CSUITE_AGENT}/inbox/.archive
#   prompt:  /opt/csuite/prompts/${CSUITE_AGENT}.md
# The poller derives these from -persona when the corresponding flags
# are empty, so we only need to pass -persona here.
#
# DREM_CLAUDE_TIMEOUT (optional, retained for compatibility) overrides the poller's default 5-minute
# per-invocation timeout. Accepts any Go duration string (e.g. "30m",
# "1h", "90m"). Unset falls back to persona.DefaultClaudeTimeout
# (5 min). Motivation: CTO-synthesis tasks routinely exceed 5 min of
# model wall-clock; 5 min bounds a single transactional reply but
# is too tight for deep analysis passes. Per-persona tuning is done by
# setting DREM_CLAUDE_TIMEOUT in the project compose template
# (internal/projects/templates/project-compose.yml.tmpl).
if [[ -n "${DREM_CLAUDE_TIMEOUT:-}" ]]; then
    echo "csuite-entrypoint: claude-timeout=${DREM_CLAUDE_TIMEOUT} (overriding default)"
    exec /usr/local/bin/csuite-persona \
        -persona "${CSUITE_AGENT}" \
        -claude-timeout "${DREM_CLAUDE_TIMEOUT}"
fi

exec /usr/local/bin/csuite-persona -persona "${CSUITE_AGENT}"
