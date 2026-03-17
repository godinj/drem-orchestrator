# Agent: TDD-Aware Coder Agent Prompts

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent orchestrator that manages Claude Code agents via tmux in git bare repos with worktrees.
Your task is to add phase-specific coder prompt variants: test-phase coders write failing tests (TDD), implementation-phase coders make pre-written tests pass.

## Context

Read these before starting:
- `docs/tdd-enforcement/prd-tdd-enforcement.md` (sections 4.8.1, 4.8.2, 4.8.3, 4.5.1)
- `internal/prompt/prompt.go` (full file — especially `Generate()`, `coderInstructions()`, `Opts` struct)
- `internal/model/models.go` (Task struct — `Phase` and `TestsFor` fields, `Context` JSONField)

Key facts:
- `coderInstructions(task)` currently returns generic coder instructions
- The Task model now has a `Phase` field: `"test"`, `"implementation"`, `"integration"`, or `""`
- For impl-phase subtasks, the test subtask's `Context["actual_test_files"]` contains the actual test file paths (populated by Agent 06 after test agents complete)
- `Opts.ParentCtx` already passes parent task context to the prompt

## Dependencies

This agent depends on Agents 03 (model with Phase field) and 05 (planner prompt rewrite). The `Phase` field on Task and the updated planner instructions must exist.

## Deliverables

### 1. Modify `internal/prompt/prompt.go`

**a) Update `coderInstructions` to dispatch by phase:**

```go
func coderInstructions(task *model.Task) []string {
    switch task.Phase {
    case "test":
        return testPhaseCoderInstructions(task)
    case "implementation":
        return implPhaseCoderInstructions(task)
    default:
        return defaultCoderInstructions(task)
    }
}
```

**b) Add `testPhaseCoderInstructions`** (§4.8.1):

```go
func testPhaseCoderInstructions(task *model.Task) []string
```

Returns instructions for writing tests BEFORE implementation (TDD):

```markdown
## Instructions

You are writing tests BEFORE implementation (TDD).

Your tests should:
1. Define the expected behavior described in the task
2. Be thorough — cover happy paths, edge cases, and error conditions
3. FAIL when run (the implementation doesn't exist yet)
4. Be clear about WHAT is being tested and WHY
5. Use the project's existing test patterns and frameworks

After writing tests:
1. Run the build command to verify test files compile
2. Run the tests — they SHOULD fail (that's expected for TDD)
3. Verify failures are for the RIGHT reason (missing implementation, not syntax errors)
4. Commit with message: "test: <what these tests verify>"
5. Do NOT push to remote
```

Also include estimated files from task context (same as current coder does).

**c) Add `implPhaseCoderInstructions`** (§4.8.3):

```go
func implPhaseCoderInstructions(task *model.Task) []string
```

Returns instructions for implementing code to pass pre-written tests:

```markdown
## Instructions

You are implementing code to pass pre-written tests (TDD).

Pre-written tests exist at: <list of test files>

Your implementation should:
1. Read the pre-written tests first to understand expected behavior
2. Implement the minimum code to make ALL tests pass
3. Do NOT modify the pre-written tests unless they have a genuine bug
4. If you believe a test is wrong, note it in your commit message but make it pass anyway

After implementation:
1. Run the build command to verify compilation
2. Run the FULL test suite — ALL tests must pass
3. If any test fails, fix your implementation (not the test)
4. NEVER modify pre-written TDD tests. Fix your code to match the tests.
5. Commit with message: "feat: <what was implemented>"
6. Do NOT push to remote
```

For the test file list, look in `task.Context["actual_test_files"]` first (populated by Agent 06's test file tracking after the test agent completes). If that's empty or nil, fall back to the estimated files from the plan. Format the list as a markdown bullet list of file paths:

```go
var testFiles []string
if task.Context != nil {
    if files, ok := task.Context["actual_test_files"]; ok {
        // Extract string slice from the interface
        if fileList, ok := files.([]any); ok {
            for _, f := range fileList {
                if s, ok := f.(string); ok {
                    testFiles = append(testFiles, s)
                }
            }
        }
    }
}
if len(testFiles) == 0 {
    // Fallback to estimated_files from context
    if files, ok := task.Context["estimated_files"]; ok {
        // ... similar extraction ...
    }
}
```

**d) Rename current `coderInstructions` body to `defaultCoderInstructions`:**

The existing generic coder instructions become the fallback for subtasks with no phase (backward compatibility) and integration-phase subtasks. Update the "after implementation" section to be stricter about tests:

```markdown
After implementation:
1. Run the build command to verify compilation
2. Run the FULL test suite — not just your new tests
3. ALL tests must pass. Do not commit if any test fails.
4. If a test fails:
   - If it's a test you wrote or modified: fix it
   - If it's a pre-existing test broken by your changes: fix your implementation, not the test
5. Commit your changes with a descriptive message
6. Do NOT push to remote
```

**e) Update `featureReviewerInstructions`** (§4.9.2):

Add test execution as the first step in the review process:

```markdown
## Review Process

1. Run the FULL test suite first and record results
2. Read the acceptance criteria from the task description carefully
3. Examine the code changes shown above
...
```

Update the review.json schema to include test results:

```json
{
    "test_results": {
        "passed": true,
        "total": 42,
        "failed": 0,
        "output_summary": "..."
    }
}
```

### 2. Add tests in `internal/prompt/prompt_test.go`

- **Test-phase coder prompt**: Task with `Phase: "test"` → prompt contains "writing tests BEFORE implementation", "SHOULD fail", "test:", does NOT contain "Run the FULL test suite"
- **Impl-phase coder prompt with actual_test_files**: Task with `Phase: "implementation"` and `Context["actual_test_files"]` → prompt contains the test file paths, "pre-written tests", "Do NOT modify the pre-written tests"
- **Impl-phase coder prompt without actual_test_files**: Falls back to estimated_files from context
- **Default coder prompt (no phase)**: Task with empty Phase → prompt contains generic coder instructions (backward compat)
- **Integration-phase coder prompt**: Task with `Phase: "integration"` → prompt contains "FULL test suite", "ALL tests must pass"
- **Feature reviewer includes test results**: Reviewer prompt contains "test_results", "Run the FULL test suite first"

## Scope Limitation

ONLY modify:
- `internal/prompt/prompt.go`
- `internal/prompt/prompt_test.go` (new or extend)

Do NOT modify: `internal/model/`, `internal/state/`, `internal/orchestrator/`, `internal/tui/`.

## Conventions

- Go 1.22+ with standard library
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./...`
