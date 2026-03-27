# Plan: C-Suite Watcher

> Source PRD: docs/csuite-agents/prd-csuite-watcher.md

## Architectural decisions

Durable decisions that apply across all phases:

- **Binary**: Standalone `cmd/csuite-watcher/` -- separate build artifact from `cmd/drem/`, same codebase
- **Event bus package**: `internal/eventbus/` -- pure package, no orchestrator imports. Owns SQLite DB, event publishing, delivery routing, acknowledgment, and retention
- **Database**: SQLite (WAL mode) at configurable path (default `~/.drem-csuite/csuite.db`). Three tables: `events`, `event_deliveries`, `turn_metrics`
- **Event schema**: `events` table: `id` (UUID), `event_type` (string), `source` (string), `task_id` (string, nullable), `from_status` (string, nullable), `to_status` (string, nullable), `details` (JSON text), `created_at` (datetime). `event_deliveries` table: `event_id` (FK), `agent` (string), `delivered_at` (datetime), `acked_at` (datetime, nullable). Primary key: `(event_id, agent)`
- **Turn metrics schema**: `turn_metrics` table: `id` (UUID), `agent` (string), `started_at`, `ended_at`, `duration_ms` (int), `tokens_in` (int), `tokens_out` (int), `events_processed` (int), `messages_sent` (int), `exit_status` (int), `error_details` (text, nullable)
- **Routing config**: YAML file mapping event types + optional status filters to target agent lists. Static configuration, no runtime subscriptions
- **Orchestrator hook**: Configurable `event_hook` command in TOML config. Fire-and-forget subprocess call with single JSON argument. Wired via extracted file (NOT added to orchestrator.go -- it is at 808 lines, shrink-only per constitution)
- **Agent turn command**: `claude -p "Process your turn" --system-prompt <prompt-path> --output-format json` as blocking subprocess
- **Kyle exception**: Events delivered to Kyle and status tracked, but lifecycle not managed by watcher (Kyle remains interactive in tmux)

---

## Phase 1: Event Bus -- One Event, End-to-End

**User stories**: 3, 4, 5, 6, 7, 22

### What to build

A pure `internal/eventbus/` package that owns a SQLite database with `events` and `event_deliveries` tables. The package exposes a `Bus` type with methods to publish events (insert event row + create delivery rows based on routing rules), query unacked deliveries for a given agent, and acknowledge processed events. A routing engine reads a YAML config file that maps event types and optional status filters to target agent lists. The `csuite-watcher event '<json>'` CLI command calls `Bus.Publish()` to store the event and create deliveries. An agent-facing query interface returns unacked deliveries for a given agent, and an ack interface marks them processed.

The end-to-end proof: publish a `task_status_changed` event with `to_status: failed` via CLI, verify a delivery row exists for `mike` (per routing config), query unacked deliveries for mike, ack the event, verify it no longer appears in unacked queries.

### Acceptance criteria

- [ ] `internal/eventbus/` package exists with `Bus` type, no imports from `internal/orchestrator/`
- [ ] SQLite database created at configurable path with WAL mode, `events` and `event_deliveries` tables auto-migrated
- [ ] `Bus.Publish(event)` inserts event row and creates `event_deliveries` rows based on routing rules
- [ ] `Bus.UnackedDeliveries(agent)` returns events with NULL `acked_at` for the given agent, ordered by `created_at`
- [ ] `Bus.Ack(agent, eventIDs)` sets `acked_at` on matching delivery rows; subsequent `UnackedDeliveries` excludes them
- [ ] Routing engine loads YAML config and matches event type + optional `to_status` filter to target agent list
- [ ] All events are stored in `events` table regardless of whether any routing rule matches
- [ ] `cmd/csuite-watcher/` binary skeleton with `event` subcommand that accepts a JSON argument and calls `Bus.Publish()`
- [ ] Integration tests: publish -> query unacked -> ack -> verify acked (using temp-file SQLite)
- [ ] Routing tests: rule matching with type filters, status filters, wildcard, and no-match cases
- [ ] Documentation: README section or doc comment describing event bus schema and CLI usage

