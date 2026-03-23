# PRD: C-Suite Agent Team

## Problem Statement

The drem-orchestrator requires significant manual effort to operate. The workflow contains numerous bugs, and fixing one bug frequently reveals others. The operator must manually triage failures, prioritize the backlog, monitor agent health, verify architectural adherence, and manage the overall development pipeline — all of which prevents the orchestrator from becoming a tool that can be left to run autonomously.

There is no feedback loop for continuous improvement: no agent watches operational health to surface systemic issues, no agent ensures the right features are being built in the right order, no agent guards the constitution and architecture as code is merged, and no agent manages the workforce of temporary operators that could be doing this hands-on work. The operator is the single point of failure for all of these concerns.

The goal is to create a self-improving layer: a team of executive-level Claude Code agents that actively use, monitor, and iterate on the orchestrator — filing bugs, designing features, prioritizing work, and guarding quality — until the orchestrator becomes viable for the operator to use with minimal manual intervention.

## Solution

Introduce a **C-Suite agent team** that operates entirely outside the drem-orchestrator Go binary. These are long-running Claude Code sessions, each with a distinct executive role, that coordinate via disk-based inboxes and manage a pool of temporary operator agents. Together, they form a self-improvement loop around the orchestrator.

The team consists of five agents:

- **Kyle (CEO)** — the operator's direct interface. Reactive hub that receives reports from other C-Suite agents, writes summaries to disk, and delegates work. Kyle starts the other agents when the operator begins a conversation.
- **Mike (COO)** — monitors the orchestrator's database for operational issues (failure rates, stuck tasks, agent deaths). Works with Alex on next steps. Spawns temporary operator agents to run the orchestrator and observe its behavior.
- **Alex (CPO)** — owns product direction. Prioritizes the backlog based on pain points and roadmap understanding. Reviews filed issues, bug reports, and PRDs. Uses the "grill me" skill with the operator to design new features. Can consult other C-Suite agents for input.
- **Ross (Chief HR)** — manages the workforce. Monitors context window health of all agents (C-Suite and temp workers) via ctxmon, triggers save-to-disk when thresholds are reached, and orchestrates agent restarts with restored context. Also identifies when new C-Suite roles are needed and onboards them.
- **Seth (CTO)** — guards technical quality. Watches everything merged into master and iteratively verifies adherence to the constitution and architecture decisions. Uses the "repo-audit" skill for lightweight incremental checks rather than deep audits on every merge.

**Temporary operator agents** are spawned by Mike and lifecycle-managed by Ross. They run the orchestrator, observe its behavior, write bug reports, and verify that tests pass — but do not make code changes themselves. One temp worker runs at a time initially.

Key properties:
- **External to the orchestrator**: The C-Suite agents are not managed by the drem-orchestrator software. They manage and iterate on it.
- **Disk-based communication**: Agents communicate via a shared directory structure with inboxes, outboxes, and state files. No inter-process messaging.
- **Self-managing context**: Each agent monitors its own context window usage, saves state to disk when approaching limits, and restarts with restored context. The existing memory system is used for persistence across restarts.
- **Always-on (ideally)**: C-Suite agents are intended to run continuously, though machine restarts and other conditions may interrupt them. Kyle restarts others as needed.

Additionally, a **headless CLI** is added to the existing `drem` binary as a Go subcommand, giving temp workers (and C-Suite agents) programmatic access to orchestrator operations without requiring TUI interaction.

## User Stories

