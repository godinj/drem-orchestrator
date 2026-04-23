# PRD: C-Suite Agent TUI Dashboard

## Problem Statement

The drem-orchestrator TUI provides full visibility into orchestrator tasks and agents (planners, coders, testers), but has zero visibility into the C-Suite agent layer (Kyle, Mike, Alex, Seth) or the temp workers they spawn. Operators must use a separate non-interactive shell script (`csuite-status.sh --dashboard`) or manually read files in `~/.drem-csuite/` to understand what the C-Suite agents are doing, what messages they're exchanging, and what instructions temp workers received.

This forces operators to context-switch between the TUI and terminal, makes it impossible to take actions (restart agents, send messages, kill workers) without manual shell commands, and obscures the message flow that drives all C-Suite coordination.

## Solution

Add a new screen to the existing drem TUI that provides full read/write visibility into C-Suite agents and temp workers. The screen is accessible via the `q` key and shows agent health metrics, temp worker status, and the complete message flow between agents. Operators can take actions directly from this screen: send messages, restart downed agents, and kill temp workers.

Data is ingested into the database via two paths: a background goroutine in the orchestrator polls agent state files and syncs to the DB, and the `csuite_send` shell function dual-writes messages to both the filesystem inbox and the DB. The TUI reads exclusively from the DB on a 2-second polling interval.

## User Stories

1. As an operator, I want to press `q` in the TUI to switch to a C-Suite agent dashboard, so that I can see agent health without leaving the TUI.
2. As an operator, I want to see each C-Suite agent's status (alive/dead), context window usage percentage, heartbeat age, and current activity, so that I can tell at a glance which agents are healthy.
3. As an operator, I want to see active temp workers with their ID, requester, assigned task, and status, so that I can track what work is being done by temps.
4. As an operator, I want to select a C-Suite agent and see their unprocessed inbox messages, so that I can understand what's queued up for each agent.
5. As an operator, I want to see all messages involving a selected agent (sent and received, chronological), so that I can follow conversation threads.
6. As an operator, I want to toggle between inbox-only and all-messages views in the detail pane, so that I can focus on either pending work or full history.
7. As an operator, I want to select a temp worker and see their brief/instructions, current state, and completion output, so that I can understand what each worker is doing and what they were told to do.
8. As an operator, I want to send a message to any C-Suite agent from the TUI with a priority selector, subject line, and multi-line body, so that I can communicate without writing shell commands.
9. As an operator, I want the TUI to auto-generate message frontmatter (from, to, timestamp, type) when I compose a message, so that I only need to write the content.
10. As an operator, I want to restart a downed C-Suite agent directly from the TUI without a confirmation dialog, so that I can recover agents quickly.
11. As an operator, I want to kill a temp worker from the TUI, which kills the tmux pane, cleans up the worker directory, and marks the DB record as killed, so that I can stop runaway or stuck workers.
12. As an operator, I want the C-Suite dashboard to poll every 2 seconds, so that agent status is near-real-time.
13. As an operator, I want message history capped at 100 messages with oldest purged automatically, so that the database doesn't grow unbounded.
14. As an operator, I want to press `q` again (or the same toggle key) to return to the orchestrator view, so that switching is fast.
15. As an operator, I want agent context percentage displayed with the same visual treatment as orchestrator agent metrics, so that the experience is consistent across screens.
16. As an operator, I want to open an action menu on a selected agent or worker to see available actions, so that I don't need to memorize keybindings.
17. As an operator, I want dead agents to be visually distinct (color-coded) from alive agents, so that problems are immediately visible.
18. As an operator, I want to see heartbeat staleness with color thresholds (yellow >5min, red >15min), matching the existing csuite-status.sh convention.
19. As an operator, I want message priority displayed with visual distinction (color or icon), so that critical messages stand out.
20. As an operator, I want the detail pane to show full message bodies when I select a message, not just subject lines.

## Implementation Decisions

### Database Schema

Three new tables:

**`csuite_agents`** — One row per C-Suite agent, upserted on each state sync.
- `name` (PK): kyle, mike, alex, seth
- `status`: alive/dead
- `context_percent`: integer
- `heartbeat_at`: timestamp
- `current_activity`: text
- `session`: integer
- `updated_at`: timestamp

**`csuite_workers`** — One row per temp worker.
- `id` (PK): worker-001, etc.
- `requester`: spawning agent name
- `task_id`: optional orchestrator task link
- `status`: spawned/working/completed/killed
- `brief`: text (instructions sent to worker)
- `output`: text (completion summary, nullable)
- `heartbeat_at`: timestamp
- `created_at`: timestamp
- `updated_at`: timestamp

**`csuite_messages`** — Capped at 100 rows, oldest purged on insert.
- `id`: auto-increment
- `from_agent`: sender name
- `to_agent`: recipient name
- `subject`: text
- `body`: text
- `priority`: low/medium/high/critical
- `type`: observation/request/report/decision
- `read`: boolean
- `created_at`: timestamp

### Module Structure

| Module | Purpose |
|--------|---------|
| `internal/csuite/` | New package: DB models, queries (CRUD + purge), state poller goroutine |
| `internal/tui/csuite.go` | New screen model: agent table, worker table, detail pane rendering, key handling |
| `internal/tui/csuite_actions.go` | Action handlers: send message modal, restart agent (shells out to `csuite-launch.sh`), kill worker (tmux kill + dir cleanup + DB update) |
| `scripts/csuite-proto.sh` | Modified: `csuite_send` function dual-writes to agent inbox file AND inserts into `csuite_messages` table via sqlite3 |

### State Ingestion

