# Agent: Improve Orchestrator Package Test Coverage

> Historical prompt; do not execute against the current state machine. Use
> `docs/test-coverage/prompts/02-reconciliation.md` for the Phase 4
> recovery-only reconciliation contract.

You are working on the `master` branch of drem-orchestrator, a Go-based task orchestrator.
Your task is to increase test coverage of `internal/orchestrator/` from 60.1% to at least 68%.

## Context

Read these before starting:
- `internal/orchestrator/` — all `.go` files (both source and existing tests)
- `internal/testutil/testutil.go` (shared helpers — `NewTestDB`, `SetupBareRepo`, `AddWorktree`, `CommitFile`, `CreateProject`, `CreateTask`, `CreateAgent`, `NewMockSupervisor`)
- `internal/model/` (task/agent models)
- `internal/state/` (state transition rules)

The orchestrator is the largest package (7004 LOC, 11 source files). Focus on the highest-impact untested paths.

## Deliverables

### Add tests for untested orchestrator logic

Identify untested functions first:
```bash
go test -coverprofile=coverage.out ./internal/orchestrator/
go tool cover -func=coverage.out | grep -v '100.0%' | sort -t: -k3 -n
```

Focus on the largest coverage gaps. Common categories:

#### 1. Task scheduling decisions

- `scheduleSubtasks` — test with different dependency graphs
- `findCurrentGroup` — test group selection logic
- Wave grouping with file overlap detection

#### 2. State processing branches

- `processBacklog` — test dependency checking, agent spawning
- `processPlanning` — test plan parsing, clarification trigger
- `checkFeatureCompletion` — all subtasks done, partial completion, empty features

#### 3. Reconciliation edge cases

- `reconcileStaleSubtasks` — test with feature branches that have/don't have changes
- `reconcileOrphanedSubtasks` — test merge success and failure paths
- `reconcileStuckAgents` — test with/without commits, retry limits

#### 4. Error paths

- Database errors during state transitions
- Merge failures
- Supervisor timeout/error handling

### Conventions

- Use `testutil.NewTestDB(t)` for ALL database creation
- Use `testutil.SetupBareRepo(t)` for git repo setup
- Use `testutil.NewMockSupervisor(t, response)` for supervisor-dependent tests
- Use table-driven tests for parameterized cases
- Use `t.TempDir()` for filesystem operations
- Do NOT define local test helper functions — use `testutil/` helpers

### Scope Limitation

- Only add/modify test files in `internal/orchestrator/`
- Do NOT modify source files
- Do NOT duplicate test infrastructure that exists in `testutil/`
- If a test requires complex setup, it's OK to test a smaller unit in isolation

## Verification

```bash
# Coverage must be >= 68%:
go test -cover ./internal/orchestrator/...

# All tests must pass:
go test ./internal/orchestrator/...

# No regressions:
go test ./...

# Constitution check still passes:
bash scripts/check_constitution.sh
```
