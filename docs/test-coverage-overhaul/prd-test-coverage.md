# Test Coverage Overhaul — PRD

## Problem

The drem-orchestrator has a test-to-source ratio of 0.70 (9,761 test lines / 14,004 source lines), but coverage is concentrated in small, stable packages while the large, complex packages that would be targets for architectural refactoring are severely undertested.

| Package | LOC | Coverage | Risk |
|---------|-----|----------|------|
| internal/state | 230 | **93.3%** | Low |
| internal/worktree | 1,008 | **74.9%** | Low |
| internal/ctxmon | ~300 | **70.8%** | Low |
| internal/tmux | 507 | **60.1%** | Medium |
| internal/taskimport | 194 | **57.9%** | Low |
| internal/model | 170 | **56.2%** | Low |
| internal/prompt | 727 | **42.4%** | Medium |
| internal/orchestrator | ~5,400 | **38.2%** | **High** |
| internal/supervisor | ~500 | **37.3%** | Medium |
| internal/merge | 512 | **13.9%** | **High** |
| internal/tui | ~2,300 | **4.3%** | **High** |
| internal/agent | 674 | **4.0%** | **High** |
| internal/memory | 374 | **0.0%** | **High** |
| internal/db | 65 | **0.0%** | Low |

**5 packages with >1,000 combined LOC sit below 15% coverage.** These are the packages most likely to need refactoring and the least safe to refactor.

### Structural Issues

1. **Duplicated test helpers** — `setupBareRepo()`, `addWorktree()`, `commitFile()` are copy-pasted identically across worktree, merge, and orchestrator tests. `testDB()` has 3 divergent copies.
2. **No shared test utilities package** — each package reinvents the same setup.
3. **Limited mocking** — only one mock interface (`mockWorktreeClient` in merge). Most packages are tightly coupled to real git, tmux, and SQLite.
4. **No fuzz or benchmark tests.**

## Goal

- Shared `internal/testutil` package eliminating all duplicated test helpers
- Unit tests for all exported functions in `internal/memory` (currently 0%)
- Coverage improvements in `internal/agent`, `internal/tui`, `internal/merge`, and `internal/prompt`
- Key interfaces extracted to enable mocking without real tmux/git
- All tests pass, no regressions

## Current State

### Test Infrastructure

**Database helpers** (3 divergent copies):
- `model/models_test.go` — `testDB()` with `file::memory:` (no cache)
- `orchestrator/orchestrator_test.go` — `testDB()` with `file::memory:?cache=shared`
- `orchestrator/failure_recovery_test.go` — `newTestDB()` with UUID-isolated `file:{UUID}?mode=memory&cache=private`

**Git helpers** (2 identical copies):
- `worktree/worktree_test.go` — `setupBareRepo()`, `addWorktree()`, `commitFile()`
- `merge/merge_test.go` — `setupBareRepo()`, `addWorktree()`, `commitFile()` (identical)

**Other helpers** (no duplication):
- `agent/runner_test.go` — `requireTmux()`, `testSessionName()`
- `orchestrator/orchestrator_test.go` — `testOrchestrator()`, `initBareRepo()`, `runGitCmd()`

### Package Analysis

#### internal/memory (0% — 374 LOC)

**All functions are GORM-dependent and testable with in-memory SQLite:**

| Function | Signature | Testable? |
|----------|-----------|-----------|
| `NewManager` | `(db *gorm.DB)` | Constructor |
| `StoreMemory` | `(agentID, content, memType, taskID, metadata)` | In-mem SQLite |
| `GetMemories` | `(agentID, taskID, memType, limit)` | In-mem SQLite |
| `GetProjectMemories` | `(projectID, memTypes, limit)` | In-mem SQLite |
| `CompactAgentMemory` | `(agentID)` | In-mem SQLite |
| `BuildAgentContext` | `(agentID, taskID, maxTokens)` | In-mem SQLite |
| `ExtractMemoriesFromOutput` | `(agentID, taskID, output)` | Regex + store |

Private pure helpers that should be exported for testing:
- `titleCase(s string) string`
- `matchPatterns(line string, patterns, memType) *entry`

#### internal/agent (4% — 674 LOC)

**What's tested:** `verifySpawn` (alive/dead/completed), prompt file writes.

**What's NOT tested:**

