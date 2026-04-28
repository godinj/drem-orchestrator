# C-Suite Chat Persona Restart Control Plan

## Problem

Persona containers can generate a burst of replies after restart because the persona poller immediately scans its inbox on boot and processes every pending `*.md` file in mtime order. At the same time, `csuite-watcher` runs a startup rescan that can deliver any unledgered outbox files into persona inboxes. The current design is safe for message recovery, but it gives the operator little control over which pending messages should still be answered after containers have been stopped and restarted.

## Goals

- Prevent restart-time reply bursts without losing recoverability.
- Let the operator review and ignore/archive pending persona inbox messages before a persona restarts.
- Let the operator stop, start, and recreate individual or all persona containers from `csuite-chat` through a narrow safe control surface.
- Show when each persona is currently generating a response.
- Keep destructive or broad Docker operations out of the TUI.

## Current Code Paths

- Persona runtime:
  - `cmd/csuite-persona/main.go`
  - `internal/csuite/persona/persona.go`
  - `internal/csuite/persona/poller.go`
  - `deploy/docker/context/csuite-entrypoint.sh`
  - `internal/projects/templates/project-compose.yml.tmpl`
- Watcher rescan and delivery:
  - `cmd/csuite-watcher/serve.go`
  - `internal/deliver/rescan.go`
  - `internal/deliver/deliver.go`
- Bridge and disk-backed message store:
  - `internal/serve/serve.go`
  - `internal/serve/messages.go`
  - `internal/csuite/diskstore/diskstore.go`
  - `internal/csuite/diskstore/reader.go`
  - `internal/bridgeclient/client.go`
  - `internal/bridgeclient/types.go`
- Chat TUI:
  - `cmd/csuite-chat/main.go`
  - `internal/tui/chat/model.go`
  - `internal/tui/chat/commands.go`
  - `internal/tui/chat/keys.go`
  - `internal/tui/chat/view.go`

## Phase 1: Persona Runtime Guardrails

Add runtime controls to `internal/csuite/persona.Config`:

- `StartupQuietPeriod time.Duration`
- `StartupDrain bool`
- `MaxMessagesPerScan int`
- `MaxMessagesAtBoot int`
- `RuntimeStateFile string`

Suggested environment and flag names:

- `DREM_CSUITE_STARTUP_QUIET_PERIOD`, `-startup-quiet-period`
- `DREM_CSUITE_STARTUP_DRAIN`, `-startup-drain`
- `DREM_CSUITE_MAX_MESSAGES_PER_SCAN`, `-max-messages-per-scan`
- `DREM_CSUITE_MAX_MESSAGES_AT_BOOT`, `-max-messages-at-boot`
- `DREM_CSUITE_RUNTIME_STATE_FILE`, `-runtime-state-file`

Behavior changes:

- Remove the unbounded immediate startup scan.
- During startup quiet period, write runtime state as `boot_quiet` and do not process inbox messages.
- If startup drain is enabled, move live inbox messages to an ignored/drained location without invoking `opencode`.
- If startup drain is disabled, cap the first scan with `MaxMessagesAtBoot`.
- Cap every normal scan with `MaxMessagesPerScan` when it is greater than zero.
- Write a runtime state file before and after model invocation so external surfaces can display `processing`.

Runtime state should live at `<persona-root>/runtime.json` by default and include:

- persona
- status: `starting`, `boot_quiet`, `idle`, `processing`, `error`, `stopping`
- boot ID
- started_at
- quiet_until
- current inbox filename
- current turn started_at
- last processed filename
- last exit code
- last error

Tests:

- Startup quiet period suppresses immediate processing.
- Max boot messages limits first scan.
- Max messages per scan limits later scans.
- Startup drain moves files and does not spawn.
- Runtime state is written for idle and processing transitions.

## Phase 2: Queue Review API

Add queue-specific API endpoints instead of overloading `/api/messages`:

- `GET /api/inbox?agent=mike&limit=50&include_archived=false`
- `POST /api/inbox/archive`
- `POST /api/inbox/ignore`

Request body for archive/ignore:

```json
{
  "agent": "mike",
  "id": "message-id",
  "reason": "operator restart review"
}
```

Add bridge client methods:

- `GetInboxQueue(ctx, agent string, limit int) ([]InboxQueueItem, error)`
- `ArchiveInboxItem(ctx, agent, id, reason string) error`
- `IgnoreInboxItem(ctx, agent, id, reason string) error`

Diskstore behavior:

