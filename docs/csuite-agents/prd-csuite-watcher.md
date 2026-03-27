# PRD: C-Suite Watcher

## Problem Statement

The C-Suite agent team is designed as long-running Claude Code sessions that continuously loop: process inbox, do work, heartbeat, wait for messages, repeat. This fights against Claude's natural turn-based behavior. Agents are instructed to "NEVER halt at an idle prompt," but Claude Code sessions are fundamentally request-response -- there is no guarantee an agent will keep looping, and when it does, the idle wait burns resources (tmux sessions, potential API costs) for no productive work.

Additionally, there is no event-driven bridge between the orchestrator and the C-Suite agents. Mike must poll the orchestrator database every loop iteration to discover failures, stuck tasks, and agent deaths -- even though the orchestrator knows about these events the instant they happen. This polling approach adds latency, wastes context on repetitive queries, and means critical failures are discovered on a cadence rather than immediately.

Finally, there is no centralized tracking of agent activity. Turn duration, events processed, token usage, and context trends are not recorded anywhere, making it impossible to understand agent efficiency or debug performance issues from the TUI dashboard.

## Solution

Replace the long-running daemon pattern with a **turn-based lifecycle** managed by a standalone **watcher binary**. C-Suite agents start fresh every turn -- the watcher launches `claude -p` with the agent's system prompt and a "process your turn" message, the agent does its work, exits, and the watcher records the outcome. There is no accumulated context between turns: `state.md` is the sole persistence mechanism, and the event bus provides full visibility into what happened between turns. This eliminates context management entirely for non-Kyle agents -- no save thresholds, no restart protocols, no session tracking.

The watcher is built around three core systems:

1. **Event bus** -- a SQLite-backed event log with delivery tracking and per-agent acknowledgment. The orchestrator emits events to the watcher via a configurable hook (`csuite-watcher event '<json>'`). The watcher evaluates routing rules, creates deliveries for target agents, and wakes those agents. Agents query unacked events on the bus to understand what happened, process them, and ack. The event bus serves dual purposes: it is both a wake-up trigger mechanism and a readable log that gives agents full visibility into system activity.

2. **Agent lifecycle manager** -- manages the subprocess lifecycle for each agent. On wake-up, it launches `claude -p --system-prompt <prompt> "Process your turn" --output-format json` as a blocking subprocess, waits for exit, parses the JSON output for metrics (tokens, duration), and records the turn outcome. Each turn is a fresh session -- the agent reads `state.md` and the event bus for context, does its work, updates `state.md`, and exits.

3. **Trigger system** -- watches for conditions that should wake an agent: new `.signal` files in agent inboxes (message-driven), new unacked event deliveries (event-driven), and a 5-minute safety timer for Mike to verify event bridge accuracy. Triggers are deduplicated -- if an agent is already running its current turn, new triggers are queued until the turn completes.

The watcher is a **standalone Go binary** (`csuite-watcher`) in the same codebase as the orchestrator but compiled and managed separately. This allows agents to rebuild the watcher at runtime without affecting the running orchestrator. The watcher writes to a SQLite database that stores the event bus, delivery tracking, and turn metrics. The orchestrator's TUI reads from this database to display C-Suite agent status, turn history, and token usage.

Kyle is a special case: he remains interactive (operator talks directly in a tmux session) for now. The watcher still delivers events to Kyle and tracks his status, but does not manage his lifecycle. In the future, once the TUI supports sending messages to Kyle, Kyle can transition to the same turn-based model as the other agents, making all five agents uniform.

## User Stories