| Function | Dependencies | Why Untested |
|----------|-------------|--------------|
| `SpawnAgent` | tmux, worktree, filesystem, DB | No mock interface for tmux |
| `SpawnAgentInWorktree` | tmux, filesystem, DB | No mock interface for tmux |
| `StopAgent` | tmux, DB | No mock interface |
| `GetAgentOutput` | tmux, DB | No mock interface |
| `CleanupStaleAgents` | tmux, DB | No mock interface |
| `ReapOrphanedSessions` | tmux, DB | No mock interface |
| `heartbeatLoop` | DB (periodic) | Background goroutine |
| `contextMonitorLoop` | Filesystem, DB | Background goroutine |

**Key blocker:** `tmux.Manager` is a concrete type. Extracting a `SessionManager` interface would unlock testing of the entire agent lifecycle without real tmux.

Private pure helpers that should be exported for testing:
- `truncateTitle(s string, maxLen int) string`
- `sanitizeSessionName(s string) string`
- `agentTypeLabel(at model.AgentType) string`

#### internal/tui (4.3% — ~2,300 LOC)

**What's tested:** `buildDisplayList()` (5 scenarios), `Selected()` (6 scenarios) — all in board.go.

**What's NOT tested:**

| Component | LOC | Testable Methods | Status |
|-----------|-----|-----------------|--------|
| `app.go` (Model) | 1,348 | `Update()` (20+ msg types), `View()` | All untested |
| `detail.go` (DetailModel) | ~300 | `deletableItems()`, `selectedDeleteItem()`, `isDeleteTarget()` | All untested |
| `agents.go` (AgentsModel) | ~100 | `visibleAgents()`, `isSubtaskID()`, `setTaskFilter()`, `clampAgentCursor()` | All untested |
| `board.go` (BoardModel) | 365 | `View()`, `adjustScroll()`, `Update()` | Partially tested |

Most sub-model methods are pure functions operating on in-memory state — testable without Bubble Tea framework overhead.

#### internal/merge (13.9% — 512 LOC)

**What's tested:** `MergeAgentIntoFeature` (1 integration test), `mergeWithRebaseAndRetry` (6 mock tests covering retry logic).

**What's NOT tested:**

| Function | Dependencies |
|----------|-------------|
| `PlanAgentMerge` | Git operations |
| `MergeAllAgentsIntoFeature` | DB + git |
| `MergeFeatureIntoMain` | DB + git + build verification |
| `SyncFeaturesAfterMerge` | Git operations |
| `GetMergeStatus` | DB + git |
| `detectBuildCommand` | Filesystem |
| `intersect` | Pure (no deps) |

Private pure helpers that should be exported for testing:
- `intersect(a, b []string) []string`
- `detectBuildCommand(path string) (string, []string)`

#### internal/prompt (42.4% — 727 LOC)

**What's tested:** Planner (6 tests), plan reviewer (2), coder (9), feature reviewer (1).

**What's NOT tested:**
- `researcherInstructions()` — researcher prompt generation
- `fixerInstructions()` — fixer prompt generation
- `defaultInstructions()` — fallback prompt
- `readBuildCommands()` — filesystem reading
- `Generate()` end-to-end composition
- Edge cases: empty task context, special characters, very long descriptions

All prompt code is pure (string building) — trivially testable.

## Test Plan

### Phase 0: Test Infrastructure — `internal/testutil`

**New package:** `internal/testutil/`

**File:** `testutil.go`

Consolidate all duplicated helpers:

```go
// Database helpers
func NewTestDB(t *testing.T) *gorm.DB           // UUID-isolated, cache=private
func NewSharedTestDB(t *testing.T) *gorm.DB      // cache=shared for multi-connection tests

// Git helpers (currently copy-pasted in worktree + merge)
func SetupBareRepo(t *testing.T) string
func AddWorktree(t *testing.T, bareRepo, branch, dir string) string
func CommitFile(t *testing.T, worktree, filename, content, message string)
func RunGitCmd(t *testing.T, dir string, args ...string) string
```

**Migration:** Update all existing test files to import from `testutil` and delete the local copies.

---

### Phase 1: Easy Wins — Pure Functions & DB-Only Packages

#### 1a. internal/memory tests (~200–250 LOC)

**File:** `memory_test.go`

All functions testable with in-memory SQLite via `testutil.NewTestDB()`.