A background goroutine in the orchestrator polls `~/.drem-csuite/*/state.md` files every 2 seconds. It parses YAML frontmatter for structured fields (`context_percent`, `heartbeat`, `current_activity`, `session`) and upserts into `csuite_agents`. Temp worker state files at `~/.drem-csuite/temp-workers/worker-*/state.md` are synced to `csuite_workers` the same way.

Agent liveness is determined by checking for a running tmux session on the `drem` socket matching the agent's session name.

### Message Ingestion

The `csuite_send` function in `csuite-proto.sh` is modified to dual-write: it continues writing the .md file to the recipient's inbox directory (backward compatible), and additionally inserts the message into `csuite_messages` via sqlite3. This is a stepping stone toward eventually deprecating file-based messaging entirely.

On insert, if the row count exceeds 100, the oldest rows are deleted.

### Screen Layout

```
┌─C-Suite Agents────────────────────┬──Temp Workers──────────────┐
│ NAME    STATUS  CTX%  HB    FOCUS │ ID    REQ   TASK   STATUS  │
│ kyle    alive   12%   2m    idle  │ w-003 mike  9340.. working │
│ mike    dead    --    15m   --    │ w-004 alex  2209.. working │
│ alex    alive   18%   <1m   loop  │                            │
│ seth    alive   22%   3m    audit │                            │
├─Messages / Detail─────────────────┴────────────────────────────┤
│ [Context-sensitive: inbox or all-messages for selected agent,  │
│  or brief/state/output for selected worker]                    │
│                                                                │
│ Toggle: [I]nbox | [A]ll messages                               │
└────────────────────────────────────────────────────────────────┘
```

- Upper left: C-Suite agent table with selectable rows
- Upper right: Temp worker table with selectable rows
- Lower: Detail pane — content depends on selection above
- Action menu triggered from selected row

### Navigation and Actions

- `q`: Toggle between orchestrator view and C-Suite view
- Arrow keys / `j`/`k`: Navigate agent and worker tables
- `Tab` or `l`/`h`: Switch focus between agent table and worker table
- `Enter` or action key: Open modal action menu for selected item
- Action menu items vary by context:
  - Agent: Send Message, Restart Agent
  - Worker: Kill Worker
- Detail pane toggles: `i` for inbox only, `a` for all messages

### Restart Mechanics

Restart shells out to `csuite-launch.sh` with the agent name. The script handles session creation on the `drem` tmux socket using the config at `~/git/drem-orchestrator.git/master/.tmux.conf`. No confirmation dialog.

### Kill Worker Mechanics

Kill performs three actions in sequence:
1. Kill the tmux pane/window for the worker on the `drem` socket
2. Remove the worker directory (`~/.drem-csuite/temp-workers/worker-NNN/`)
3. Update the `csuite_workers` DB record status to `killed`

### Alert Thresholds (Visual)

- Context >75%: yellow
- Context >85%: red
- Heartbeat stale >5min: yellow
- Heartbeat stale >15min: red
- Dead agents: red row highlight
- Message priority `critical`/`high`: red/yellow badge

### Import Constraint

The orchestrator package is at both import ceilings (6 internal imports, 55 baseline). The new `internal/csuite/` package must not be imported by `internal/orchestrator/` directly — instead, the orchestrator's state poller goroutine should be wired at the application entry point level, keeping the `csuite` package independent. The TUI package imports `csuite` for queries, which is a new import — verify this stays within the TUI package's import budget.

## Testing Decisions

All tests should verify external behavior through public interfaces, not implementation details.

### What to Test

- **DB operations** (`internal/csuite/`): CRUD for all three tables, message purge at 100-row cap, upsert idempotency for agent state, worker lifecycle transitions (spawned → working → completed/killed).
- **State poller**: Given a state.md file on disk with known content, verify the poller produces correct DB records. Test with missing files, malformed YAML, and stale heartbeats.
- **TUI rendering** (`internal/tui/csuite.go`): Given a model with known agent/worker/message state, verify the rendered output contains expected content. Test empty states (no agents, no workers, no messages).
- **Action handlers** (`internal/tui/csuite_actions.go`): Verify restart calls the correct launch script with correct arguments. Verify kill performs all three cleanup steps. Verify send-message writes correct frontmatter and inserts into DB.
- **Message composition**: Verify auto-generated frontmatter has correct fields, priority is respected, and body is preserved.

### Prior Art

- Existing TUI tests in `internal/tui/` for model updates and rendering
- Existing DB tests in `internal/model/` for GORM operations
- Test infrastructure in `internal/testutil/` — use existing factories and helpers, do not create local test helpers

## Out of Scope

- **Orchestrator task management on C-Suite screen** — task board, plan approval, and task actions remain on the existing orchestrator screen.
- **Web dashboard** — this is terminal-only.
- **Message threading** — messages are flat and chronological, not threaded into conversations.
- **Full migration from file-based to DB-based messaging** — this PRD adds dual-write as a stepping stone. Full migration is a separate future effort.
- **Historical analytics** — no context burn rate graphs, message volume charts, or trend analysis.
- **Agent-to-agent routing through DB** — agents still read from filesystem inboxes. The DB is for TUI display only (for now).
- **Persistent worker pool** — workers are ephemeral, created and destroyed per task.

## Further Notes

- This feature is **Tier 6** (new capability). It should not begin implementation until the P1 reconciler bug (22092984) is merged and the orchestrator is stable.
- The spawn worker script (934d2d44) is a soft dependency — the TUI can display temp workers regardless, but data quality improves once the script standardizes directory layout and state file format.
- The dual-write approach in `csuite_send` is intentionally a stepping stone. Once this feature is stable, a follow-up effort can deprecate filesystem inboxes entirely and route all C-Suite communication through the database.
- The existing `csuite-status.sh --dashboard` script remains useful as a lightweight monitoring tool that doesn't require the TUI. It is not deprecated by this feature.