1. As an orchestrator operator, I want C-Suite agents to exit cleanly after each turn of work, so that no resources are consumed when agents are idle.
2. As an orchestrator operator, I want the watcher to start agents automatically when new messages arrive in their inbox, so that agents respond to inter-agent communication without manual intervention.
3. As an orchestrator operator, I want the orchestrator to emit events to the watcher via a configurable hook, so that the watcher can react to task state transitions, agent deaths, and other orchestrator events in real time.
4. As an orchestrator operator, I want the watcher to route orchestrator events to the appropriate C-Suite agents based on configurable routing rules, so that each agent only wakes up for events relevant to their role.
5. As an orchestrator operator, I want agents to query unacked events on the event bus when they wake up, so that they have full context about what happened since their last turn without needing a wake-up summary.
6. As an orchestrator operator, I want agents to acknowledge events after processing them, so that the watcher knows which events have been handled and does not re-deliver them.
7. As an orchestrator operator, I want all task state transitions to be recorded on the event bus (not just failures), so that agents can read the full stream to understand system state even when no action is required.
8. As an orchestrator operator, I want the watcher to run `claude -p` as a blocking subprocess with a fresh session each turn, so that the watcher knows exactly when a turn completes, can capture the output for metrics, and agents never accumulate stale context.
9. As an orchestrator operator, I want the watcher to record turn metrics (duration, tokens in/out, events processed, messages sent, exit status) after each turn, so that I can track agent efficiency and debug performance issues.
10. As an orchestrator operator, I want agents to persist all relevant state to `state.md` at the end of every turn, so that fresh sessions can fully reconstruct context from disk without needing accumulated conversation history.
11. As an orchestrator operator, I want a 5-minute safety timer for Mike, so that Mike periodically verifies the orchestrator pipeline state matches what the event bridge has reported, catching any missed or stale events.
12. As an orchestrator operator, I want the watcher to deduplicate wake-up triggers, so that an agent is not started while it is already processing a turn -- new triggers queue until the current turn completes.
13. As an orchestrator operator, I want Kyle to remain interactive in a tmux session for now, so that I can talk to Kyle directly while the other agents use the turn-based model.
14. As an orchestrator operator, I want the watcher to deliver events to Kyle and track his status even though it does not manage his lifecycle, so that Kyle can query the event bus and the dashboard reflects Kyle's state.
15. As an orchestrator operator, I want the watcher to be a standalone Go binary separate from the orchestrator, so that agents can rebuild and restart the watcher without affecting the running orchestrator.
16. As an orchestrator operator, I want the watcher to write its own heartbeat, so that Kyle can detect a dead watcher as a fallback alerting mechanism (not the default route).
17. As an orchestrator operator, I want the watcher to run as a systemd service with automatic restart, so that it recovers from crashes without manual intervention.
18. As an orchestrator operator, I want the TUI csuite dashboard to display agent status, current/last turn metrics, and token counts (per-turn and cumulative) from the watcher's database, so that I can monitor the C-Suite team at a glance.
19. As an orchestrator operator, I want the TUI to show a recent events stream from the event bus, so that I can see what's happening in the system in real time.
20. As an orchestrator operator, I want agent prompts rewritten to use a turn-based processing model instead of a continuous loop, so that agents work naturally with Claude's request-response pattern.
21. As an orchestrator operator, I want the orchestrator hook to be configurable (enable/disable, command path), so that the orchestrator behaves exactly as it does today when the hook is disabled.
22. As an orchestrator operator, I want the orchestrator hook to accept a JSON payload as a single argument, so that the interface is extensible without changing the CLI contract when new event fields are added.
23. As an orchestrator operator, I want the event bus to support retention cleanup (prune events older than a configurable age), so that the database does not grow unbounded.
24. As an orchestrator operator, I want the watcher and orchestrator packaged in the same codebase, so that this setup can be migrated to new projects as a single unit.
25. As an orchestrator operator, I want the future option to transition Kyle to turn-based mode once the TUI supports sending messages, so that all five agents eventually use a uniform lifecycle model.

## Implementation Decisions

### Watcher Binary

The watcher is a standalone Go binary (`csuite-watcher`) compiled from `cmd/csuite-watcher/` in the orchestrator repository. It is distinct from the orchestrator binary (`drem`) -- same codebase, separate build artifact. This allows agents to rebuild and restart the watcher at runtime without affecting the running orchestrator process.

### Event Bus (SQLite)

The event bus uses a SQLite database (WAL mode for concurrent read/write) stored at a configurable path (default: `~/.drem-csuite/csuite.db`). Two core tables:

**`events`**: All orchestrator events. Columns: `id`, `event_type`, `source`, `task_id`, `from_status`, `to_status`, `details` (JSON), `created_at`. All task state transitions are recorded, not just failures.

**`event_deliveries`**: Per-agent delivery and acknowledgment tracking. Columns: `event_id` (FK to events), `agent`, `delivered_at`, `acked_at` (NULL until processed). Primary key: `(event_id, agent)`. The watcher creates delivery rows when an event is published. Agents query unacked deliveries, process them, and update `acked_at`.

Retention: a configurable prune job deletes events (and their deliveries) older than a threshold (default: 7 days).

### Routing Engine

A YAML configuration file defines which event types route to which agents:

