# PRD: Test-Driven Development Enforcement

**Date**: 2026-03-11
**Status**: Draft
**Scope**: `internal/orchestrator/`, `internal/prompt/`, `internal/state/`, `internal/agent/`, `internal/merge/`

---

## 1. Problem Statement

The orchestrator currently treats tests as optional. Agents receive soft instructions like "run tests if applicable," the planner _may_ include test subtasks, and the only quality gate is a manual human check at `TESTING_READY` — after all implementation is done. This means:

1. **Quality feedback comes too late.** The human reviews a fully-built feature and discovers that tests are missing, wrong, or flaky. Fixing this requires replanning or manual intervention.
2. **No automated test verification.** The orchestrator never checks whether tests actually pass. Coder agents can merge branches with failing tests.
3. **Test coverage is inconsistent.** Whether a plan includes test subtasks depends on the planner's mood. There is no structural enforcement.
4. **Flaky tests propagate.** Without a gate, tests that intermittently fail get merged and compound into integration failures.

The goal is to shift the human quality gate **left**: verify the quality of tests _before_ implementation begins (true TDD), and enforce automated test passage at every merge point.

---

## 2. Goals

1. **Test-first subtask ordering** — Test subtasks are written, merged, and human-reviewed before implementation subtasks begin.
2. **Mandatory test subtasks** — Every plan must include test subtasks. If TDD is genuinely impossible for a subtask, the planner must explicitly justify the exception.
3. **Automated test gates** — Each coder agent must pass tests before its branch can merge. The orchestrator verifies test passage, not just compilation.
4. **No flaky tests** — Test failures are real failures. No retrying around flaky tests; fix them.
5. **Context-aware failure recovery** — Agents retry on test failure up to 85% context window, then escalate to a fixer agent, then to a human.
6. **Enforced for all tasks** — This is the default behavior for every task in every project, including drem-orchestrator itself.

## 3. Non-Goals

