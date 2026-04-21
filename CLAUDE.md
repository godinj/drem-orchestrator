# Project guidance for Claude Code

This file records hard constraints and recurring decisions that must
survive across sessions. Update it any time an operator directive
would otherwise be lost.

## Authentication: subscription-only

**Do NOT use auth tokens for Claude.** No `CLAUDE_CODE_OAUTH_TOKEN`,
no `ANTHROPIC_API_KEY`, no `ANTHROPIC_AUTH_TOKEN`, no API-key
fallback of any kind.

The only acceptable auth path is the operator's Claude subscription
via the OAuth credentials file at `~/.claude/.credentials.json`.
Containers pick this up via a read-only bind-mount (see
`compose.override.yml`). Workers and personas that need to invoke
`claude` or `claude -p` must rely on that mounted credentials file —
not an env-var token.

Why this matters: `claude setup-token` and `CLAUDE_CODE_OAUTH_TOKEN`
are a separate subscription-scoped mechanism that the operator has
explicitly opted out of. If a design doc or plan proposes using
them, flag it and pivot to the credentials-file path.

## Other standing constraints

- **Caveman mode** re-activates on session start via hook. Operator
  toggles off with `/caveman off`. Code, commits, plan docs, and
  review output stay in normal prose regardless.
- **Never push to origin unless explicitly asked.** The repo
  routinely accumulates 40+ local commits ahead of origin; that is
  expected.
- **Do not restart `drem-sglang`.** CUDA graph recapture takes
  ~5 minutes; sglang's healthy uptime is precious.
- **Do not run `docker compose up` without `--no-deps`** when
  scoping to a subset of services. The compose graph has
  `depends_on: service_healthy` that cascades into sglang recapture.
- **Docs-as-acceptance-criteria.** Every feature commit sequence
  includes the docs update (install.md, PRD, plan) as part of the
  feature, not a follow-up.
- **Constitution checks are repo-wide.** Quality audits are never
  scoped to the latest change alone.
- **Dispatch subagents for substantial work.** One-line or
  single-function fixes can be inline; multi-file or design-heavy
  work gets a worktree subagent.

## Containerization pivot

Active. Reference `docs/prd-containerization.md` and
`plans/containerization.md`. The csuite personas (Seth, Alex, Mike,
Ross) run as separate Claude Code containers; Kyle runs as a Go
binary (`cmd/drem-kyle`) — different runtime, intentionally.
Workers are ephemeral-per-task containers spawned by
`drem-global-spawner-1`.

## Bare-repo config

Registered projects' bare git repos must have
`receive.denyCurrentBranch=ignore` (see `internal/projects/bare_repo.go`
and `plans/bare-repo-denyCurrentBranch.md`). `drem project register`
and `--update` set it automatically; back-fill on existing
pre-pivot repos with a direct `git config`.