1. As an orchestrator operator, I want to start a conversation with Kyle and have him brief me on the current state of the system, so that I can quickly understand what has happened since I last checked in.
2. As an orchestrator operator, I want Kyle to start the other C-Suite agents when needed, so that I don't have to manually manage each agent's lifecycle.
3. As an orchestrator operator, I want Kyle to write reports to disk summarizing decisions, status, and recommendations, so that I can review them asynchronously.
4. As an orchestrator operator, I want Kyle to delegate operational concerns to Mike and product concerns to Alex, so that each agent stays focused on their domain.
5. As an orchestrator operator, I want Mike to monitor the orchestrator's database for failure rates, stuck tasks, and agent deaths, so that operational issues are surfaced without me having to check manually.
6. As an orchestrator operator, I want Mike to spawn temporary operator agents to run the orchestrator and observe its behavior, so that the pipeline is exercised and bugs are discovered automatically.
7. As an orchestrator operator, I want Mike to work with Alex to determine next steps when operational issues are found, so that bugs become prioritized feature work rather than ad-hoc firefighting.
8. As an orchestrator operator, I want Mike to surface overarching operational patterns (not just individual failures), so that systemic issues are identified and addressed.
9. As an orchestrator operator, I want temp workers to write bug reports when they observe failures or unexpected behavior, so that issues are captured in the orchestrator's pipeline automatically.
10. As an orchestrator operator, I want temp workers to verify that tests pass after orchestrator changes, so that regressions are caught without my involvement.
11. As an orchestrator operator, I want temp workers to not make code changes themselves, so that all code modifications go through the orchestrator's normal planning and review pipeline.
12. As an orchestrator operator, I want only one temp worker running at a time initially, so that resource usage is predictable and debugging is simpler.
13. As an orchestrator operator, I want Alex to prioritize the backlog by reasoning about what causes the most pain and aligning with the roadmap, so that the most impactful work is done first.
14. As an orchestrator operator, I want Alex to review filed issues, bug reports, and PRDs from his inbox, so that product direction is informed by real operational data.
15. As an orchestrator operator, I want Alex to use the "grill me" skill with me to design new features, so that PRDs are thoroughly stress-tested before entering the pipeline.
16. As an orchestrator operator, I want Alex to be able to consult other C-Suite agents when designing features, so that operational and technical constraints inform product decisions.
17. As an orchestrator operator, I want Ross to monitor the context window usage of all agents (C-Suite and temp workers) via ctxmon, so that no agent silently degrades due to context overflow.
18. As an orchestrator operator, I want Ross to trigger a save-to-disk procedure when an agent's context reaches a configurable threshold, so that work in flight is preserved before a restart.
19. As an orchestrator operator, I want Ross to orchestrate agent restarts with restored context, so that agents resume seamlessly after hitting context limits.
20. As an orchestrator operator, I want Ross to manage the full lifecycle of temp workers (startup, monitoring, handoff, shutdown), so that Mike's context is not polluted with lifecycle cruft and Mike can focus on the big picture.
21. As an orchestrator operator, I want Ross to identify when a new C-Suite role is needed and onboard it, so that the team can grow to address emerging needs.
22. As an orchestrator operator, I want Seth to watch everything merged into master and verify adherence to the constitution and architecture decisions, so that technical quality is maintained continuously.
23. As an orchestrator operator, I want Seth to use the "repo-audit" skill for lightweight incremental checks rather than deep audits on every merge, so that quality verification does not become a bottleneck.
24. As an orchestrator operator, I want Seth to flag constitution or architecture violations and route them to Kyle or Alex for prioritization, so that technical debt is tracked and addressed.
25. As an orchestrator operator, I want all C-Suite agents to communicate via disk-based inboxes, so that coordination works reliably without inter-process messaging.
26. As an orchestrator operator, I want each agent to have a state file that captures their current context summary, so that restarts preserve continuity.
27. As an orchestrator operator, I want a headless CLI subcommand on the `drem` binary, so that temp workers and C-Suite agents can query and operate the orchestrator programmatically.
28. As an orchestrator operator, I want the headless CLI to support querying task status, listing agents, viewing failures, and filing tasks, so that the most common operations are available without TUI interaction.
29. As an orchestrator operator, I want C-Suite agents to use the existing memory system for persistence across restarts, so that no new persistence infrastructure is needed.
30. As an orchestrator operator, I want Kyle to be the entry point I interact with directly, so that I have a single point of contact rather than managing five agents individually.

## Implementation Decisions

### Disk Communication Protocol

All C-Suite and temp worker agents communicate via a shared directory structure:

```
~/.drem-csuite/
  ├── kyle/
  │   ├── inbox/          # Messages TO Kyle
  │   ├── outbox/         # Reports FROM Kyle
  │   └── state.md        # Kyle's current context summary
  ├── mike/
  │   ├── inbox/
  │   ├── outbox/
  │   └── state.md
  ├── alex/
  │   ├── inbox/
  │   ├── outbox/
  │   └── state.md
  ├── ross/
  │   ├── inbox/
  │   ├── outbox/
  │   └── state.md
  ├── seth/
  │   ├── inbox/
  │   ├── outbox/
  │   └── state.md
  └── temp-workers/
      ├── worker-001/
      │   ├── inbox/
      │   ├── outbox/
      │   └── state.md
      └── ...
```