- Specifying _which_ test framework or runner to use (that's project-specific via CLAUDE.md)
- Changing the TUI dashboard layout (beyond surfacing new states)
- Modifying the merge pipeline itself (covered by the merge reliability PRD)
- Adding code coverage metrics or thresholds (future work)

---

## 4. Design

### 4.1 New Task Lifecycle: Test-First Flow

The current flow is:

```
PLANNING → PLAN_REVIEW → IN_PROGRESS (all subtasks) → TESTING_READY (human gate) → MERGING → DONE
```

The new flow introduces a test-writing phase with a human review gate before implementation:

```
PLANNING → PLAN_REVIEW → TEST_WRITING → TEST_REVIEW (human gate) → IN_PROGRESS → TESTING_READY → MERGING → DONE
```

#### 4.1.1 New States

| State | Description |
|-------|-------------|
| `TEST_WRITING` | Test subtasks are being executed by coder agents. Implementation subtasks remain in backlog. |
| `TEST_REVIEW` | All test subtasks have merged. Human reviews test quality before greenlighting implementation. |

#### 4.1.2 Updated State Machine

```
BACKLOG       → {PLANNING, PAUSED}
PLANNING      → {PLAN_REVIEW, FAILED, PAUSED}
PLAN_REVIEW   → {TEST_WRITING, PLANNING}           # changed: was → IN_PROGRESS
TEST_WRITING  → {TEST_REVIEW, FAILED, PAUSED}      # new
TEST_REVIEW   → {IN_PROGRESS, TEST_WRITING}         # new: human approves or rejects tests
IN_PROGRESS   → {TESTING_READY, FAILED, PAUSED}
TESTING_READY → {MERGING, IN_PROGRESS, PLANNING}
MERGING       → {DONE, FAILED}
PAUSED        → {BACKLOG, PLANNING, IN_PROGRESS, TEST_WRITING}
DONE          → {}
FAILED        → {BACKLOG, IN_PROGRESS}
```

The human gate moves from `TESTING_READY` (post-implementation) to `TEST_REVIEW` (post-test-writing, pre-implementation). The `TESTING_READY` state becomes an automated gate (run full suite, verify passage) rather than a manual one.

#### 4.1.3 Subtask Dependency Graph Change

Currently, the planner produces:

```
[impl-1, impl-2, impl-3] → [test] → [integration]
```

The new required ordering is:

```
[test-1, test-2, ...] → HUMAN REVIEW → [impl-1, impl-2, impl-3] → [integration]
```

Test subtasks:
- Run first, against the feature branch
- Each test subtask corresponds to exactly one implementation subtask (strict 1:1)
- Tests are expected to _fail_ at this point (they test unimplemented behavior)
- The human reviews test quality, coverage, and intent — not passage

Implementation subtasks:
- Depend on their corresponding test subtask(s)
- Are only scheduled after the human approves the tests at `TEST_REVIEW`
- Must pass all tests (including the pre-written ones) before their branch can merge

---

### 4.2 Plan Structure Changes

#### 4.2.1 Plan JSON Schema Extension

Each subtask gains a `phase` field and test subtasks gain a `tests_for` field.
The `tests_for` field auto-generates a reverse dependency — the implementation
subtask does not need an explicit `dependencies` entry for its test subtask
(see §4.3.2):

```json
{
  "subtasks": [
    {
      "title": "Write tests for track automation rendering",
      "description": "...",
      "files": ["internal/track/automation_test.go"],
      "phase": "test",
      "tests_for": [1],
      "agent_type": "coder",
      "dependencies": []
    },
    {
      "title": "Implement track automation rendering",
      "description": "...",
      "files": ["internal/track/automation.go", "internal/track/renderer.go"],
      "phase": "implementation",
      "agent_type": "coder",
      "dependencies": []
    },
    {
      "title": "Integration wiring",
      "description": "...",
      "files": ["internal/track/registry.go"],
      "phase": "integration",
      "agent_type": "coder",
      "dependencies": [1]
    }
  ],
  "tdd_exceptions": [
    {
      "subtask_index": 2,
      "reason": "Integration wiring connects existing tested components — no new behavior to test in isolation"
    }
  ]
}
```

#### 4.2.2 Phase Definitions

| Phase | Scheduling | Human Gate | Test Requirement |
|-------|-----------|------------|-----------------|
| `test` | Scheduled during `TEST_WRITING`. Runs before any implementation. | Reviewed at `TEST_REVIEW`. | N/A — these _are_ the tests. |
| `implementation` | Scheduled during `IN_PROGRESS`, after `TEST_REVIEW` approval. | None (automated test gate at merge). | Must pass all tests before merge. |
| `integration` | Scheduled last during `IN_PROGRESS`, after all impl subtasks merge. | `TESTING_READY` automated check. | Must pass full test suite. |

#### 4.2.3 TDD Exceptions

Some subtasks genuinely cannot be test-first:
- Pure integration/wiring subtasks that connect already-tested components
- Research subtasks that produce documentation, not code
- Infrastructure subtasks (CI config, build scripts) where the "test" is the build itself

The planner must explicitly declare these in `tdd_exceptions` with a justification. The human reviewer sees these at `PLAN_REVIEW` and can reject the plan if the exceptions are unjustified.

---

### 4.3 Plan Validation Changes

Extend `ValidatePlan()` in `internal/orchestrator/plan_validation.go`:

#### 4.3.1 Require 1:1 Test-to-Implementation Mapping

Every implementation subtask must have exactly one corresponding test subtask.
This strict 1:1 mapping protects against subtle regressions — each test subtask
is scoped tightly to its implementation, making it clear what broke and when.

```
ERROR if: No subtasks have phase "test" AND no tdd_exceptions cover all implementation subtasks.
ERROR if: An implementation subtask has no corresponding test subtask and no tdd_exception.
ERROR if: A test subtask's tests_for references more than one implementation subtask (enforce 1:1).
ERROR if: Two test subtasks reference the same implementation subtask (no duplicates).
```

#### 4.3.2 `tests_for` Implies Reverse Dependency

The `tests_for` field on a test subtask is the **source of truth** for the
test-to-implementation relationship. The orchestrator auto-generates the
reverse dependency: if test subtask T has `tests_for: [I]`, then
implementation subtask I automatically depends on T. The planner does NOT
need to declare this dependency explicitly in the `dependencies` field of the
implementation subtask — if it does, it's treated as redundant (not an error).

This means:
- Planners declare `tests_for` on test subtasks and `dependencies` for
  inter-implementation ordering only
- The orchestrator's dependency resolver merges auto-generated TDD
  dependencies with explicit `dependencies` before scheduling

#### 4.3.3 Validate Phase Ordering

```
ERROR if: A "test" phase subtask depends on an "implementation" phase subtask (tests must come first).
ERROR if: An "implementation" phase subtask has no corresponding test subtask (via tests_for) and no tdd_exception.
WARNING if: A test subtask's tests_for references don't cover all files in the corresponding impl subtask.
```

#### 4.3.4 Validate TDD Exceptions

```
WARNING if: More than 50% of implementation subtasks are exempted (suggests the planner is avoiding TDD).
ERROR if: A tdd_exception references a subtask that has phase "test" (nonsensical).
```

#### 4.3.5 Test Gate Applicability

The automated test gate at merge time applies **only to implementation and
integration phase subtasks**. Test-phase subtasks are expected to produce
intentionally-failing tests (the implementation doesn't exist yet), so they
skip the merge-time test verification. The gate for test-phase subtasks is
limited to: tests compile, and failures are for the right reasons (missing
implementation, not syntax errors).

---

### 4.4 Orchestrator Flow Changes

#### 4.4.1 `PLAN_REVIEW → TEST_WRITING` Transition

When the plan reviewer approves, instead of transitioning to `IN_PROGRESS`:

```go
// After plan review approval:
// 1. Identify all phase:"test" subtasks
// 2. Transition parent to TEST_WRITING
// 3. Schedule only test-phase subtasks (impl subtasks stay in BACKLOG)
```

#### 4.4.2 `TEST_WRITING` Processing

During the main tick loop, when the parent is in `TEST_WRITING`:

```go
func (o *Orchestrator) processTestWriting(parent *model.Task) {
    testSubtasks := o.getSubtasksByPhase(parent.ID, "test")

    // Schedule unstarted test subtasks (respecting file overlap / wave scheduling)
    o.scheduleSubtasks(parent, testSubtasks)

    // Check if all test subtasks are done
    if o.allSubtasksDone(testSubtasks) {
        // Transition to TEST_REVIEW for human approval
        state.TransitionTask(parent, model.StatusTestReview, "orchestrator",
            map[string]any{"reason": "all test subtasks completed"})
    }
}
```

#### 4.4.3 `TEST_REVIEW` Human Gate

At `TEST_REVIEW`, the human reviews:

1. **Test quality** — Are the tests testing the right things? Do they cover the acceptance criteria?
2. **Test intent** — Do the tests describe the expected behavior clearly?
3. **TDD exceptions** — Are the exceptions justified?

The human can:
- **Approve** → Transition to `IN_PROGRESS`, schedule implementation subtasks
- **Reject with feedback** → Transition back to `TEST_WRITING` with the
  following behavior:
  1. The rejected test subtasks are marked `REJECTED` (a terminal state for
     that subtask instance — it is NOT re-run)
  2. New test subtasks are created as replacements, cloned from the rejected
     ones but with the human's feedback appended to the description
  3. The new test agents work on top of the integration branch, which already
     contains the merged (but rejected) test code — they can see and revise it
  4. The `tests_for` references on the new test subtasks point to the same
     implementation subtasks as the originals
  5. When all replacement test subtasks complete, the parent transitions back
     to `TEST_REVIEW` for another round of human review

#### 4.4.4 `IN_PROGRESS` Changes

During `IN_PROGRESS`, only `implementation` and `integration` phase subtasks are scheduled. The existing wave-based scheduling (from the merge reliability PRD) applies.

#### 4.4.5 `TESTING_READY` Automation

`TESTING_READY` becomes an automated gate instead of a manual one:

```go
func (o *Orchestrator) processTestingReady(parent *model.Task) {
    // Spawn a reviewer agent on the integration branch
    // The reviewer runs the full test suite and reports results
    // If tests pass → transition to MERGING
    // If tests fail → spawn fixer agent (see §4.6)
}
```

---

### 4.5 Per-Agent Test Gate at Merge Time

The merge-time test gate applies to **implementation and integration phase
subtasks only**. Test-phase subtasks skip this gate (their tests are expected
to fail — see §4.3.5).

#### 4.5.1 Test Execution in Agent Prompt

Update coder instructions to make test execution mandatory, not optional.
The rule is simple: **always fix implementation, not tests.** If there is a
genuine problem with a test, it will surface when the agent hits the context
window limit and escalates to a fixer/human.

```markdown
## After Implementation

1. Run the build command to verify compilation
2. Run the FULL test suite — not just your new tests
3. ALL tests must pass. Do not commit if any test fails.
4. If a test fails:
   - If it's a test you wrote or modified: fix it
   - If it's a pre-existing test broken by your changes: fix your implementation, not the test
   - NEVER modify pre-written TDD tests. Fix your code to match the tests.
5. Commit your changes with a descriptive message
6. Do NOT push to remote
```

#### 4.5.2 Pre-Existing Test Health

If there are pre-existing broken or flaky tests in the codebase, they should
ideally be caught and addressed before doing any work. The orchestrator can
detect this by running the test suite on the integration branch _before_
scheduling any subtasks (during the `PLAN_REVIEW → TEST_WRITING` transition).
If tests fail on a clean branch, the task is blocked with a diagnostic until
the pre-existing failures are resolved.

#### 4.5.3 Test Result Verification at Merge

Before merging an agent's branch, the orchestrator verifies tests passed.
A retry allowance handles environmental flakiness (filesystem timing, network):

```go
func (o *Orchestrator) verifyTestsBeforeMerge(agent *model.Agent, subtask *model.Task) (*TestResult, error) {
    testCmd := o.getTestCommand(subtask) // from CLAUDE.md

    // Retry up to 3 times to handle environmental flakiness.
    // Tests must pass — but transient env issues (timing, filesystem)
    // get a few attempts before we block the merge.
    var lastResult *TestResult
    for attempt := 1; attempt <= 3; attempt++ {
        result := o.runCommand(agent.WorktreePath, testCmd)
        lastResult = &TestResult{
            Passed:   result.ExitCode == 0,
            Output:   result.Stdout,
            ExitCode: result.ExitCode,
        }
        if lastResult.Passed {
            return lastResult, nil
        }
        if attempt < 3 {
            time.Sleep(time.Duration(attempt) * 2 * time.Second)
        }
    }
    return lastResult, nil // all retries exhausted — block merge
}
```

If tests fail after retries, the merge is blocked and the agent is notified
(see §4.6).

#### 4.5.4 Test Result Storage

Store test results on the agent record for debugging and audit:

```go
type TestResult struct {
    Passed    bool      `json:"passed"`
    Output    string    `json:"output"`     // truncated to last 5000 chars
    ExitCode  int       `json:"exit_code"`
    RunAt     time.Time `json:"run_at"`
    Duration  float64   `json:"duration_seconds"`
}
```

This is stored in `agent.Context["last_test_result"]` and surfaced in the TUI.

---

### 4.6 Failure Recovery

#### 4.6.1 Agent-Level Test Failure

When a coder agent's tests fail:

```
1. Agent retries internally (it's instructed to fix and re-run)
2. If agent reaches 85% context window → orchestrator intervenes:
   a. Stop the agent
   b. Spawn a fixer agent with:
      - The test failure output
      - The agent's diff
      - The failing test file(s)
      - Instruction: "Fix the code to pass these tests. Do NOT modify the tests."
   c. If fixer reaches 80% context window → stop, escalate to human
```

#### 4.6.2 Context Window Monitoring

**Implemented** in `internal/ctxmon/` (commit fb390a9).

The orchestrator monitors each agent's context window usage via Claude Code's
built-in `statusLineScript` setting. A shell script (`ctxmon.StatusScript`)
is installed at `.claude/context-status.sh` in each agent's worktree. Claude
Code calls it with status JSON on stdin after every turn, and the script
extracts token counts + usage percentage into `.claude/context-usage.json`.

A `PreCompact` hook writes a signal file (`.claude/compaction-triggered`)
when Claude Code auto-compacts, providing an additional hard signal that the
agent has hit its context limit.

The `contextMonitorLoop` goroutine (one per agent, 5-second polling interval)
reads the usage file and compaction signal, updating both in-memory state
(`RunningAgent.ContextUsage`) and the DB (`agent.Config`). The orchestrator's
`checkContextUsage()` runs on every tick and takes action at two thresholds:

| Threshold | Default | Action |
|-----------|---------|--------|
| `context_warn_percent` | 75% | Log warning, emit `context_window_warning` event |
| `context_stop_percent` | 90% | Stop agent, fail task, emit `context_window_exceeded` event |
| Compaction triggered | N/A | Stop agent immediately (context at limit) |

Both thresholds are configurable in `drem.toml` (`context_warn_percent`,
`context_stop_percent`).

**TDD-specific escalation (to be built on this infrastructure):**

For the TDD enforcement flow, we layer graduated escalation on top of the
existing monitoring:

- **Implementation agents at `context_warn_percent` (75%)**: Log warning.
  The agent is likely struggling with test failures. No action yet.
- **Implementation agents at 85%**: Stop agent. Spawn a fixer with the test
  failure output and the agent's diff. Instruction: "Fix the code to pass
  these tests. Do NOT modify the tests."
- **Fixer agents at 80%**: Stop fixer. Escalate to human review with a
  diagnostic summary.

This requires a new intermediate threshold (`context_fixer_percent`, default
85) and logic in `checkContextUsage()` to distinguish agent roles and spawn
fixers instead of immediately failing the task.

#### 4.6.3 Test-Writing Phase Failure

When a test-writing agent itself fails (e.g., can't produce compilable tests,
exhausts its context window, or produces tests with syntax errors):

```
1. Agent reaches context_stop_percent → orchestrator stops it
2. Orchestrator inspects the agent's worktree:
   a. If test files exist and compile → treat as partial success:
      - Mark the test subtask as DONE
      - Log a warning that the agent was stopped early
      - The human will catch quality issues at TEST_REVIEW
   b. If no compilable test files exist → mark subtask as FAILED:
      - Spawn a new test-writing agent with:
        - The original subtask description
        - The failed agent's error output (last 2000 chars)
        - Instruction: "A previous agent failed to write these tests. Start fresh."
      - If the retry also fails → mark the parent task as FAILED with
        diagnostic: "Unable to generate compilable tests for subtask N"
        and escalate to human
```

No fixer agent is spawned for test-writing failures — unlike implementation
failures, there is no "fix the code to match the tests" framing available.
The test-writing agent IS the creative step; retrying with context is the
right recovery, and the human gate at `TEST_REVIEW` catches quality issues.

#### 4.6.4 Integration Test Failure (at `TESTING_READY`)

When the automated test suite fails on the integration branch:

```
1. Spawn a fixer agent on the integration worktree with:
   - Full test failure output
   - The diff between the feature branch and main
   - Instruction: "Fix the integration failures. Prefer fixing implementation code over modifying tests."
2. If fixer succeeds (tests pass) → proceed to MERGING
3. If fixer reaches 80% context window:
   a. Stop the fixer
   b. Write a diagnostic summary to the task
   c. Transition to a state where the human can review
      (keep in TESTING_READY, add a "needs_human_review" flag)
```

---

### 4.7 Planner Prompt Changes

Replace the current soft test guidance with mandatory TDD instructions:

```markdown
## Test-Driven Development (MANDATORY)

Every implementation subtask MUST have exactly ONE corresponding test subtask
that runs FIRST. This is a strict 1:1 mapping — no test subtask may cover
multiple implementation subtasks. This protects against subtle regressions
by keeping each test tightly scoped to its implementation.

### Test Subtask Requirements

For each implementation subtask, create exactly one test subtask that:
- Has phase: "test" and tests_for: [<impl subtask index>] (exactly one index)
- Writes tests that define the expected behavior BEFORE implementation
- Tests should initially FAIL (they test unimplemented behavior)
- Covers the acceptance criteria relevant to that implementation subtask
- Has agent_type: "coder"
- Has NO dependencies on implementation subtasks

### Implementation Subtask Requirements

Each implementation subtask must:
- Have phase: "implementation"
- Make the pre-written tests pass
- NEVER modify the pre-written tests — always fix implementation to match tests

Note: you do NOT need to add the test subtask to the implementation subtask's
`dependencies` — this dependency is auto-generated from `tests_for`. Only use
`dependencies` for ordering between implementation subtasks themselves.

### TDD Exceptions

If a subtask genuinely cannot be test-first, you MUST declare it in tdd_exceptions with:
- The subtask index
- A specific justification (not "too hard to test")

Valid exceptions:
- Integration/wiring subtasks connecting already-tested components
- Research subtasks producing documentation
- Infrastructure subtasks where the build IS the test

Invalid exceptions:
- "This is UI code" — UI code can have unit tests
- "This is a refactor" — refactors should preserve existing test behavior
- "Tests will be added later" — that's not TDD

### Ordering

test subtasks → HUMAN REVIEW → implementation subtasks → integration subtask

### Example Structure

Subtask 0: "Write tests for automation curve rendering" (phase: test, tests_for: [1])
Subtask 1: "Implement automation curve rendering" (phase: implementation)
  → auto-depends on subtask 0 via tests_for
Subtask 2: "Write tests for automation parameter binding" (phase: test, tests_for: [3])
Subtask 3: "Implement automation parameter binding" (phase: implementation)
  → auto-depends on subtask 2 via tests_for
Subtask 4: "Integration wiring" (phase: integration, depends: [1, 3])
```

---

### 4.8 Coder Agent Prompt Changes

#### 4.8.1 Test-Phase Coder

When a coder agent is working on a test-phase subtask:

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
```

#### 4.8.2 Test File Tracking

The plan's `files` field is a prediction of what the test agent will create.
The actual test files may differ. To provide accurate file paths to the
implementation-phase coder:

1. When a test-phase agent completes, the orchestrator runs `git diff
   --name-only` on the agent's branch against the integration branch base
2. Files matching test patterns (e.g., `*_test.go`, `*_test.py`,
   `test_*.py`, `*.test.ts`) are recorded in the subtask's context:
   `subtask.Context["actual_test_files"]`
3. The implementation-phase coder prompt is populated from
   `actual_test_files`, not the plan's `files` field

If the diff produces no test-pattern files, the orchestrator falls back to
the plan's `files` field and logs a warning.

#### 4.8.3 Implementation-Phase Coder

When a coder agent is working on an implementation-phase subtask:

```markdown
## Instructions

You are implementing code to pass pre-written tests (TDD).

Pre-written tests exist at: <list of test files from actual_test_files>

Your implementation should:
1. Read the pre-written tests first to understand expected behavior
2. Implement the minimum code to make ALL tests pass
3. Do NOT modify the pre-written tests unless they have a genuine bug
4. If you believe a test is wrong, note it in your commit message but make it pass anyway

After implementation:
1. Run the build command to verify compilation
2. Run the FULL test suite — ALL tests must pass
3. If any test fails, fix your implementation (not the test)
4. Commit with message: "feat: <what was implemented>"
```

---

### 4.9 Reviewer Agent Changes

#### 4.9.1 Plan Reviewer

The plan reviewer now additionally evaluates:

- Are test subtasks meaningful (not just stubs)?
- Does each test subtask cover the acceptance criteria of its corresponding impl subtask?
- Are TDD exceptions justified?

Add to the review.json schema:

```json
{
  "tdd_assessment": {
    "test_coverage_adequate": true,
    "exceptions_justified": true,
    "issues": ["Test subtask 0 only tests happy path, missing edge cases for..."]
  }
}
```

#### 4.9.2 Feature Reviewer (at `TESTING_READY`)

The feature reviewer now runs the full test suite as its first action and reports:

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

If tests fail, this blocks the `TESTING_READY → MERGING` transition automatically.

---

## 5. Implementation Plan

Ordered by dependency. Each phase builds on the previous.

### Phase 1: State Machine & Plan Schema

| Item | Files | Description |
|------|-------|-------------|
| 1a | `internal/state/machine.go` | Add `TEST_WRITING` and `TEST_REVIEW` states and transitions |
| 1b | `internal/model/task.go` | Add `StatusTestWriting`, `StatusTestReview` constants; add `StatusRejected` for subtasks rejected at `TEST_REVIEW` |
| 1c | `internal/orchestrator/plan_validation.go` | Add phase validation, TDD exception validation, test ordering checks |
| 1d | `internal/orchestrator/orchestrator.go` | Parse `phase`, `tests_for`, `tdd_exceptions` from plan JSON |

### Phase 2: Planner & Test Writing Flow

| Item | Files | Description |
|------|-------|-------------|
| 2a | `internal/prompt/prompt.go` | Rewrite `plannerInstructions()` with mandatory TDD guidance |
| 2b | `internal/prompt/prompt.go` | Add test-phase coder instructions variant |
| 2c | `internal/orchestrator/orchestrator.go` | Add baseline test health check — run test suite on integration branch before scheduling test subtasks; block with diagnostic if pre-existing tests fail |
| 2d | `internal/orchestrator/orchestrator.go` | Add `processTestWriting()` — schedule test-phase subtasks only |
| 2e | `internal/orchestrator/orchestrator.go` | Add `TEST_WRITING → TEST_REVIEW` transition on all test subtasks done |

### Phase 3: Human Test Review Gate

| Item | Files | Description |
|------|-------|-------------|
| 3a | `internal/orchestrator/orchestrator.go` | Add `HandleTestReviewApproved()` / `HandleTestReviewRejected()` — rejection marks old test subtasks `REJECTED`, clones new ones with feedback, transitions back to `TEST_WRITING` |
| 3b | `internal/prompt/prompt.go` | Update plan reviewer to assess TDD quality |
| 3c | `internal/tui/` | Surface `TEST_WRITING` and `TEST_REVIEW` states in dashboard — status colors, `a`/`r` keybindings for approve/reject at `TEST_REVIEW` (mirroring existing `PLAN_REVIEW` UX), rejection prompts for feedback text |

### Phase 4: Per-Agent Test Gate

| Item | Files | Description | Status |
|------|-------|-------------|--------|
| 4a | `internal/orchestrator/orchestrator.go` | Add `verifyTestsBeforeMerge()` — run test command, check exit code | |
| 4b | `internal/prompt/prompt.go` | Update implementation-phase coder instructions (tests mandatory, no flaky tolerance) | |
| 4c | `internal/ctxmon/`, `internal/agent/runner.go`, `internal/orchestrator/orchestrator.go` | Context window monitoring via status line script + PreCompact hook | **Done** (fb390a9) |
| 4d | `internal/orchestrator/orchestrator.go` | Add `context_fixer_percent` threshold (85%) and role-aware fixer escalation | |
| 4e | `internal/orchestrator/orchestrator.go` | Add test file tracking — `git diff --name-only` after test-phase agent completes, store `actual_test_files` in subtask context | |
| 4f | `internal/orchestrator/orchestrator.go` | Add test-writing failure recovery — retry once with error context, then fail to human (see §4.6.3) | |

### Phase 5: Automated `TESTING_READY` Gate

| Item | Files | Description |
|------|-------|-------------|
| 5a | `internal/orchestrator/orchestrator.go` | Replace manual `TESTING_READY` gate with automated reviewer spawn |
| 5b | `internal/orchestrator/orchestrator.go` | Add integration fixer flow (80% context → human escalation) |
| 5c | `internal/model/task.go` | Add `NeedsHumanReview` flag for fixer escalation |

---

## 6. Testing Strategy (for this PRD's implementation)

We will eat our own dogfood: implement this PRD using TDD, even before the orchestrator enforces it.

### Unit Tests

| Component | Tests |
|-----------|-------|
| State machine | `TEST_WRITING` and `TEST_REVIEW` transitions valid/invalid |
| Plan validation | Plans without test subtasks rejected; phase ordering enforced; TDD exceptions validated; `tests_for` auto-generates reverse dependency |
| Phase parsing | Plan JSON with `phase`, `tests_for`, `tdd_exceptions` parses correctly |
| `processTestWriting()` | Only test-phase subtasks scheduled; impl subtasks remain in backlog |
| `verifyTestsBeforeMerge()` | Tests pass → merge proceeds; tests fail → merge blocked |
| Context window check | 85%+ → triggers fixer; below threshold → no action |
| Test-phase prompt | Contains TDD-specific instructions; does NOT contain "run tests if applicable" |
| Impl-phase prompt | References pre-written test files from `actual_test_files`; mandates all tests pass |
| Test file tracking | `git diff --name-only` extracts test files; falls back to plan `files` if none found |
| Baseline health check | Pre-existing test failures block `TEST_WRITING` with diagnostic |

### Integration Tests

| Scenario | Expected Outcome |
|----------|-----------------|
| Plan with no test subtasks, no exceptions | Validation error, plan rejected |
| Plan with test subtasks | Test subtasks scheduled first, impl subtasks wait |
| Test subtask completes → all tests done | Parent transitions to `TEST_REVIEW` |
| Human approves tests | Parent transitions to `IN_PROGRESS`, impl subtasks scheduled |
| Human rejects tests with feedback | Old test subtasks marked `REJECTED`, new ones cloned with feedback, parent transitions to `TEST_WRITING` |
| Test-writing agent exhausts context (compilable tests exist) | Subtask marked DONE, warning logged, human catches quality at `TEST_REVIEW` |
| Test-writing agent exhausts context (no compilable tests) | Retry with error context; second failure → parent FAILED, human escalation |
| Impl agent's tests fail | Merge blocked, agent retries |
| Impl agent hits 85% context | Fixer spawned with test failure context |
| Fixer hits 80% context | Fixer stopped, human review requested |
| Full suite fails at `TESTING_READY` | Fixer spawned on integration branch |

---

## 7. Success Metrics

| Metric | Current | Target |
|--------|---------|--------|
| Plans with test subtasks | ~20% (planner's discretion) | 100% |
| Test review before implementation | 0% (no gate exists) | 100% |
| Agent branches merged with failing tests | Unknown (not tracked) | 0% |
| Feature-level test failures caught before merge to main | ~50% (manual) | >95% (automated) |
| Replanning due to missing tests (post-implementation) | Frequent | Rare (caught at plan review) |
| Human time spent on manual test verification | High (TESTING_READY gate) | Low (shifted to TEST_REVIEW of test design) |

---

## 8. Migration & Backward Compatibility

- **In-progress tasks**: Tasks already past `PLAN_REVIEW` when this ships continue with the old flow. Only newly planned tasks use the TDD flow.
- **Projects without test commands**: If CLAUDE.md has no test command, the orchestrator logs a warning and skips the automated test gate (falls back to manual). This is a degraded mode, not the norm.
- **Drem-orchestrator itself**: This project adopts TDD enforcement immediately. The CLAUDE.md already specifies `go test ./...` as the test command.

---

## 9. Resolved Questions

All open questions have been resolved. Decisions are recorded here for
traceability and reflected in the design sections above.

1. ~~**Test subtask granularity**~~ — **Resolved: strict 1:1 mapping enforced.** Each implementation subtask gets exactly one test subtask. `tests_for` must reference exactly one index. This protects against subtle regressions by keeping test scope tightly coupled to implementation scope. See §4.3.1 and §4.7.

2. ~~**Context window monitoring mechanism**~~ — **Resolved: status line script.** Implemented in `internal/ctxmon/` (commit fb390a9). Uses Claude Code's `statusLineScript` setting to extract token usage from the built-in status JSON, plus a `PreCompact` hook for compaction detection. No API proxy needed. See §4.6.2.

3. ~~**Test failure attribution**~~ — **Resolved: always fix implementation, not tests.** This is the correct default for TDD. If there is a genuine problem with a test, it will surface when the agent exhausts its context window and escalates to a fixer/human — at which point a human can make the judgement call. See §4.5.1.

4. ~~**Flaky test detection**~~ — **Resolved: retry allowance for environmental flakiness, but tests must pass.** The merge-time test gate retries up to 3 times with backoff to handle transient environmental issues (filesystem timing, network). But all tests must ultimately pass — no skipping. Pre-existing broken or flaky tests should be caught and addressed before work begins (baseline test health check at `PLAN_REVIEW → TEST_WRITING` transition). See §4.5.2 and §4.5.3.

5. ~~**Test-phase merge gate**~~ — **Resolved: skip test gate for test-phase subtasks.** Test-phase agents produce intentionally-failing tests. The merge-time test gate applies only to implementation and integration phase subtasks. Test-phase subtasks are gated on: tests compile, and failures are for the right reasons (missing implementation, not syntax errors). See §4.3.5.
