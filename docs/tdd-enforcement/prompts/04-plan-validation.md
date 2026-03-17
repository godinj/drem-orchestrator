# Agent: Plan Validation for TDD Enforcement

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent orchestrator that manages Claude Code agents via tmux in git bare repos with worktrees.
Your task is to extend plan validation to enforce TDD structure: 1:1 test-to-implementation mapping, phase ordering, `tests_for` reverse dependencies, and TDD exception rules.

## Context

Read these before starting:
- `docs/tdd-enforcement/prd-tdd-enforcement.md` (sections 4.3.1 through 4.3.5)
- `internal/orchestrator/plan_validation.go` (current `ValidatePlan`, `planEntry` usage, helpers)
- `internal/orchestrator/orchestrator.go` — search for the `planEntry` struct definition (around line 2949) to see the fields including the new `Phase`, `TestsFor` fields

Key facts:
- `planEntry` has been extended with `Phase string` and `TestsFor []int` fields (by Agent 03)
- A new `tddException` struct exists with `SubtaskIndex int` and `Reason string`
- `ValidatePlan` currently takes `[]planEntry` and returns `PlanValidationResult`
- The function should also accept TDD exceptions for validation

## Deliverables

### 1. Modify `internal/orchestrator/plan_validation.go`

**a) Update `ValidatePlan` signature** to accept TDD exceptions:

```go
func ValidatePlan(subtasks []planEntry, exceptions []tddException) PlanValidationResult
```

The `tddException` struct is defined in `orchestrator.go` (by Agent 03). Import it or reference it directly since it's in the same package.

**b) Add the following validation rules after the existing checks (cycle detection, file overlap, etc.):**

#### Rule: Require 1:1 test-to-implementation mapping (§4.3.1)

```
ERROR if: No subtasks have phase "test" AND no tdd_exceptions cover all implementation subtasks.
ERROR if: An implementation subtask has no corresponding test subtask and no tdd_exception.
ERROR if: A test subtask's tests_for references more than one implementation subtask (enforce 1:1).
ERROR if: Two test subtasks reference the same implementation subtask (no duplicates).
ERROR if: A test subtask's tests_for references a subtask index that doesn't exist (out-of-bounds).
ERROR if: A test subtask's tests_for references a subtask that is not phase "implementation".
```

Implementation approach:
1. Build a map `implCoverage: map[int]int` where key is impl subtask index, value is covering test subtask index
2. For each test-phase subtask, validate its `tests_for` entries
3. For each implementation-phase subtask, check that it's covered by exactly one test or has a TDD exception

Only apply these rules if at least one subtask has a non-empty `Phase` field. If all phases are empty (old-format plan), skip TDD validation entirely for backward compatibility.

#### Rule: Validate phase ordering (§4.3.3)

```
ERROR if: A "test" phase subtask depends on an "implementation" phase subtask.
ERROR if: An "implementation" phase subtask has no corresponding test subtask and no tdd_exception.
WARNING if: A test subtask's tests_for file coverage doesn't overlap with the impl subtask's files.
```

For the file overlap warning: compare the test subtask's `allFiles()` with the impl subtask's `allFiles()`. If there's zero overlap, emit a warning (suggests the test is in the wrong place).

#### Rule: Validate TDD exceptions (§4.3.4)

```
WARNING if: More than 50% of implementation subtasks are exempted.
ERROR if: A tdd_exception references a subtask index that has phase "test".
ERROR if: A tdd_exception references a subtask index that is out of bounds.
```

#### Rule: Generate reverse dependencies from `tests_for` (§4.3.2)

Add a new exported function that computes the merged dependency graph:

```go
// MergeTDDDependencies takes the parsed plan entries and returns a new slice
// where each implementation subtask's Dependencies includes its corresponding
// test subtask (auto-generated from tests_for). Explicit dependencies are
// preserved; auto-generated ones are added only if not already present.
func MergeTDDDependencies(subtasks []planEntry) []planEntry
```

This function:
1. For each test-phase subtask with `TestsFor: [I]`, add the test subtask's index to `subtasks[I].Dependencies` if not already there
2. Returns a copy (don't mutate the input)

**c) Update all callers of `ValidatePlan`** to pass the exceptions parameter. Search for `ValidatePlan(` in the codebase. If there's only one call site, update it. If the caller doesn't have exceptions available, pass `nil`.

**d) Update `isTestSubtask`** to prefer the `Phase` field:

```go
func isTestSubtask(entry planEntry) bool {
    if entry.Phase == "test" {
        return true
    }
    if entry.IsTest {
        return true
    }
    // ... existing keyword fallback ...
}
```

**e) Replace `findMissingTestDependencies`** — the existing function checks that test subtasks depend on impl subtasks, which is the opposite of TDD ordering. With the new flow, tests run FIRST. Remove or replace this function with one that validates the new ordering. The existing check #5 in `ValidatePlan` that uses it should be replaced with the phase-ordering validation above.

### 2. Add tests in `internal/orchestrator/plan_validation_test.go`

Table-driven tests for:

- **No test subtasks, no exceptions**: All impl subtasks → error "no test subtasks"
- **Valid 1:1 mapping**: 2 test subtasks each with `tests_for` pointing to 1 impl subtask → valid
- **test_for references 2 impl subtasks**: Error "test subtask must reference exactly one"
- **Two tests reference same impl**: Error "duplicate test coverage"
- **tests_for out of bounds**: Error "out of bounds"
- **tests_for references non-implementation subtask**: Error "references non-implementation"
- **Test depends on impl subtask**: Error "test must not depend on implementation"
- **More than 50% exceptions**: Warning about excessive exceptions
- **Exception references test-phase subtask**: Error
- **Exception references out-of-bounds**: Error
- **Old-format plan (no phases)**: All existing validations pass, TDD checks skipped
- **MergeTDDDependencies**: Test subtask with `tests_for: [1]` → impl subtask 1 gains dependency on test subtask
- **MergeTDDDependencies with existing dependency**: No duplicate added
- **File overlap warning**: Test subtask files don't overlap with impl subtask files → warning

## Scope Limitation

ONLY modify:
- `internal/orchestrator/plan_validation.go`
- `internal/orchestrator/plan_validation_test.go` (new or extend)

Do NOT modify: `internal/model/`, `internal/state/`, `internal/prompt/`, `internal/tui/`, `internal/orchestrator/orchestrator.go`.

## Conventions

- Go 1.22+ with standard library
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./...`