---

## Phase 2: Agent Turn Lifecycle + Metrics

**User stories**: 1, 8, 9, 10, 12

### What to build

A lifecycle manager that runs an agent turn as a fresh `claude -p` subprocess. On trigger, the manager launches `claude -p "Process your turn" --system-prompt <prompt> --output-format json` as a blocking subprocess, waits for exit, parses the JSON output for token counts and session metadata, and records the turn outcome in a `turn_metrics` table in the watcher's SQLite database. Trigger deduplication ensures that if an agent is already running, new triggers are queued and cause an immediate next turn when the current one completes rather than starting a concurrent subprocess.

The end-to-end proof: trigger a turn for mike, subprocess launches (using a mock command in tests), output is parsed, `turn_metrics` row recorded with duration, tokens in/out, and exit status. A second trigger while the first is running is queued and executes after the first completes.

### Acceptance criteria

- [ ] Lifecycle manager launches `claude -p` as a blocking subprocess with `--system-prompt`, `--output-format json`, and configurable CLI path
- [ ] JSON output from `claude -p` is parsed for token counts (input/output) and exit metadata
- [ ] `turn_metrics` table auto-migrated in watcher SQLite DB
- [ ] Turn record written after subprocess exits: agent, started_at, ended_at, duration_ms, tokens_in, tokens_out, exit_status, error_details
- [ ] Trigger deduplication: concurrent trigger for same agent is queued, not started in parallel
- [ ] Queued trigger causes immediate next turn after current turn completes
- [ ] Kyle exception: lifecycle manager does not start or stop Kyle
- [ ] Integration tests with mock command simulating claude's JSON output
- [ ] Deduplication tests: concurrent triggers queue correctly
- [ ] Documentation: README section describing turn lifecycle and metrics schema

---

## Phase 3: Orchestrator Hook

**User stories**: 3, 7, 21, 22

### What to build

A configurable hook in the orchestrator that emits events to the watcher on task state transitions and agent status changes. The hook is a fire-and-forget subprocess call: the orchestrator calls the configured command with a single JSON argument containing event type, task ID, status transition, and timestamp. The hook configuration lives in the TOML config file as `event_hook`. When unconfigured or empty, no hook is called and the orchestrator behaves identically to today.

The hook implementation lives in an extracted file (NOT in `orchestrator.go`, which is at 808 lines and under a shrink-only constitution constraint). All task state transitions are emitted, not just failures.

### Acceptance criteria

- [ ] `event_hook` field added to TOML config (string, default empty)
- [ ] Hook called on every task state transition with JSON payload: `{"type":"task_status_changed","task_id":"...","from_status":"...","to_status":"...","timestamp":"..."}`
- [ ] Hook called on agent status changes with JSON payload: `{"type":"agent_status_changed","agent_id":"...","from_status":"...","to_status":"...","timestamp":"..."}`
- [ ] Hook is fire-and-forget: does not block orchestrator operation, errors logged but not propagated
- [ ] No-op when `event_hook` is empty or unconfigured -- zero behavior change for existing users
- [ ] Implementation in extracted file, NOT in `orchestrator.go` (constitution: shrink-only)
- [ ] Tests: configured hook receives expected JSON on state transition; unconfigured hook causes no subprocess call
- [ ] Documentation: config option documented with example

---

## Phase 4: Trigger System + Watcher Main Loop

**User stories**: 2, 11, 12, 13, 14, 16

### What to build

The watcher's main loop that combines three trigger sources into a unified agent wake-up system. Inbox signal watcher monitors `.signal` files in agent inbox directories. Event delivery watcher checks for new unacked deliveries after each `Publish()` call. A safety timer wakes Mike every 5 minutes regardless of other triggers. All triggers feed into the lifecycle manager from Phase 2. The watcher writes its own heartbeat to the SQLite database so Kyle can detect a dead watcher as a fallback.

Kyle receives event deliveries and has status tracked, but the trigger system does not manage Kyle's lifecycle (Kyle remains interactive in tmux).

### Acceptance criteria

