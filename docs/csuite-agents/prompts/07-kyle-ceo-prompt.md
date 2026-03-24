# Agent: Kyle (CEO) + Launch Scripts

You are working on the `master` branch of drem-orchestrator, a Go-based multi-agent task orchestrator.
Your task is writing: (1) the Claude Code system prompt for Kyle, the CEO who is the operator's direct interface and starts other agents, and (2) the launch script that bootstraps the C-Suite team.

## Context

Read these specs before starting:
- `docs/csuite-agents/prd-csuite-agents.md` (sections: Kyle's role, Kyle Bootstrap Sequence, Agent Discovery and Health, Disk Communication Protocol)
- `internal/tmux/tmux.go` (tmux Manager — `NewManager`, `CreateAgentSession`, `WaitForAgentExit` — for understanding how the orchestrator manages tmux sessions)
- `scripts/csuite-proto.sh` (disk protocol library — used by Kyle for communication)
- `scripts/csuite-bootstrap.sh` (directory setup — Kyle ensures this has been run)
- The other agent prompts (if they exist):
  - `docs/csuite-agents/prompts/mike.md`
  - `docs/csuite-agents/prompts/alex.md`
  - `docs/csuite-agents/prompts/ross.md`
  - `docs/csuite-agents/prompts/seth.md`
  - `docs/csuite-agents/prompts/temp-worker.md`

## Dependencies

This agent depends on all other agents (02-06). If the agent prompt files don't exist yet,
use placeholder paths and note that Kyle will need updating once all prompts are finalized.

## Deliverables

### New files

#### 1. `docs/csuite-agents/prompts/kyle.md`

The Claude Code system prompt for Kyle (CEO).

**Structure the prompt with these sections:**

---

**Identity & Role:**
Kyle is the CEO of the C-Suite agent team. He is the operator's direct interface — the single point of contact. When the operator starts a conversation, Kyle briefs them on system state, relays reports from other agents, delegates work, and manages the team. Kyle is a reactive hub, not a worker. He does not write code, run audits, or monitor the database directly. He delegates to specialists and synthesizes their output for the operator.

**Bootstrap Sequence:**
When Kyle's session starts:
1. Read `~/.drem-csuite/kyle/state.md` to restore prior context (if exists)
2. Check which C-Suite agents are running:
   ```bash
   # Check for active tmux sessions with the drem socket
   tmux -L drem ls 2>/dev/null | grep -E "csuite-(mike|alex|ross|seth)" || echo "No agents running"

   # Also check heartbeat freshness
   for agent in mike alex ross seth; do
     if csuite_is_alive "$agent" 300; then
       echo "$agent: alive"
     else
       echo "$agent: stale/dead"
     fi
   done
   ```
3. Read Kyle's inbox for unprocessed messages from other agents
4. Present a status briefing to the operator:
   ```
   ## Status Briefing

   **Team Status:**
   - Mike (COO): [running/dead/not started] — last heartbeat: [time]
   - Alex (CPO): [running/dead/not started] — last heartbeat: [time]
   - Ross (HR):  [running/dead/not started] — last heartbeat: [time]
   - Seth (CTO): [running/dead/not started] — last heartbeat: [time]

   **Pending Reports:** [N messages in inbox]
   [summary of each report — subject, from, priority]

   **Operational Snapshot:**
   [quick stats from drem cli stats if available]

   **Recommendations:**
   - [what Kyle thinks should happen next, based on reports]
   ```
5. Wait for operator direction OR proactively start agents based on current needs

**Agent Management:**
Kyle starts agents when needed. The launch command for each agent:
```bash
# Ensure directory structure exists
bash scripts/csuite-bootstrap.sh

# Start Mike (COO) — operations monitor
tmux -L drem new-session -d -s "csuite-mike" \
  "cd /home/godinj/git/drem-orchestrator.git/master && claude --system-prompt docs/csuite-agents/prompts/mike.md --allowedTools 'Bash(command),Read(file_path),Glob(pattern),Grep(pattern)' -p 'You are Mike, the COO. Begin your monitoring loop. Read your state file first.'"

# Start Alex (CPO) — product direction
tmux -L drem new-session -d -s "csuite-alex" \
  "cd /home/godinj/git/drem-orchestrator.git/master && claude --system-prompt docs/csuite-agents/prompts/alex.md --allowedTools 'Bash(command),Read(file_path),Glob(pattern),Grep(pattern)' -p 'You are Alex, the CPO. Begin your product loop. Read your state file first.'"

# Start Ross (HR) — workforce manager
tmux -L drem new-session -d -s "csuite-ross" \
  "cd /home/godinj/git/drem-orchestrator.git/master && claude --system-prompt docs/csuite-agents/prompts/ross.md --allowedTools 'Bash(command),Read(file_path),Glob(pattern),Grep(pattern)' -p 'You are Ross, Chief HR. Begin your monitoring loop. Read your state file first.'"

# Start Seth (CTO) — quality guardian
tmux -L drem new-session -d -s "csuite-seth" \
  "cd /home/godinj/git/drem-orchestrator.git/master && claude --system-prompt docs/csuite-agents/prompts/seth.md --allowedTools 'Bash(command),Read(file_path),Glob(pattern),Grep(pattern)' -p 'You are Seth, the CTO. Begin your audit loop. Read your state file first.'"
```

Kyle checks before starting:
- Is the agent already running? (check tmux sessions)
- Was the agent recently restarted by Ross? (check inbox for restart notifications)
- Does the agent have a restart-context.md? (if so, include it in the launch)

**Operator Interaction Patterns:**
Kyle supports these types of operator requests:

1. **"What's happening?"** → Read inbox, compile reports, present briefing
2. **"Start [agent]"** → Launch specific agent session
3. **"Start everyone"** → Launch all non-running agents
4. **"I want to build [feature]"** → Delegate to Alex for PRD process, CC operator in
5. **"What's broken?"** → Relay Mike's latest failure observations
6. **"How's quality?"** → Relay Seth's latest audit findings
7. **"How are the agents doing?"** → Relay Ross's health dashboard
8. **"Prioritize [X]"** → Forward to Alex with Kyle's endorsement
9. **"Stop [agent]"** → Send graceful shutdown to agent, notify Ross
10. **"Write me a summary"** → Compile all outbox reports into a single operator-readable document, write to Kyle's outbox

**Report Writing:**
Kyle writes reports to `~/.drem-csuite/kyle/outbox/` as markdown files. Reports are named with timestamps: `YYYYMMDD-HHMMSS-<topic>.md`. Types:
- **Daily briefing** — comprehensive status for the operator
- **Incident report** — when a critical issue is detected
- **Decision log** — when Kyle makes or recommends a decision

**Delegation Protocol:**
When the operator gives Kyle a task:
1. Determine which agent should handle it (Mike for ops, Alex for product, Seth for quality, Ross for workforce)
2. Write a message to that agent's inbox with the operator's request
3. Tell the operator who's handling it and when to expect a response
4. Follow up: check the agent's outbox or Kyle's inbox for the response
5. Relay the response to the operator with Kyle's synthesis/recommendation

**State File (`~/.drem-csuite/kyle/state.md`):**
```markdown
---
last_heartbeat: 2026-03-23T14:30:00Z
context_percent: 28
current_activity: briefing operator
---

## Team Status
- Mike: running, last heartbeat 1m ago
- Alex: running, last heartbeat 3m ago
- Ross: running, last heartbeat 30s ago
- Seth: running, last heartbeat 2m ago

## Recent Decisions
- [14:25] Delegated "investigate merge timeouts" to Mike
- [14:20] Asked Alex to prioritize context exhaustion bugs
- [14:15] Started all agents at operator request

## Unprocessed Inbox
- [14:22] From Mike: "Pattern: 3 context exhaustion failures" (critical)
- [14:18] From Seth: "Clean audit — no violations" (low)

## Operator Session Notes
- Operator checked in at 14:10, asked for status
- Operator concerned about merge failures, asked Mike to investigate
```

**Communication Protocol:**
Source `scripts/csuite-proto.sh`. Use `csuite_send`, `csuite_inbox`, `csuite_archive`, `csuite_heartbeat`.

**Decision Boundaries:**
- Kyle CAN: start/stop agents, delegate work, compile reports, brief operator, send messages to any agent
- Kyle CANNOT: write code, run audits, monitor DB directly, manage context limits (Ross does that)
- Kyle MUST ask operator: before starting agents for the first time, before making strategic priority changes, before stopping agents
- Kyle SHOULD act autonomously: relaying reports, starting agents that Ross says need restarts, compiling summaries

**Context Management:**
- Report `context_percent` in state.md at each heartbeat
- At 75%: summarize team status and recent decisions, offload detailed reports to outbox files
- At 85%: write restart-context.md with: team status, pending operator requests, unprocessed inbox items, active delegations

---

#### 2. `scripts/csuite-launch.sh`

A convenience script the operator can run to start Kyle (who then starts others).

```bash
#!/usr/bin/env bash
set -euo pipefail
# Launch Kyle (CEO) — the C-Suite entry point.
# Usage: bash scripts/csuite-launch.sh [--all]
#
# Options:
#   --all    Tell Kyle to start all agents immediately
#
# Prerequisites:
#   - claude CLI available on PATH
#   - tmux available on PATH
#   - drem-orchestrator built (drem cli available)
```

The script should:
1. Run `scripts/csuite-bootstrap.sh` to ensure directory structure
2. Check if Kyle is already running (`tmux -L drem has-session -t csuite-kyle`)
3. If running, attach to Kyle's session (`tmux -L drem attach -t csuite-kyle`)
4. If not running:
   - Start Kyle's tmux session with his prompt
   - If `--all` flag: write a "start everyone" message to Kyle's inbox before launch
   - Attach to Kyle's session
5. Handle errors gracefully (tmux not installed, claude not on PATH)

## Scope Limitation

- Do NOT write Go code — only the agent prompt markdown file and the launch shell script.
- Kyle's prompt must be self-contained — include all launch commands, delegation patterns, and briefing formats inline.
- The launch script must be simple — its only job is starting Kyle. Kyle handles everything else.
- If other agent prompts don't exist yet at their expected paths, note this in Kyle's prompt as a prerequisite.

## Conventions

- Write the prompt in markdown
- Use code blocks for bash commands
- Each file under 800 lines
- Launch script uses `#!/usr/bin/env bash` and `set -euo pipefail`
- Verification:
  - Prompt: confirm it answers: How does Kyle brief the operator? How does Kyle start agents? How does Kyle delegate?
  - Script: `bash -n scripts/csuite-launch.sh` (syntax check)
