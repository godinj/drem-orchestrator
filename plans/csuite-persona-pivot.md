# Plan: csuite-persona pivot

**Status:** implemented (2026-04-20, Wave 2 of the csuite-docker end-to-end rollout).

**Related:**

- `docs/containerization/install.md` §"C-Suite personas: the persona poller runtime" — operator view.
- `docs/prd-containerization.md` — Architecture §Warm containers bullet.
- Wave 1 commits: `918c421`, `7fa9e85`, `a80d9dd` (operator home + credentials bind-mounts, settings preseed, pinned claude CLI).

## Rationale

The Wave-1 csuite runtime (`deploy/docker/context/csuite-run.sh`) did:

```bash
exec claude --print --system-prompt-file /opt/csuite/prompts/$CSUITE_AGENT.md
```

This launches a long-lived, interactive `claude` process that blocks on
stdin. Inside a compose-launched container there is no TTY feeding that
process — so the CLI sits forever, reachable only over its own stdin
(which nothing writes) or via the `csuite-watcher` service's external
turn-driver path.

The practical consequence: the inbox dispatch pattern operators expect
from the C-Suite docs — "drop an `.md` file into `~/.drem-csuite/seth/inbox/`
and get a reply in `outbox/`" — never actually worked against the
containerised personas. The interactive CLI had no file-watcher and the
`csuite-watcher` path was designed around its own event-bus deliveries,
not the file-based inbox protocol. Users trying to run the documented
workflow hit a dead end.

Wave 2 replaces the interactive entrypoint with a dedicated polling
binary (`cmd/csuite-persona`) that scans the inbox directly and invokes
`claude -p` (non-interactive, one-shot) per message. Each message is a
fresh claude invocation, which keeps state.md as the sole shared memory
surface and makes the "what did Seth last do?" question answerable from
a single file read.

## Architecture

```
┌────────────────────────────────────────────────────────────────────┐
│ csuite-<persona> container                                         │
│                                                                    │
│   tini (PID 1)                                                     │
│     └─ csuite-entrypoint.sh (reads $CSUITE_AGENT)                  │
│          └─ csuite-persona -persona <name>  (the poll loop)        │
│                                                                    │
│   Every 2 s:                                                       │
│     1. ReadDir /home/drem/.drem-csuite/<persona>/inbox             │
│     2. For each .md sorted by mtime asc:                           │
│          - ReadFile                                                │
│          - exec.CommandContext("claude", "--dangerously-skip-      │
│              permissions", "-p", <body>, "--system-prompt",        │
│              <prompt>, "--output-format", "text")                  │
│            (5-min timeout, SIGKILL on cancel)                      │
│          - stdout → outbox/<ts>-<persona>-reply-<shortid>.md       │
│          - inbox/<name> → inbox/.archive/<name>                    │
│          - Atomic replace state.md                                 │
│     3. On non-zero exit: bump <name>.failures; after N failures    │
│        (default 3) archive as <name>.failed so the loop advances.  │
│                                                                    │
│   On SIGTERM: cancel ctx at next tick boundary, drain in-flight    │
│   claude -p invocation, exit 0.                                    │
└────────────────────────────────────────────────────────────────────┘
```

### Key module boundaries

- `cmd/csuite-persona/main.go` — thin CLI wrapper. Flag parsing, slog
  JSON handler wiring, signal notification. ~90 lines.
- `internal/csuite/persona/persona.go` — `Config`, `Spawner` interface,
  `ApplyDefaults`/`Validate`.
- `internal/csuite/persona/poller.go` — the `Poller.Run` loop and all
  filesystem side effects (inbox scan, outbox write, state.md atomic
  replace, sidecar counter).
- `internal/csuite/persona/subprocess.go` — production `Spawner` that
  launches `claude` via `os/exec.CommandContext` with `Setpgid` so the
  cancel path kills the whole process group.

### Authentication — subscription-only

The poller never reads or sets `CLAUDE_CODE_OAUTH_TOKEN`,
`ANTHROPIC_API_KEY`, or `ANTHROPIC_AUTH_TOKEN`. The `claude` CLI reads
its credentials from the read-only bind-mount at
`/home/drem/.claude/.credentials.json` that Wave 1 (commit `7fa9e85`)
added to the compose template. An earlier Wave-2 design draft proposed
using `CLAUDE_CODE_OAUTH_TOKEN` as a stop-gap; the operator explicitly
rejected that path (see CLAUDE.md "Authentication: subscription-only").

The `Spawner` interface in `internal/csuite/persona/persona.go`
intentionally does not carry a token field. Tests confirm the production
spawner never sets any Claude token env var.

## Commit sequence

The pivot landed on `worktree-agent-a3723e3f` in six commits against
master HEAD `a80d9dd`:

| # | SHA        | Scope                                                                 |
|---|------------|-----------------------------------------------------------------------|
| 1 | `0e26a66`  | feat(cmd): add csuite-persona headless poller binary                  |
| 2 | `61d42c8`  | test(csuite-persona): unit tests for poll loop, spawner, archive      |
| 3 | `cf56d3e`  | feat(csuite-base): install csuite-persona and swap entrypoint         |
| 4 | `50d20b4`  | test(csuite): integration test for inbox -> claude -p -> outbox       |
| 5 | `1e8ba75`  | docs(containerization): document csuite-persona runtime pivot         |
| 6 | *(this doc)* | docs(plans): csuite-persona pivot implemented                      |

All six commits pass `go build ./...`, `go vet ./...`, and
`go test ./...` (plus `go test -tags=integration ./cmd/csuite-persona/...`
for the integration file, which is opt-in).

## Design trade-offs

### Polling, not fsnotify (deferred)

The first cut uses a 2 s mtime-ordered directory scan rather than
`fsnotify`/inotify. Rationale:

- A 2 s P99 wake-up is acceptable for operator-driven turns.
- The poll loop is simpler to reason about under container restarts: on
  startup it does a single full scan (no race against the OS event
  queue), which means any messages that landed while the container was
  down are picked up on the first tick.
- fsnotify's Linux inotify backend has quirks around bind-mounted
  directories (events sometimes do not fire when the inode is touched
  on the host rather than inside the container).

Migration to fsnotify is tracked as a follow-up. The main risk to
watch: inotify watches attach to inode numbers, and bind-mounts across
different filesystems can drop events. If we pivot, the test matrix must
cover both same-filesystem and tmpfs bind-mount scenarios.

### `claude -p` argv idiom

The Go `Spawner` invokes:

```
claude --dangerously-skip-permissions \
       -p "<message body>" \
       --system-prompt "<prompt content>" \
       --output-format text
```

This matches the shape already used by `internal/watcher/subprocess.go`
(`RunClaudeSubprocess`) and `internal/watcher/lifecycle.go`. The prompt
content is passed as a string value rather than a file path (the
`csuite-run.sh` legacy script used `--system-prompt-file`). Reading the
prompt once at poller startup and passing the string avoids the
filesystem syscall on every invocation.

Open question for Kyle to sanity-check: should we use
`--output-format json` and parse the Claude CLI's structured output
for metrics (tokens, duration)? The current `text` choice keeps the
outbox file human-readable, which was the stated UX goal — the JSON
shape is an optimisation if/when Kyle wants to surface per-message
token counts in the dashboard.

### Failure budget (N=3)

Three attempts before archiving as `.failed` is a judgement call:

- N=1 would drop transient network glitches (common when the container
  first starts and the credentials file has not been refreshed in a
  while) straight into `.failed` with no retry.
- N=5 would mean a permanently-bad prompt keeps the loop spinning for
  at least 30 s (6 ticks × 5 retries) before advancing.
- N=3 is the Goldilocks choice: one retry for the credential-refresh
  race, one retry for a transient 503 from Anthropic, one retry for
  good luck, then give up and archive.

Overridable via `-max-failures`.

### 5-minute `claude -p` timeout

Matches the longest-observed healthy turn in the current watcher
runtime. The interactive `claude` CLI in `internal/watcher/subprocess.go`
currently uses a 99-minute timeout; that is inappropriate for the
persona use case because:

- Persona turns are a single prompt, not an agentic loop with tool use.
- If a persona's `claude -p` takes longer than five minutes, something
  is wrong upstream (Anthropic 5xx, OAuth refresh hang) and the right
  response is to fail fast, archive as `.failed`, and let the operator
  investigate — not block the loop.

## Deferred items

- **fsnotify migration.** Same semantics, smaller wake-up latency, but
  fragile on bind-mounts. Separate plan doc when picked up.
- **`claude -p --resume` for cross-message memory.** Per CEO decision:
  each message is currently a fresh session; state.md is the only
  cross-message memory surface. Adding `--resume <session-id>` would
  let the persona carry conversation context across messages, at the
  cost of making state harder to reason about. Defer pending clear UX
  need.
- **Worker-base preseed analogous to this one.** The workers still
  depend on a working `~/.claude/` inside the container; the preseed
  story that Wave 1 added for csuite (`claude-code-onboarding.json` +
  `claude-code-settings.json`) has not been replicated in
  `worker-base.Dockerfile`. Tracked as a separate drive-by task.
- **Structured metrics.** The JSON log handler emits `duration_ms`
  per message; agentmon does not yet ingest the csuite-persona stream.
  Adding a structured log consumer that POSTs per-message metrics to
  the orchestrator HTTP API would close the Kyle-visibility gap.