```yaml
routing:
  - event: task_status_changed
    to_status: [failed]
    wake: [mike]

  - event: task_status_changed
    to_status: [done, merging]
    wake: [seth]

  - event: agent_status_changed
    to_status: [dead]
    wake: [mike, ross]

  - event: task_filed
    wake: [alex]
```

All events are recorded on the bus regardless of routing. Routing only determines which agents get `event_deliveries` rows (and therefore which agents are woken). Agents can query the full `events` table to see everything.

Static configuration is sufficient initially. Runtime subscription changes are out of scope.

### Orchestrator Hook

The orchestrator is modified to call a configurable external command on task state transitions and agent status changes. Configuration:

```yaml
event_hook: "csuite-watcher event"
```

The orchestrator calls the hook with a single JSON argument:

```bash
csuite-watcher event '{"type":"task_status_changed","task_id":"42","from_status":"planning","to_status":"failed","details":"context exhaustion","timestamp":"2026-03-26T14:30:00Z"}'
```

The hook is fire-and-forget from the orchestrator's perspective -- it should not block orchestrator operation. If the hook command is empty or unconfigured, no hook is called and the orchestrator behaves exactly as it does today.

Events emitted by the hook include all task state transitions (not just failures) and agent status changes. Git post-merge events also go through this hook.

### Agent Lifecycle Manager

The lifecycle manager runs each agent turn as a fresh session in a blocking subprocess:

```bash
claude -p "Process your turn" \
  --system-prompt docs/csuite-agents/prompts/mike.md \
  --output-format json \
  --dangerously-skip-permissions
```

Every turn is a new session. There is no accumulated context between turns -- agents read `state.md` and the event bus at the start of each turn to reconstruct context, do their work, update `state.md`, and exit. This eliminates context management entirely: no save thresholds, no `context_save_requested` events, no `restart-context.md`, no session tracking.

The `--output-format json` flag provides structured output including token counts and session metadata. The lifecycle manager parses this output after the process exits to record turn metrics.

**Kyle exception**: Kyle is not managed by the lifecycle manager. He runs in a tmux session interactively. The watcher still creates `event_deliveries` for Kyle and records his status (by reading his heartbeat from `state.md`), but does not start or terminate him.

### Trigger System

Three trigger sources, all feeding into the lifecycle manager:

1. **Inbox signal watcher**: Uses `inotifywait` to watch `~/.drem-csuite/*/inbox/.signal` files. When a signal is detected, wakes the corresponding agent.

2. **Event delivery watcher**: After `eventbus.Publish()` creates new deliveries, the trigger system checks if the target agents need waking.

3. **Safety timer**: Every 5 minutes, wakes Mike regardless of other triggers. Mike uses this turn to verify the orchestrator pipeline state matches the event bus, catching any missed or stale events from the bridge.

**Deduplication**: If an agent is already running (subprocess active), new triggers are queued. When the current turn completes, queued triggers cause an immediate next turn rather than waiting for a new trigger.

### Turn Metrics

Stored in the same SQLite database as the event bus. Table: `turn_metrics`.

Columns: `id`, `agent`, `started_at`, `ended_at`, `duration_ms`, `tokens_in`, `tokens_out`, `events_processed`, `messages_sent`, `exit_status`, `error_details`.

Per-turn token counts come from the `claude -p --output-format json` response. Cumulative totals are derived by summing over `turn_metrics`.

### TUI Integration

The existing TUI csuite dashboard screen (behind `q` key) is updated to read from the watcher's SQLite database instead of (or in addition to) polling disk state files. The dashboard displays:

- Agent status (running / idle)
- Current turn: duration so far, events being processed
- Last turn: duration, events processed, outcome
- Tokens in/out: per last turn and cumulative total
- Turns completed today
- Recent events stream from the event bus

### Agent Prompt Rewrites

All five agent prompts are rewritten to replace the continuous loop model with a turn-based model. The core loop sections are replaced with:

```markdown
## Turn Structure

You start fresh every turn. Your `state.md` and the event bus are your memory.

1. Read `~/.drem-csuite/<you>/state.md` for prior context
2. Source protocol library
3. Query unacked events: `sqlite3 ~/.drem-csuite/csuite.db "SELECT e.* FROM events e JOIN event_deliveries d ON e.id = d.event_id WHERE d.agent = '<you>' AND d.acked_at IS NULL ORDER BY e.created_at"`
4. Process inbox messages (read, respond, archive)
5. Do your domain-specific work based on events and messages
6. Ack processed events: `sqlite3 ~/.drem-csuite/csuite.db "UPDATE event_deliveries SET acked_at = datetime('now') WHERE agent = '<you>' AND event_id IN (...)"`
7. Update state.md with current context summary, active patterns, recent decisions
8. Exit cleanly -- you will be started again when there is new work
```

