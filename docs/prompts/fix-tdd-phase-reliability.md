# Fix TDD Phase: All Tests First, Then Implementation

## Goal

Enforce a clean TDD cycle where ALL test subtasks run in parallel, produce tests + compilable stubs, get human-reviewed together, and only then does implementation begin. The human reviews the full test plan (tests + API surface) before any feature code is written.

Desired lifecycle:
```
PLAN_REVIEW → TEST_WRITING (all test subtasks in parallel)
            → TEST_REVIEW  (human reviews all tests + stubs together)
            → IN_PROGRESS  (implementation subtasks fill in stubs)
            → TESTING_READY
```

## Changes Required

### 1. Schedule all test subtasks in parallel during TEST_WRITING

**File**: `internal/orchestrator/orchestrator.go` — `processTestWriting()` and `scheduleSubtasks()`

**Problem**: The wave schedule is built from ALL subtasks using file-overlap graph coloring. Test subtasks that both touch shared files (e.g. `CMakeLists.txt`) end up in different wave groups, so they run sequentially even though test additions are trivially mergeable.

**Fix**: During TEST_WRITING, ignore the wave schedule and schedule all test-phase subtasks whose explicit `dependencies` are met. Do not use the file-overlap groups for gating.

Rationale: Test subtasks are additive — they create new test files and append entries to build files. File-overlap conflicts between test subtasks are rare and trivial (both add lines to CMakeLists.txt). The existing merge retry logic handles the occasional conflict. Serializing test subtasks doubles the wall-clock time of the TEST_WRITING phase for no practical benefit.

In `scheduleSubtasks()` (or a test-writing-specific variant), when the parent is in TEST_WRITING:
- Filter subtasks to `Phase == "test"` (already done)
- Check `DependenciesMet()` for each (already done)
- **Skip** the `findCurrentGroup()` / wave-group gate — schedule all eligible test subtasks at once
- Respect agent capacity limits as usual

### 2. Require test agents to produce compilable stubs alongside tests

**File**: `internal/prompt/prompt.go` — `testPhaseCoderInstructions()`

**Problem**: Test-phase agents currently receive no instruction about stub generation. They write tests that may or may not compile depending on whether implementation exists. The human reviewer at TEST_REVIEW can't verify the test plan without seeing the API surface those tests exercise.

**Fix**: Update `testPhaseCoderInstructions()` to instruct test agents to:

1. Write comprehensive test files covering the behavior described in the task
2. Create **minimal stub headers and source files** so that the tests **compile but fail at runtime**
   - Headers with class/struct declarations, method signatures, correct includes
   - Source files with method bodies that return default values, empty containers, or `false` — enough to link but not pass tests
   - Only stub the public API surface that the tests exercise — do not implement any real logic
3. Verify that `cmake --build` (or equivalent build command) succeeds — tests must compile
4. Run the tests — they SHOULD fail (expected for TDD). Verify failures are assertion failures, not compilation or linker errors
5. Commit both test files AND stub files in a single commit

Add something like this to the test-phase prompt:

```
## Stub Requirements

Your tests must compile and link. To achieve this, create minimal stub implementations
alongside your tests:

- **Headers**: Full class/struct declarations with method signatures matching what your
  tests call. Use correct includes and namespaces.
- **Source files**: Method bodies that compile and link but do NOT implement real logic.
  Return default values (0, false, "", empty containers). The goal is that tests fail
  on ASSERTIONS, not on compilation or linker errors.
- **Only stub what tests need**: Don't stub internal helpers or implementation details.
  The stubs define the public API contract that the implementation must fulfill.

The human reviewer will examine your tests AND stubs together to approve the API surface
before implementation begins. Your stubs ARE the interface specification.
```

### 3. Add C++ test file detection

**File**: `internal/orchestrator/orchestrator.go` — `isTestFile()` function

**Problem**: `isTestFile()` only recognizes Go, Python, and JS/TS test file patterns. C++ test files (e.g. `tests/unit/model/LaneVersionTest.cpp`) are not detected, so `extractTestFiles()` returns empty results and implementation agents don't get told which test files to read.

**Fix**: Add C++ patterns to `isTestFile()`:
- `*Test.cpp`, `*Tests.cpp`, `*_test.cpp`, `*_tests.cpp`
- Files under `tests/` or `test/` directories
- `*Test.h`, `*Tests.h` (test helpers)

### 4. Auto-commit orchestrator artifacts before merge

**File**: `internal/merge/merge.go` (or equivalent merge path)

**Problem**: The orchestrator writes `plan.json` to the integration worktree during plan approval without committing it. Supervisor sessions drop `.claude/` artifacts. The merge guard rejects merges into dirty worktrees, even when the uncommitted files don't conflict.

**Fix**: Before attempting a merge into the integration worktree:
1. Check `git status --porcelain` for uncommitted changes
2. If present, auto-commit with message `"chore: commit orchestrator artifacts before merge"`
3. Stage only: tracked modified files + `plan.json` + `.claude/` directory contents
4. Do NOT stage unknown/unexpected files — log a warning for those but don't block the merge

Also fix the root cause: when `HandlePlanApproved()` writes `plan.json` to the integration worktree, commit it immediately as part of that operation.

### 5. Re-evaluate TEST_WRITING after manual supervisor fixes

**File**: `internal/orchestrator/orchestrator.go` — `processTestWriting()`

**Problem**: When a subtask fails during TEST_WRITING and a supervisor manually fixes it (merges the work, sets subtask to `done`), `processTestWriting()` doesn't detect the change. The parent stays stuck — no subtasks to schedule, but not all done either.

**Fix**: Ensure `processTestWriting()` runs its completion check on every tick unconditionally:
1. Query all test-phase subtask statuses
2. If all are `done` → transition to `TEST_REVIEW`
3. If some are `backlog` with deps met → schedule them
4. Clear any `needs_human_review` or blocking flags that were set by prior failure handling — the supervisor's fix should unstick the pipeline

The completion check should not be gated by `baseline_tests_failed` or similar flags. Those flags should only block *scheduling new subtasks*, not *checking if existing subtasks finished*.

## Testing

Add/update tests in `internal/orchestrator/test_writing_test.go`:

1. **Test**: All test-phase subtasks are scheduled in parallel during TEST_WRITING regardless of file overlap
2. **Test**: Test-phase subtasks with explicit `dependencies` are still respected (only file-overlap gating is removed)
3. **Test**: After supervisor fixes a failed test subtask (sets to done), processTestWriting transitions to TEST_REVIEW on next tick
4. **Test**: Full lifecycle: TEST_WRITING → all tests done → TEST_REVIEW → approved → IN_PROGRESS → only impl subtasks scheduled
5. **Test**: C++ test files are recognized by isTestFile()
6. **Test**: Merge into integration worktree succeeds when plan.json is uncommitted

## Files to modify

| File | Change |
|------|--------|
| `internal/orchestrator/orchestrator.go` | `processTestWriting()`: remove wave-group gating for test subtasks, fix completion re-evaluation |
| `internal/orchestrator/orchestrator.go` | `isTestFile()`: add C++ patterns |
| `internal/orchestrator/orchestrator.go` | `HandlePlanApproved()`: commit plan.json after writing |
| `internal/prompt/prompt.go` | `testPhaseCoderInstructions()`: add stub generation requirements |
| `internal/merge/merge.go` | Auto-commit dirty worktree before merge |
| `internal/orchestrator/test_writing_test.go` | New test cases per above |