- Queue list reads live `<root>/<agent>/inbox/*.md` by default.
- Archive moves live file to `<root>/<agent>/inbox/.archive/<filename>`.
- Ignore moves live file to `<root>/<agent>/inbox/.ignored/<filename>`.
- Never delete ignored messages.
- Resolve IDs from the same stable message ID logic used by existing inbox readers.

Tests:

- Live queue excludes archived and ignored messages by default.
- Archive and ignore move only the selected live file.
- Unknown persona and unknown message ID are rejected.
- HTTP auth and validation failures return appropriate status codes.

## Phase 3: TUI Queue Review

Add an inbox queue mode to `internal/tui/chat`.

Suggested keys:

- `i`: open queue for active persona.
- `r`: refresh queue.
- `up/down`: move cursor.
- `enter`: preview selected item.
- `a`: archive selected item with confirmation.
- `x`: ignore selected item with confirmation.
- `esc`: return to chat.

The queue screen should show:

- persona name
- pending count
- created time or mtime
- from agent
- subject
- short body preview
- filename or ID for auditability

Tests:

- `i` enters queue mode and fetches queue.
- Archive/ignore actions require a selected item.
- Successful archive/ignore refreshes the queue.
- Esc returns to chat without losing typed input.

## Phase 4: Safe Container Control Surface

Do not let `csuite-chat` invoke arbitrary Docker commands. Add a narrow allowlisted backend surface.

Endpoints:

- `GET /api/personas/containers`
- `POST /api/personas/control`

Control request:

```json
{
  "target": "seth",
  "action": "recreate"
}
```

Allowed targets:

- `mike`
- `alex`
- `seth`
- `kyle`
- `all`

Allowed actions for first pass:

- `stop`
- `start`
- `recreate`

Defer `rebuild` until the restart/recreate flow is proven.

Implementation constraints:

- Map targets internally to `csuite-mike`, `csuite-alex`, `csuite-seth`, `csuite-kyle`.
- Never accept arbitrary service names.
- `start` and `recreate` must use `docker compose up -d --no-deps`.
- Do not touch global services such as `sglang`, `gq`, or registry.
- Prefer a backend executor with an allowlisted argv builder and tests.

Tests:

- Unknown target and unknown action are rejected.
- Generated Docker Compose argv matches the allowlist.
- `all` expands only to persona services.
- `up` commands always include `--no-deps`.

## Phase 5: TUI Container Control

Add a control mode to `internal/tui/chat`.

Suggested keys:

- `c`: open control screen.
- `s`: stop selected persona.
- `S`: start selected persona.
- `R`: recreate selected persona.
- `A`: select all personas target.
- `y`: confirm pending action.
- `n` or `esc`: cancel.

The control screen should show:

- container status: running, stopped, restarting, unknown
- runtime state: idle, boot_quiet, processing, error
- current inbox filename when processing
- pending queue count
- warning when an action may kill an in-flight turn

Tests:

- Mutating actions require confirmation.
- Control status refresh updates the model.
- Failed control actions surface in the status/error line.

## Phase 6: Compose Defaults And Docs

Update the project compose template and entrypoint after the runtime flags are available.

Candidate defaults:

```yaml
DREM_CSUITE_STARTUP_QUIET_PERIOD: "30s"
DREM_CSUITE_STARTUP_DRAIN: "false"
DREM_CSUITE_MAX_MESSAGES_PER_SCAN: "1"
DREM_CSUITE_MAX_MESSAGES_AT_BOOT: "1"
```

Rationale:

- Default quiet period prevents immediate restart bursts.
- Default no drain avoids silently skipping legitimate work.
- Max one boot message and max one message per scan make catch-up gradual.
- The TUI can enable drain/ignore explicitly when the operator wants to discard stale queue items.

Docs to update:

- `docs/containerization/install.md`
- relevant C-Suite prompt/runtime docs if they mention immediate polling behavior.

## Risks And Open Questions

- Startup drain should probably not default to true because it can skip legitimate work without operator review.
- Ignore should use `.ignored/` so operator-skipped messages are distinct from processed archive files.
- Container control likely needs either host-side execution or a privileged backend. The TUI should not own raw Docker access.
- Rebuild is materially riskier than stop/start/recreate and should be a later phase.
- Stopping a persona during `processing` can kill an in-flight turn. The TUI must show and confirm that risk.
- Kyle has a writeable plans mount, so stopping Kyle can interrupt plan writes; this deserves a stronger warning.
