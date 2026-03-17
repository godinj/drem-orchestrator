# Agent: Agent Package Pure Helper Tests

You are working on the `master` branch of drem-orchestrator, a Go TUI tool that orchestrates Claude Code agents via tmux.
Your task is to export and test the private pure helper functions in `internal/agent`, and add tests for the already-public pure methods, raising coverage from 4% toward ~15%.

## Context

Read these before starting:
- `CLAUDE.md` (build commands, conventions)
- `docs/test-coverage-overhaul/prd-test-coverage.md` (Phase 1d section)
- `internal/agent/runner.go` (source — 674 LOC)
- `internal/agent/runner_test.go` (existing tests — 5 test functions for verifySpawn and prompt writes)

The following private functions in `runner.go` are pure (no external dependencies) and should be exported:
- `truncateTitle(s string, maxLen int) string` — truncates string with "..." suffix
- `sanitizeSessionName(s string) string` — strips chars invalid for tmux session names
- `agentTypeLabel(at model.AgentType) string` — maps agent type enum to display label

The following public methods are pure and currently untested:
- `CanSpawn() bool` — checks if under maxConcurrent limit
- `GetRunningAgents() []RunningAgent` — returns copy of running agents map
- `DrainCompletions() []Completion` — drains completion channel
- `GetContextUsage(agentID uuid.UUID) *ctxmon.Usage` — looks up context usage from running map

## Deliverables

### Modified files

#### 1. `internal/agent/runner.go`

Export the three private helpers by capitalizing them:
- `truncateTitle` → `TruncateTitle`
- `sanitizeSessionName` → `SanitizeSessionName`
- `agentTypeLabel` → `AgentTypeLabel`

Update all call sites within `runner.go` to use the new capitalized names.

Add doc comments:

```go
// TruncateTitle truncates s to maxLen characters, appending "..." if truncated.
func TruncateTitle(s string, maxLen int) string

// SanitizeSessionName removes characters not valid in tmux session names.
func SanitizeSessionName(s string) string

// AgentTypeLabel returns a human-readable label for the given agent type.
func AgentTypeLabel(at model.AgentType) string
```

#### 2. `internal/agent/runner_test.go` (append ~80–100 LOC)

Add test functions for the exported helpers and pure public methods.

```go
func TestTruncateTitle(t *testing.T)
```
Table-driven:
- Short string (under limit) → returned unchanged
- Exact length → returned unchanged
- Over limit → truncated with "..." appended
- Empty string → empty string
- maxLen of 0 → returns "..." or empty (match actual behavior)

```go
func TestSanitizeSessionName(t *testing.T)
```
Table-driven:
- Valid name (alphanumeric + hyphens) → unchanged
- Name with dots → dots replaced/removed (check actual implementation)
- Name with spaces → spaces replaced/removed
- Name with special chars (`@#$%`) → stripped
- Empty string → empty string

```go
func TestAgentTypeLabel(t *testing.T)
```
Table-driven — test every `model.AgentType` constant:
- `model.AgentTypePlanner` → non-empty string
- `model.AgentTypeCoder` → non-empty string
- `model.AgentTypeReviewer` → non-empty string
- All other agent types defined in `internal/model/enums.go` → non-empty strings
- Read the actual enum values from `model` package before writing tests

```go
func TestCanSpawn(t *testing.T)
```
- Create runner with maxConcurrent=2 and 0 running → returns true
- Simulate running agents at limit → returns false
- Note: may need to manipulate the `running` map or `semaphore` channel. If the struct fields are private and not settable, test indirectly or skip this test.

```go
func TestGetRunningAgents_Empty(t *testing.T)
```
- Create a fresh runner → `GetRunningAgents()` returns empty slice (not nil)

```go
func TestDrainCompletions_Empty(t *testing.T)
```
- Create a fresh runner → `DrainCompletions()` returns empty slice

## Scope Limitation

- Only modify files in `internal/agent/`
- Only export the three listed functions — do NOT change function logic
- Do NOT require tmux for any new tests (no `requireTmux()` guard)
- Do NOT modify or duplicate existing test functions

## Verification

```bash
go build ./...
go test ./internal/agent/ -v -cover
```

All existing and new tests must pass. Coverage should increase from 4%.

## Conventions

- `gofmt` for formatting
- Exported functions have doc comments
- Table-driven tests with `t.Run(tc.name, ...)`
- `t.Helper()` on test helpers
