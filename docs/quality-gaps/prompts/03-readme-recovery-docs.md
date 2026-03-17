# Agent: README Documentation for Recovery, Reconciliation, and Troubleshooting

You are working on the `master` branch of drem-orchestrator, a Go TUI tool that orchestrates Claude Code agents via tmux.
Your task is to add three new sections to `README.md`: Self-Healing & Recovery, State Reconciliation, and Troubleshooting.

## Context

Read these before starting:
- `README.md` (current documentation — you will insert new sections)
- `internal/orchestrator/reconcile.go` (the full reconciliation system — 6 fix categories in `Reconcile()`)
- `internal/orchestrator/agent_results.go` (lines 1-25 — `MaxEmptyWorkRetries` constant and `processAgentResult` entry point)
- `internal/orchestrator/task_processing.go` (lines 525-654 — `executeMerge()` with fixer spawning on merge conflicts)
- `internal/merge/merge.go` (lines 142-167 — `MergeAgentIntoFeature` auto-commit of dirty worktrees)
- `internal/supervisor/supervisor.go` (line 38 — `--effort low` for supervisor calls)
- `internal/agent/process.go` (agent process spawning with `--effort medium`)
- `scripts/check_constitution.sh` (the constitution check wrapper)

## Deliverables

### Modified file

#### 1. `README.md`

Insert three new sections. Place them after the existing "Supervisor" section (line 252) and before the "Memory & Context" section (line 254). The content must be factually accurate based on the source code you read.

**Section 1: Self-Healing & Recovery**

Document the 5 automatic recovery patterns. For each, explain:
- What problem it solves (one sentence)
- How it works (one sentence)
- Where users see the effect (log output or TUI state change)

The 5 patterns are:
1. **Dirty worktree auto-commit** — orchestrator artifacts (.claude/, journals/) are auto-committed before rebase/merge to prevent dirty-worktree errors
2. **Stale agent unlinking** — `RetryTask()` unlinks dead agents and clears failure context keys so the task can be properly rescheduled
3. **Stuck agent recovery** — dead agent sessions are detected and their subtasks auto-retried (up to `MaxEmptyWorkRetries = 2` attempts)
4. **Already-complete fast-track** — when supervisor diagnoses "already_complete", the task is fast-tracked to DONE instead of wasting retries
5. **Merge conflict fixer** — when merge analysis recommends "spawn_agent", a fixer agent is automatically spawned to resolve conflicts

Include the retry limit: `MaxEmptyWorkRetries = 2`.

**Section 2: State Reconciliation**

Document the 6 reconciliation categories. For each, explain what state is detected and what fix is applied:

1. **Stale subtasks** — DONE subtasks whose feature branch has no changes → reset to BACKLOG
2. **Orphaned subtasks** — IN_PROGRESS subtasks with dead agents → merge remaining work or fail
3. **Empty features** — TESTING_READY tasks with zero file changes → marked FAILED
4. **Orphan worktrees** — agent worktrees with no commits and no active agent → removed
5. **Stuck agents** — subtasks with dead sessions → auto-retry with limits, then fail
6. **Already-merged features** — FAILED tasks whose feature branch is already ancestor of default → transition to DONE

Note that reconciliation runs automatically every tick (default 5s) and can be observed in the log file.

**Section 3: Troubleshooting**

A practical troubleshooting section with these subsections:

1. **Running the constitution check** — `bash scripts/check_constitution.sh` — what the 9 checks are and what passing/failing means
2. **Inspecting the database** — `sqlite3 drem.db` with useful queries:
   - `.schema` for table structure
   - `SELECT id, title, status FROM tasks;` for task overview
   - `SELECT id, agent_type, status FROM agents WHERE status = 'working';` for active agents
3. **Debugging a failed merge** — check the log file for merge errors, look at task.Context for supervisor diagnosis, check feature worktree for conflict markers
4. **Cleaning up after a failed run** — `C` key in TUI to clean dead sessions, manually remove orphan worktrees with `git worktree remove`
5. **Understanding supervisor verdicts** — explain the verdict types (approve, reject, retry, escalate) and where they appear (task.Context, detail panel)
6. **Effort levels** — supervisor uses `--effort low` (fast classification), agents use `--effort medium` (balanced speed/quality); not configurable via drem.toml

## Scope Limitation

- Do NOT modify any existing content in README.md
- Do NOT add sections beyond the three described above
- Do NOT modify any source code files
- Keep each section concise — this is reference documentation, not a tutorial

## Verification

After editing, verify the README is valid markdown:
```bash
# Check that the file is well-formed and no existing sections were removed
head -5 README.md
grep -c "^## " README.md  # Should be previous count + 3
```

## Conventions

- Match the heading style of existing README sections (## for top-level, ### for subsections)
- Use code blocks for commands and queries
- Use tables for structured information where appropriate
- Keep descriptions factual and concise — reference source file paths for users who want details
