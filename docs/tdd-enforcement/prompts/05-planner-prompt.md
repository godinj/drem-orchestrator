# Agent: Planner & Reviewer Prompt Updates for TDD

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent orchestrator that manages Claude Code agents via tmux in git bare repos with worktrees.
Your task is to rewrite the planner agent instructions to enforce mandatory TDD and update the plan reviewer to assess TDD quality.

## Context

Read these before starting:
- `docs/tdd-enforcement/prd-tdd-enforcement.md` (sections 4.7, 4.9.1)
- `internal/prompt/prompt.go` (`plannerInstructions()`, `planReviewerInstructions()`, full file)

Key facts:
- `plannerInstructions()` currently returns a `[]string` of markdown lines
- `planReviewerInstructions()` generates the plan reviewer's evaluation criteria
- The current planner instructions have a soft "Test Subtasks" section that says tests should depend on impl subtasks — this is the OPPOSITE of TDD and must be replaced

## Deliverables

### 1. Modify `internal/prompt/prompt.go`

**a) Rewrite `plannerInstructions()`** — replace the entire return value with TDD-mandatory instructions from §4.7:

The new instructions must include:

1. **Mandatory TDD header**: "Every implementation subtask MUST have exactly ONE corresponding test subtask that runs FIRST."

2. **Test subtask requirements**:
   - Has `phase: "test"` and `tests_for: [<impl subtask index>]` (exactly one index)
   - Writes tests that define expected behavior BEFORE implementation
   - Tests should initially FAIL (they test unimplemented behavior)
   - Covers acceptance criteria relevant to that implementation subtask
   - Has `agent_type: "coder"`
   - Has NO dependencies on implementation subtasks

3. **Implementation subtask requirements**:
   - Has `phase: "implementation"`
   - Makes the pre-written tests pass
   - NEVER modifies pre-written tests
   - Note: does NOT need to add test subtask to its `dependencies` — auto-generated from `tests_for`

4. **TDD exceptions**: Must be declared in `tdd_exceptions` with subtask index and justification. List valid and invalid exceptions from §4.7.

5. **Ordering**: `test subtasks → HUMAN REVIEW → implementation subtasks → integration subtask`

6. **Example structure** showing the subtask numbering pattern:
   ```
   Subtask 0: "Write tests for X" (phase: test, tests_for: [1])
   Subtask 1: "Implement X" (phase: implementation)
     → auto-depends on subtask 0 via tests_for
   Subtask 2: "Write tests for Y" (phase: test, tests_for: [3])
   Subtask 3: "Implement Y" (phase: implementation)
     → auto-depends on subtask 2 via tests_for
   Subtask 4: "Integration wiring" (phase: integration, depends: [1, 3])
   ```

7. **Updated plan.json schema** showing the `phase`, `tests_for`, and `tdd_exceptions` fields in the JSON example.

Keep the existing sections that are still valid:
- Coverage Verification
- Integration Subtask (update to mention `phase: "integration"`)
- Decomposition Rules (DO/DON'T)
- File Overlap guidance

Remove or replace:
- The old "Test Subtasks" section (tests depending on impl subtasks — wrong direction)
- Any reference to `is_test` field (replaced by `phase: "test"`)

**b) Update `planReviewerInstructions()`** — add TDD assessment to the review criteria:

Add to the "Review Criteria" section:

```markdown
6. **TDD structure**: Does every implementation subtask have a corresponding test subtask with `tests_for`?
7. **Test quality**: Are test subtask descriptions specific about what behavior they verify?
8. **TDD exceptions**: Are exceptions justified? (integration wiring and research are valid; "too hard to test" is not)
```

Add to the review.json output schema:

```json
{
    "tdd_assessment": {
        "test_coverage_adequate": true,
        "exceptions_justified": true,
        "issues": ["Test subtask 0 only tests happy path, missing edge cases for..."]
    }
}
```

### 2. Add/update tests in `internal/prompt/prompt_test.go`

- **Planner prompt contains TDD instructions**: Generate a planner prompt, verify it contains "test subtask", "phase", "tests_for", "tdd_exceptions", "MUST have exactly ONE"
- **Planner prompt does NOT contain old test ordering**: Verify it does NOT contain "Depend on ALL implementation subtasks" or "is_test"
- **Plan reviewer includes TDD assessment**: Generate a plan reviewer prompt, verify it contains "tdd_assessment", "TDD structure", "exceptions_justified"

## Scope Limitation

ONLY modify:
- `internal/prompt/prompt.go` (`plannerInstructions`, `planReviewerInstructions` functions only)
- `internal/prompt/prompt_test.go` (new or extend)

Do NOT modify: `internal/model/`, `internal/state/`, `internal/orchestrator/`, `internal/tui/`.

## Conventions

- Go 1.22+ with standard library
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./...`