Instructions like "NEVER halt at an idle prompt", the `csuite_wait_for_inbox` mechanism, and all context management protocols (save thresholds, `restart-context.md`, `/csuite-save-and-restart`) are removed entirely. Agents start fresh and exit after every turn. `state.md` is the sole persistence mechanism.

### Watcher Resilience

The watcher runs as a systemd service with automatic restart (`Restart=always`). As a fallback, it writes its own heartbeat to the SQLite database. Kyle can detect a stale watcher heartbeat when queried by the operator, but this is a fallback alerting mechanism -- Kyle does not actively poll the watcher's health.

## Testing Decisions

A good test for this feature verifies observable behavior through the module's public interface -- event publishing, delivery routing, acknowledgment state, lifecycle decisions, and metric recording -- without mocking internal implementation details or requiring live Claude Code sessions.

### Modules to test

**Event Bus**: Test event publishing (insert + delivery creation based on routing rules), unacked queries (correct filtering by agent and ack state), acknowledgment (acked_at updated correctly, subsequent unacked queries exclude acked events), and retention pruning (events older than threshold are deleted along with their deliveries). These are pure database operations testable with an in-memory or temp-file SQLite database. Prior art: existing orchestrator tests that verify state transitions through database queries in `internal/csuite/`.

**Routing Engine**: Test rule matching (event type + status filters produce correct agent lists), wildcard/catch-all rules, and rules that match no agents. Test that all events produce delivery rows for routed agents and no delivery rows for non-routed agents. These are pure functions operating on routing config and event data. Prior art: existing orchestrator tests that use configuration-driven behavior.

**Agent Lifecycle Manager**: Test metric recording (turn outcome is correctly written to turn_metrics table with tokens in/out, duration, exit status), trigger deduplication (agent is not started while already running, triggers queue correctly), and the full turn cycle (subprocess launches, output is parsed, metrics recorded). The subprocess launch itself can be tested with a mock command that simulates claude's JSON output. Prior art: existing agent runner tests in `internal/agent/` that manage subprocess lifecycles.

## Trigger System Implementation

### EventDeliveryTrigger

`EventDeliveryTrigger` is the event-driven wake-up source for C-Suite agents. It reads agent name lists from a `<-chan []string` channel and calls `TriggerAgent` on each agent in the list, processing each notification fully before consuming the next.

**Behavior:**

- Notifications are consumed sequentially. The next notification is not read from the channel until every agent in the current notification has been passed to `TriggerAgent`. This prevents notification bursts from interleaving agent triggers.
- Each notification is a `[]string` containing the names of agents that have new unacked event deliveries. The routing layer (outside the watcher package) computes this list from the event deliveries and pushes it onto the channel.
- `Run(ctx)` blocks until the context is cancelled or the channel is closed. It is safe to cancel the context at any time.

**Kyle exception:** "kyle" is silently skipped. If a notification contains `["mike", "kyle", "ross"]`, only `TriggerAgent("mike")` and `TriggerAgent("ross")` are called. Kyle's delivery records are created and tracked in the event bus (so Kyle can read his event history), but the trigger system never attempts to manage his lifecycle.

**Decoupling from eventbus:** The `watcher` package does not import `eventbus`. The channel is the integration seam. In production, the routing layer calls `Bus.Publish()`, creates delivery rows via `Bus.Deliver()`, computes the target agent list, and sends it to the channel. In tests, the test itself does this directly, demonstrating that the two subsystems are independently testable.

### SafetyTimer

`SafetyTimer` periodically calls `TriggerAgent("mike")` at a fixed interval, regardless of event delivery activity. It is a safety net ensuring Mike processes operational state even if the event bridge misses something.

**Behavior:**

- The first tick fires after one full interval has elapsed from `Start()` — not immediately. This prevents a spurious wake immediately on startup.
- The interval is configurable. If zero is passed to `NewSafetyTimer`, the default of 5 minutes is used.
- `Stop()` halts the ticker and blocks until the background goroutine exits. After `Stop()` returns, no further `TriggerAgent` calls will be made.
- Only "mike" is ever triggered. The SafetyTimer never references any other agent name.