**Test cases:**
1. `StoreMemory` — round-trip: store and retrieve by agentID
2. `StoreMemory` — with metadata JSON
3. `GetMemories` — filter by agentID, taskID, memoryType
4. `GetMemories` — limit parameter
5. `GetMemories` — empty result set
6. `GetProjectMemories` — cross-task retrieval with JOIN
7. `GetProjectMemories` — filter by memoryType slice
8. `CompactAgentMemory` — aggregates multiple memories into summary
9. `CompactAgentMemory` — idempotent on already-compacted
10. `BuildAgentContext` — token budget truncation
11. `BuildAgentContext` — empty memories returns empty string
12. `ExtractMemoriesFromOutput` — decision pattern matching
13. `ExtractMemoriesFromOutput` — blocker pattern matching
14. `ExtractMemoriesFromOutput` — file change pattern matching
15. `ExtractMemoriesFromOutput` — no matches returns nothing

#### 1b. internal/prompt missing tests (~100–120 LOC)

**File:** `prompt_test.go` (append to existing)

**Test cases:**
1. `researcherInstructions` — contains expected guidance phrases
2. `fixerInstructions` — contains fix-specific directives
3. `defaultInstructions` — fallback behavior
4. `readBuildCommands` — reads go.mod → returns `go build`
5. `readBuildCommands` — reads Makefile → returns `make`
6. `readBuildCommands` — no build files → returns empty
7. `Generate` — end-to-end with planner type
8. `Generate` — end-to-end with coder type

#### 1c. internal/merge pure helpers (~40–60 LOC)

**File:** `merge_test.go` (append to existing)

**Test cases:**
1. `intersect` — overlapping slices
2. `intersect` — no overlap
3. `intersect` — empty input
4. `detectBuildCommand` — go.mod present → `go build ./...`
5. `detectBuildCommand` — package.json present → `npm test`
6. `detectBuildCommand` — Makefile present → `make`
7. `detectBuildCommand` — nothing present → empty

#### 1d. internal/agent pure helpers (~60–80 LOC)

**File:** `runner_test.go` (append to existing)

Export and test private helpers:
1. `TruncateTitle` — within limit → unchanged
2. `TruncateTitle` — exceeds limit → truncated with "..."
3. `TruncateTitle` — empty string
4. `SanitizeSessionName` — strips invalid chars
5. `SanitizeSessionName` — preserves valid chars
6. `AgentTypeLabel` — all enum values produce non-empty strings
7. `CanSpawn` — under limit → true
8. `CanSpawn` — at limit → false
9. `GetRunningAgents` — empty → empty slice
10. `DrainCompletions` — empty channel → empty slice

---

### Phase 2: Interface Extraction & Mock-Based Testing

#### 2a. Extract `SessionManager` interface from tmux

**File:** `internal/agent/session.go` (new, ~30 LOC)

```go
// SessionManager abstracts tmux session operations for testing.
type SessionManager interface {
    CreateAgentSession(name, workDir string, cmd []string) error
    IsAgentSessionAlive(name string) (bool, error)
    KillAgentSession(name string) error
    CaptureAgentPane(name string, lines int) (string, error)
    ListAgentSessions(prefix string) ([]string, error)
}
```

`tmux.Manager` already implements these methods — just needs an interface wrapper.

#### 2b. internal/agent mock-based tests (~250–300 LOC)

**File:** `runner_mock_test.go` (new)

With `SessionManager` interface, test:
1. `SpawnAgentInWorktree` — happy path with mock session
2. `SpawnAgentInWorktree` — session creation failure
3. `StopAgent` — kills session, updates DB
4. `StopAgent` — agent not found
5. `GetAgentOutput` — captures pane output
6. `GetAgentOutput` — agent not running
7. `CleanupStaleAgents` — finds and kills stale sessions
8. `CleanupStaleAgents` — no stale agents
9. `ReapOrphanedSessions` — kills sessions with no DB record
10. `ReapOrphanedSessions` — no orphans

#### 2c. internal/tui sub-model tests (~200–250 LOC)

**File:** `detail_test.go` (new)

**Test cases for DetailModel:**
1. `deletableItems` — task with comments and plan steps
2. `deletableItems` — task with no deletable items
3. `selectedDeleteItem` — cursor at valid index
4. `selectedDeleteItem` — cursor out of bounds
5. `isDeleteTarget` — various item types
6. `isDeleteSection` — section headers vs items

