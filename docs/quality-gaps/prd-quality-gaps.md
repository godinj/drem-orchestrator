# Quality Gaps — PRD

## Problem

The drem-orchestrator has two categories of gaps identified by the constitution audit:

1. **Test coverage holes** — 3 items remain from the test-coverage-overhaul PRD
2. **Human documentation gaps** — 5 recent features shipped without README updates

---

## Test Coverage Gaps

These are the remaining items from `docs/test-coverage-overhaul/prd-test-coverage.md` that have NOT been completed.

### Gap T1: Merge Pure Helper Unit Tests (Phase 1c — NOT DONE)

**Package:** `internal/merge`
**Current coverage:** 31.5%
**File to modify:** `internal/merge/merge_test.go`

Two private helper functions have zero tests:

1. `intersect(a, b []string) []string` — returns the intersection of two string slices
2. `detectBuildCommand(path string) (string, []string)` — detects go.mod/Makefile/package.json and returns the appropriate build command

**Required tests:**
1. `intersect` — overlapping slices
2. `intersect` — no overlap
3. `intersect` — empty input
4. `detectBuildCommand` — go.mod present → `go build ./...`
5. `detectBuildCommand` — package.json present → `npm test`
6. `detectBuildCommand` — Makefile present → `make`
7. `detectBuildCommand` — nothing present → empty

### Gap T2: Prompt Helper Unit Tests (Phase 1b — PARTIAL)

**Package:** `internal/prompt`
**Current coverage:** 73.5%
**File to modify:** `internal/prompt/prompt_test.go`

Existing tests cover `plannerInstructions()` only. Missing tests for:

1. `researcherInstructions()` — contains expected research guidance phrases
2. `fixerInstructions()` — contains fix-specific directives
3. `defaultInstructions()` — fallback behavior
4. `readBuildCommands()` — reads go.mod → returns `go build`
5. `readBuildCommands()` — reads Makefile → returns `make`
6. `readBuildCommands()` — no build files → returns empty
7. `Generate()` — end-to-end composition with planner type
8. `Generate()` — end-to-end composition with coder type

### Gap T3: Merge Integration Tests (Phase 3a — INCOMPLETE)

**Package:** `internal/merge`
**Current coverage:** 31.5%
**File to modify:** `internal/merge/merge_test.go`
**Depends on:** `internal/testutil` (already complete)

Only 2 of 8 required integration tests exist (`MergeAgentIntoFeature` clean + conflict). Missing:

1. `PlanAgentMerge` — clean: reports no conflicts
2. `PlanAgentMerge` — conflicting: identifies conflicting files
3. `MergeAllAgentsIntoFeature` — two agents merged sequentially
4. `MergeAllAgentsIntoFeature` — one agent fails, others still merge
5. `MergeFeatureIntoMain` — clean merge into main
6. `SyncFeaturesAfterMerge` — rebases other features
7. `GetMergeStatus` — reports merge-ready vs conflict

---

## Human Documentation Gaps

These features exist in code but lack user-facing documentation in `README.md`.

### Gap D1: Automatic Recovery System

**Commit:** d82d0ca
**Code:** `internal/orchestrator/agent_results.go`, `internal/orchestrator/reconcile.go`, `internal/orchestrator/task_processing.go`, `internal/merge/merge.go`

The system automatically detects and recovers from 5 recurring patterns that previously required manual intervention:

1. **Rebase/Merge Cleanup** — Auto-commits `.claude/` artifacts before rebase; auto-commits dirty main worktree before merge
2. **Stale Agent Links** — `RetryTask()` unlinks stale agents and clears failure context keys
3. **Stuck Agent Sessions** — `reconcileStuckAgents()` auto-retries dead agent subtasks with retry limits
4. **Already Complete Work** — `onAgentEmptyWork()` fast-tracks to DONE when supervisor diagnoses "already_complete"
5. **Merge Conflict Resolution** — `executeMerge()` spawns fixer agent on "spawn_agent" strategy

**What to document:** Add a "Self-Healing & Recovery" section to README explaining each pattern, what triggers it, and the retry limits (`MaxEmptyWorkRetries = 2`).

### Gap D2: Reconciliation System

**Code:** `internal/orchestrator/reconcile.go` (618 lines)

Audits the entire project state and fixes 6 categories of inconsistencies:

1. **Stale Subtasks** — DONE subtasks whose parent is IN_PROGRESS but feature branch has no changes → reset to BACKLOG
2. **Orphaned Subtasks** — IN_PROGRESS subtasks with dead agents → merge remaining work or fail
3. **Empty Features** — TESTING_READY tasks with zero file changes → fail
4. **Orphan Worktrees** — Agent worktrees with no commits and no WORKING agent → removed
5. **Stuck Agents** — IN_PROGRESS subtasks with dead tmux sessions → auto-retry or fail
6. **Already-Merged Features** — FAILED tasks whose feature branch is ancestor of default → transition to DONE

**What to document:** Add a "State Reconciliation" section explaining when it runs (every tick), what it fixes, and how to interpret its log output.

### Gap D3: Context-Aware Help Overlay Enhancement

**Code:** `internal/tui/help.go` (300 lines)

README lists `?` as "Toggle context-aware help overlay" in keybindings but doesn't explain how context detection works. Users should know:

- Help content changes based on: current panel, selected task status, delete mode, agent attachment
- This replaces the old static legend that showed all keybindings at once
- Press `?` or `esc` to dismiss

**What to document:** Expand the keybindings section with a brief explanation of progressive disclosure.

### Gap D4: Supervisor Effort Levels

**Code:** `internal/supervisor/supervisor.go` (line 38), `internal/agent/process.go`

Two hardcoded effort levels control Claude Code token budgets:

- **`low`** — supervisor calls (failure diagnosis, merge conflict analysis)
- **`medium`** — agent work (planner, coder, researcher, reviewer, fixer)

**What to document:** Add a note in the Configuration or Supervisor section explaining the effort level strategy and its impact on cost/speed.

### Gap D5: Operational Documentation

No troubleshooting or operational guide exists. Users need:

1. How to run `scripts/check_constitution.sh` and interpret output
2. How to debug a failed merge
3. How to inspect the database (`sqlite3 drem.db`)
4. How to clean up after a failed run
5. How to interpret supervisor verdicts
6. Common error messages and what they mean

**What to document:** Add a "Troubleshooting" section to README.

---

## Implementation Plan

### Agent Organization

```
Tier 1 (parallel — independent work)
├── Agent A: Gap T1 — merge helper unit tests
├── Agent B: Gap T2 — prompt helper unit tests
└── Agent C: Gaps D1-D5 — README documentation updates

Tier 2 (after Tier 1 merges)
└── Agent D: Gap T3 — merge integration tests (depends on T1)
```

### Verification

```bash
# After test agents
go test ./internal/merge/ -v
go test ./internal/prompt/ -v
go test ./... -cover

# After docs agent
# Manual review of README.md additions
bash scripts/check_constitution.sh
```

### Success Criteria

- `internal/merge` coverage rises from 31.5% to ~55%
- `internal/prompt` coverage rises from 73.5% to ~85%
- README.md has sections for: Self-Healing & Recovery, State Reconciliation, Troubleshooting
- All existing tests still pass
- Constitution check still passes 9/9