Messages are markdown files with YAML frontmatter:

```yaml
---
from: mike
to: alex
timestamp: 2026-03-23T14:30:00Z
subject: "High failure rate in merge phase"
priority: high
type: observation | request | report | decision
---

Message body in markdown.
```

Agents poll their inbox periodically or on restart. Processed messages are moved to an `archive/` subdirectory within the inbox to prevent reprocessing. Agents write their outbox messages as new files with timestamp-based filenames for natural ordering.

### Agent Prompt and Personality System

Each C-Suite agent is a Claude Code session launched with a carefully crafted system prompt (stored in the repo under a prompts directory) that defines:

- **Role and responsibilities** — what this agent owns and does not own
- **Communication protocol** — how to read inbox, write to other agents' inboxes, update state file
- **Tools and skills available** — which Claude Code skills this agent should use (e.g., Alex uses "grill-me", Seth uses "repo-audit")
- **Decision boundaries** — what this agent can decide autonomously vs. what requires escalation to Kyle or the operator
- **Context management** — instructions for monitoring own context via ctxmon and cooperating with Ross on save/restart

### Context Lifecycle Manager

Ross monitors all agent context windows using ctxmon. The lifecycle is:

1. **Normal operation** — agent context below configurable warning threshold (e.g., 75%)
2. **Warning** — Ross notifies the agent to begin winding down current work
3. **Save threshold** (e.g., 85%) — Ross instructs the agent to save state:
   - Write a `state.md` summarizing current work in flight, pending decisions, and next actions
   - Flush any unsent messages to outboxes
   - Write a `restart-context.md` with instructions for the next session
4. **Restart** — Ross launches a new session for the agent with `restart-context.md` as initial context, plus inbox contents

The thresholds are configurable per agent role (C-Suite agents accumulate context differently than temp workers).

### Temp Worker Framework

Temp workers are spawned by Mike and lifecycle-managed by Ross:

- **Spawning**: Mike decides a temp worker is needed (e.g., to exercise a recently-fixed pipeline path). Mike writes a task brief to `~/.drem-csuite/temp-workers/worker-NNN/inbox/` and signals Ross to start the worker.
- **Execution**: The temp worker uses the headless CLI to operate the orchestrator: filing tasks, monitoring progress, observing failures. It writes bug reports to the orchestrator's drop directory and observation reports to its outbox.
- **Handoff**: When Ross detects context threshold, Ross triggers save-to-disk. If work remains, Ross spawns a new worker with the previous worker's state.
- **Completion**: When the task brief is satisfied, the temp worker writes a summary to its outbox and signals Ross for shutdown.
- **Constraint**: One temp worker at a time initially. This may be relaxed in the future.

### Headless Orchestrator CLI

A new `drem cli` (or `drem query`) subcommand is added to the existing Go binary, providing programmatic access to orchestrator operations:

**Read operations (minimum viable):**
- `drem cli tasks [--status=STATUS]` — list tasks, optionally filtered by status
- `drem cli task <id>` — show task details including subtasks, assigned agent, and comments
- `drem cli agents [--status=STATUS]` — list agents and their current state
- `drem cli failures [--since=DURATION]` — show recent task/agent failures with error context
- `drem cli stats` — operational summary (tasks by status, agent utilization, failure rate, throughput)

**Write operations (minimum viable):**
- `drem cli file-task --title=TITLE --description=DESC` — create a new task in CLASSIFYING status
- `drem cli comment <task-id> --body=BODY` — add a comment to a task

All commands output structured text (or JSON with `--json` flag) suitable for consumption by Claude Code agents. These subcommands read from and write to the same SQLite database the orchestrator uses, with appropriate locking.

### Kyle Bootstrap Sequence

When the operator starts a conversation with Kyle:

1. Kyle reads `~/.drem-csuite/kyle/state.md` to restore prior context (if exists)
2. Kyle checks which C-Suite agents are running (by checking for active tmux sessions or process markers)
3. Kyle reads his inbox for unprocessed messages from other agents
4. Kyle presents a status briefing to the operator: who's running, pending reports, key issues, recommendations
5. The operator directs Kyle to start specific agents or Kyle starts them based on current needs

### Integration with Existing Skills

- **Alex** uses `grill-me` when designing features with the operator and `write-a-prd` to produce PRDs
- **Seth** uses `repo-audit` for constitution and architecture checks after merges
- **Alex** uses `write-a-prd` and may use `prd-to-issues` to break PRDs into pipeline-ready work
- **Kyle** may use `wt-status` to understand worktree state when briefing the operator

### Agent Discovery and Health

Each running agent writes a heartbeat timestamp to its `state.md` file. Kyle (and Ross) can detect stale agents by checking heartbeat freshness. A stale heartbeat (e.g., no update in 5 minutes) indicates the agent has died or hung, and Ross can restart it.

## Testing Decisions

A good test for this feature verifies observable behavior through the module's public interface — message parsing, routing, state serialization, and CLI output — without mocking internal implementation details or requiring live Claude Code sessions.

### Modules to test

**Disk Communication Protocol**: Test message parsing (YAML frontmatter extraction, field validation), message routing (correct inbox targeting), archive behavior (processed messages move to archive), and filename generation (timestamp-based ordering). These are pure functions operating on file content and paths. Prior art: existing orchestrator tests that verify state transitions through database queries.

**Context Lifecycle Manager**: Test state serialization and deserialization (round-trip `state.md` and `restart-context.md` through write/read), threshold detection logic (given a context percentage, determine correct action), and the save-to-disk protocol (verify that all expected files are written). Prior art: existing context monitoring in `internal/agent/` that tracks context usage percentages.

**Headless CLI**: Integration tests that set up a test SQLite database with known task/agent state, run CLI subcommands, and verify output format and content. Test both text and JSON output modes. Test write operations (file-task, comment) by verifying database state after command execution. Prior art: existing orchestrator tests that use test databases with seeded data.

## Out of Scope

- **Modifications to the drem-orchestrator's core Go code** (beyond the headless CLI subcommand) — the C-Suite agents operate externally and interact with the orchestrator through its database and drop directory.
- **Inter-agent real-time messaging** — all communication is asynchronous via disk. No WebSocket, no shared memory, no message queue.
- **Automated C-Suite agent deployment** — the operator starts Kyle manually; Kyle starts others. There is no systemd service, no Docker container, no auto-start on boot.
- **Multi-machine distribution** — all agents run on the same machine, sharing the same filesystem.
- **TUI integration for C-Suite status** — the orchestrator's TUI does not display C-Suite agent status. C-Suite agents are invisible to the orchestrator.
- **Temp workers interacting with the TUI directly** — temp workers use the headless CLI exclusively. TUI observation capabilities are a future enhancement.
- **More than one temp worker at a time** — the system is designed for single-worker operation initially, though the architecture does not preclude scaling later.

## Further Notes

- The C-Suite agents are a **bootstrapping mechanism**. Their purpose is to make the orchestrator good enough that the operator can use it directly with minimal manual intervention. As the orchestrator improves, the C-Suite layer may become less necessary — or it may evolve into a permanent operational layer. This is intentionally left open.
- The headless CLI is valuable beyond the C-Suite use case. It provides scriptability and integration points that benefit any operator, and should be designed with that broader audience in mind.
- The disk communication protocol is intentionally simple (markdown files in directories). This makes it inspectable by humans, debuggable with standard tools (`ls`, `cat`), and compatible with any Claude Code session without special tooling. Resist the temptation to add complexity (databases, binary formats, locking protocols) until the simple approach demonstrably fails.
- Context management is the hardest operational challenge. C-Suite agents accumulate context through inbox reading, state restoration, and ongoing observation. The save/restart cycle must preserve enough context for continuity without carrying forward stale information. Ross's effectiveness at managing this cycle will be a key determinant of system reliability.
- The agent prompts are the most important deliverable in this PRD. The technical infrastructure (directories, CLI, message format) is straightforward. Getting the agent personalities, decision boundaries, and coordination patterns right is what will determine whether this team is effective or just noisy.