**Kyle exception:** Kyle is never triggered by the SafetyTimer. The 5-minute safety interval applies only to Mike's operational verification role.

**Independence:** SafetyTimer is fully decoupled from EventDeliveryTrigger and from the event bus. It runs a simple `time.Ticker` loop in a background goroutine and requires no external state.

### Kyle Exception

The Kyle exception is enforced at the trigger level, not the lifecycle manager level. This means:

1. Events published to the bus are delivered to Kyle and persisted in the `event_deliveries` table. Kyle can query his unacked deliveries when he is active in his tmux session.
2. The `event_deliveries` rows for Kyle are created just like for any other agent, so the dashboard and event history are complete.
3. Neither `EventDeliveryTrigger` nor `SafetyTimer` will ever call `TriggerAgent("kyle")`. Filtering early prevents the lifecycle manager from even receiving a wake request for Kyle.

### Watcher Package Decoupling

The `watcher` package has zero imports of `internal/` packages. It receives agent names via `<-chan []string` (for event-driven triggers) and `AgentTriggerer` (for turn execution). The eventbus, routing engine, and any other orchestration logic live outside the watcher package boundary.

This design keeps the `watcher` package's import count at zero internal packages, well within the 6-import constitution ceiling, and allows each subsystem to be tested independently:

- `eventbus` tests verify publish, deliver, poll, ack, and unacked query behavior with a real SQLite database.
- `watcher` unit tests verify trigger filtering, sequential processing, and timer behavior with mock channels and mock triggerers.
- Integration tests (in `package watcher_test`) import both packages to wire the channel-based seam and verify end-to-end behavior: `Bus.Publish()` → `Bus.Deliver()` → channel notification → `EventDeliveryTrigger` → `TriggerAgent` called for the correct agents.

## Out of Scope

- **Kyle's transition to turn-based mode** -- Kyle remains interactive for now. The uniform turn-based model for Kyle depends on TUI messaging support, which is a separate feature.
- **Dynamic routing subscriptions** -- agents cannot modify routing rules at runtime. Static YAML configuration is sufficient initially.
- **Multiple watcher instances** -- the watcher is a single process. High-availability or distributed operation is not addressed.
- **Changes to the disk-based messaging protocol** -- the existing `csuite_send`/`csuite_inbox`/`csuite_archive` protocol remains unchanged. The event bus supplements but does not replace inter-agent messaging.
- **Temp worker lifecycle changes** -- temp workers continue to be spawned by Mike and managed per existing protocols. The watcher does not manage temp worker lifecycles (though this could be a future enhancement).
- **Orchestrator core changes beyond the hook** -- the orchestrator's task processing, agent management, and merge infrastructure are not modified. Only the configurable event hook is added.

## Further Notes

- The event bus and the disk-based inbox serve complementary purposes. The event bus carries orchestrator-originated events (task transitions, agent status changes). The disk-based inbox carries inter-agent messages (observations, requests, reports, decisions). Agents check both when they start a turn. Over time, the inbox protocol may migrate to the event bus, but that is not in scope here.
- The watcher's turn-based model aligns Claude Code's natural behavior with the system's needs. Instead of agents fighting to stay alive, they do their work and exit. The watcher handles scheduling and metrics externally. This should result in more reliable agent behavior and lower resource consumption.
- Fresh starts every turn eliminate context management entirely for non-Kyle agents. There are no save thresholds, no `restart-context.md`, no `/csuite-save-and-restart`, no session tracking. `state.md` is the sole persistence mechanism -- agents curate it at the end of every turn, and it serves as compact, high-signal memory that is strictly better than raw accumulated conversation history (which is mostly stale tool output and query results). This dramatically simplifies Ross's role: Ross no longer monitors context windows or orchestrates save/restart cycles for non-Kyle agents. Ross's remaining responsibilities are temp worker lifecycle management, Kyle's context monitoring (since Kyle still accumulates context as an interactive session), and workforce gap identification.
- The `claude -p --output-format json` response provides structured data including token counts, which the lifecycle manager parses for metrics. If the JSON schema changes in future Claude Code versions, the parser will need updating, but the interface is stable enough for this use case.
- The 5-minute safety timer for Mike is a pragmatic concession. In an ideal system, the event bridge captures everything and no polling is needed. The timer exists to catch edge cases where the hook fails silently or an event is missed. If the event bridge proves reliable, the timer interval can be extended or removed.