**File:** `agents_test.go` (new)

**Test cases for AgentsModel:**
1. `visibleAgents` — no filter → all agents
2. `visibleAgents` — task filter → only matching agents
3. `isSubtaskID` — subtask of selected task → true
4. `isSubtaskID` — unrelated task → false
5. `setTaskFilter` — sets filter, resets cursor
6. `clampAgentCursor` — within bounds → unchanged
7. `clampAgentCursor` — beyond bounds → clamped

---

### Phase 3: Integration Testing for Merge & Orchestrator

#### 3a. internal/merge integration tests (~200–250 LOC)

**File:** `merge_test.go` (append to existing)

Uses `testutil.SetupBareRepo()` and real git:
1. `PlanAgentMerge` — clean: reports no conflicts
2. `PlanAgentMerge` — conflicting: identifies conflicting files
3. `MergeAllAgentsIntoFeature` — two agents merged sequentially
4. `MergeAllAgentsIntoFeature` — one agent fails, others still merge
5. `MergeFeatureIntoMain` — clean merge into main
6. `MergeFeatureIntoMain` — build verification failure → rollback
7. `SyncFeaturesAfterMerge` — rebases other features
8. `GetMergeStatus` — reports merge-ready vs conflict

#### 3b. internal/orchestrator state transition tests (~300–400 LOC)

**File:** `lifecycle_test.go` (new)

End-to-end task lifecycle tests with mock agent runner:
1. Task BACKLOG → PLANNING (agent spawned)
2. Task PLANNING → PLAN_REVIEW (plan parsed from output)
3. Task PLAN_REVIEW → approved → TEST_WRITING (TDD flow)
4. Task PLAN_REVIEW → rejected → PLANNING (re-plan)
5. Task TEST_WRITING → TEST_REVIEW (tests written)
6. Task TEST_REVIEW → approved → IN_PROGRESS
7. Task TEST_REVIEW → rejected → TEST_WRITING (rewrite)
8. Task IN_PROGRESS → MERGING (all subtasks complete)
9. Task MERGING → TESTING_READY (merged successfully)
10. Task TESTING_READY → DONE (tests pass)
11. Task IN_PROGRESS → PAUSED → IN_PROGRESS (pause/resume)
12. `DeletePlanStep` — removes step from plan JSON
13. `SpawnReviewerSession` — spawns reviewer agent
14. `SpawnFixerSession` — spawns fixer agent

## Implementation Order

```
Phase 0: Test Infrastructure (~100 LOC)
  └── internal/testutil/testutil.go + migrate existing tests

Phase 1: Easy Wins (~400-510 LOC, no refactoring needed)
  ├── 1a. memory/memory_test.go (15 tests)
  ├── 1b. prompt/prompt_test.go additions (8 tests)
  ├── 1c. merge/merge_test.go additions (7 tests)
  └── 1d. agent/runner_test.go additions (10 tests)

Phase 2: Interface Extraction + Mocks (~480-580 LOC)
  ├── 2a. agent/session.go interface
  ├── 2b. agent/runner_mock_test.go (10 tests)
  └── 2c. tui/detail_test.go + agents_test.go (13 tests)

Phase 3: Integration Tests (~500-650 LOC)
  ├── 3a. merge/merge_test.go additions (8 tests)
  └── 3b. orchestrator/lifecycle_test.go (14 tests)
```

Each phase is independently shippable. Phase 0 should land first since all subsequent phases use `testutil`.

## Expected Coverage After

| Package | Before | After (est.) |
|---------|--------|-------------|
| internal/memory | 0% | **~75%** |
| internal/agent | 4% | **~45%** |
| internal/tui | 4.3% | **~25%** |
| internal/merge | 13.9% | **~55%** |
| internal/prompt | 42.4% | **~65%** |
| internal/orchestrator | 38.2% | **~50%** |

## Success Criteria

- `internal/testutil` package exists; zero duplicated test helpers remain
- `internal/memory` has tests covering all exported functions
- `SessionManager` interface extracted; agent lifecycle testable without tmux
- All TUI sub-model pure functions have unit tests
- Merge pipeline has integration tests for all public functions
- Orchestrator has lifecycle tests covering major state transitions
- All existing tests still pass
- `go test ./...` passes clean
- Total new test code: ~1,480–1,840 lines across 4 phases