- [ ] Inbox signal watcher: detects `.signal` files in `~/.drem-csuite/*/inbox/`, wakes corresponding agent
- [ ] Event delivery watcher: after `Publish()` creates deliveries, checks if target agents need waking
- [ ] Safety timer: wakes Mike every 5 minutes regardless of other triggers
- [ ] All three trigger sources feed into lifecycle manager, respecting deduplication from Phase 2
- [ ] Kyle exception: events delivered to Kyle, status tracked, lifecycle not managed
- [ ] Watcher heartbeat written to SQLite database at regular intervals
- [ ] Watcher main loop: starts all trigger sources, runs until SIGTERM/SIGINT
- [ ] Tests: signal file triggers agent wake; event delivery triggers agent wake; safety timer fires on schedule
- [ ] Documentation: trigger system overview and configuration

---

## Phase 5: Service Packaging + Resilience

**User stories**: 15, 17, 23, 24

### What to build

Production packaging for the watcher binary: a systemd service unit with automatic restart, event retention cleanup that prunes events older than a configurable age, and graceful shutdown on SIGTERM that waits for in-progress agent turns to complete before exiting.

### Acceptance criteria

- [ ] systemd service unit file with `Restart=always` and appropriate `After=` dependencies
- [ ] Retention cleanup: configurable max age (default 7 days), deletes events and their deliveries older than threshold
- [ ] Retention runs on a schedule (e.g., hourly) within the watcher process
- [ ] Graceful shutdown: SIGTERM handler waits for in-progress turns to complete, then exits cleanly
- [ ] Watcher binary is buildable as standalone artifact from `cmd/csuite-watcher/`
- [ ] Tests: retention prunes old events but keeps recent ones; shutdown completes in-progress turn
- [ ] Documentation: systemd setup instructions, retention configuration

---

## Phase 6: TUI Integration

**User stories**: 18, 19

### What to build

Update the existing TUI csuite dashboard (behind `q` key) to read from the watcher's SQLite database. The dashboard displays agent status (running/idle), current and last turn metrics (duration, events processed, outcome), token counts (per-turn and cumulative), turns completed today, and a recent events stream from the event bus.

### Acceptance criteria

- [ ] TUI csuite dashboard reads from watcher's SQLite DB (path configurable, default `~/.drem-csuite/csuite.db`)
- [ ] Agent status shows running/idle based on active turn in lifecycle manager
- [ ] Last turn metrics displayed: duration, events processed, tokens in/out, exit status
- [ ] Cumulative token totals derived from `turn_metrics` table
- [ ] Turns completed today count displayed per agent
- [ ] Recent events stream from `events` table shown in dashboard
- [ ] Falls back gracefully if watcher DB does not exist (shows "watcher not running" or similar)
- [ ] Tests: dashboard renders correctly with mock watcher DB data
- [ ] Documentation: updated TUI help text describing new dashboard columns

---

## Phase 7: Agent Prompt Rewrites

**User stories**: 20, 25

### What to build

Rewrite all five C-Suite agent prompts to use a turn-based processing model. Remove the continuous loop pattern, context management protocols (save thresholds, `restart-context.md`, `/csuite-save-and-restart`), and `csuite_wait_for_inbox`. Replace with a turn structure: read `state.md`, query unacked events on the event bus, process inbox messages, do domain-specific work, ack events, update `state.md`, and exit cleanly.

### Acceptance criteria

- [ ] All 5 agent prompts (alex, kyle, mike, ross, seth) rewritten for turn-based model
- [ ] Continuous loop instructions removed (no "NEVER halt", no `csuite_wait_for_inbox`)
- [ ] Context management removed (no save thresholds, no `restart-context.md`, no `/csuite-save-and-restart`)
- [ ] Turn structure documented in each prompt: read state -> query events -> process inbox -> work -> ack -> update state -> exit
- [ ] Event bus query and ack patterns documented in each prompt
- [ ] Kyle prompt retains interactive model but gains event bus query capability
- [ ] Ross's role simplified: no context window monitoring for non-Kyle agents
- [ ] Documentation: migration guide describing what changed and why
