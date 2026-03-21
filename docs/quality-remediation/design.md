# Quality Remediation Design Document

Consolidated findings from two audits:
1. **Full repo constitution check** — constitution violations, test coverage gaps, ARCHITECTURE.md staleness, and human docs gaps
2. **Deep module interface audit** — shallow modules with unnecessarily broad public surfaces

Each issue below is scoped to a single prompt-sized unit of work.

---

## Phase 1: Constitution Violations (blocking — fails CI)

### 1.1 Fix gofmt violation

**File:** `internal/supervisor/types.go`

**Problem:** Not formatted per `gofmt`. Constitution rule: gofmt compliance 100% [enforced].

**Fix:** Run `gofmt -w internal/supervisor/types.go`. Verify with `gofmt -l internal/supervisor/types.go` (should produce no output).

**Acceptance criteria:**
- `gofmt -l internal/supervisor/types.go` returns no output
- `bash scripts/check_constitution.sh` gofmt check passes
- All existing tests still pass

---

### 1.2 Shrink task_processing.go below 800 lines

**File:** `internal/orchestrator/task_processing.go` (808 lines, limit is 800)

**Problem:** 8 lines over the 800-line ceiling. Constitution rule: File length ceiling [enforced].

**Fix:** Extract a coherent group of related functions (e.g., merge conflict handling, or supervisor invocation helpers) into a new file in the same package. The extracted functions should form a logical unit, not an arbitrary split.

**Acceptance criteria:**
- `wc -l internal/orchestrator/task_processing.go` <= 800
- New file is under 800 lines
- No new exported symbols added (keep extractions package-private if possible)
- All existing tests still pass
- `bash scripts/check_constitution.sh` file length check passes

---

### 1.3 Shrink prompt.go below 800 lines

**File:** `internal/prompt/prompt.go` (803 lines, limit is 800)

**Problem:** 3 lines over the 800-line ceiling. Constitution rule: File length ceiling [enforced].

**Fix:** Extract a coherent group of prompt-building helpers into a separate file in the same package. Look for a natural seam — e.g., agent-type-specific prompt sections, or constraint/context injection helpers.

**Acceptance criteria:**
- `wc -l internal/prompt/prompt.go` <= 800
- New file is under 800 lines
- All existing tests still pass
- `bash scripts/check_constitution.sh` file length check passes

---

### 1.4 Reduce orchestrator internal imports below baseline

**Package:** `internal/orchestrator/` (37 internal imports, baseline is 35)

**Problem:** 2 imports over the shrink-only baseline of 35. Constitution rule: Package import ceiling [enforced].

**Fix:** Audit all `internal/` imports across orchestrator's source files. Identify the 2 newest imports and determine whether they can be:
- Pushed to a dependency (inversion)
- Accessed through an existing import (indirect)
- Consolidated with a related import

Run: `grep -rh 'internal/' internal/orchestrator/*.go | grep -v _test.go | sort -u` to see all imports.

**Acceptance criteria:**
- Internal import count for `internal/orchestrator/` <= 35
- All existing tests still pass
- `bash scripts/check_constitution.sh` import ceiling check passes

---

## Phase 2: Deep Module Narrowing

### 2.1 Narrow supervisor public surface

**Package:** `internal/supervisor/`
**Current state:** 61 exports, 799 LOC (ratio 0.076)
**Target:** ~10-15 exports

**Problem:** Exports 10+ diagnostic struct types that are JSON carriers for internal decisions: `FailureDiagnosis`, `FeedbackIntegration`, `MergeConflictAnalysis`, `BuildFailureDiagnosis`, `PlanDepthReview`, `DepthConstraintDiagnosis`, `AssumptionCrossCheck`, `MissedAssumption`, `DepthViolation`, etc.

**Fix:**
1. Audit which supervisor types are referenced outside the supervisor package. Run: `grep -rn 'supervisor\.' internal/ --include='*.go' | grep -v 'internal/supervisor/' | grep -v '_test.go'` to find external consumers.
2. For types only used within `internal/supervisor/` and its tests, unexport them (lowercase the first letter).
3. For types that MUST be exported because other packages reference them, keep them exported.
4. Move unexported types and helper functions to internal-only files if needed.
5. Do NOT change any behavior — only visibility.

**Acceptance criteria:**
- External consumers of the supervisor package still compile
- All existing tests pass
- Export count reduced (measure with `grep -c '^func [A-Z]\|^type [A-Z]\|^func (.*) [A-Z]' internal/supervisor/*.go | grep -v _test`)
- No new packages created

---

### 2.2 Narrow constraints public surface

**Package:** `internal/constraints/`
**Current state:** 126 exports, 1100 LOC (ratio 0.115)
**Target:** ~30-40 exports

**Problem:** Exposes internal plumbing (`globFiles`, `matchDoublestarGlob`, `isGitIgnored`, `evalMaxLines`) and every constraint type struct individually. Consumers should only need `Config`, `LoadConfig`, `Evaluate`, `Report`, `Result`, `FormatReport`.

**Fix:**
1. Audit which constraint types and helper functions are referenced outside the constraints package. Run: `grep -rn 'constraints\.' internal/ --include='*.go' | grep -v 'internal/constraints/' | grep -v '_test.go'`
2. Unexport internal helpers: `globFiles`, `matchDoublestarGlob`, `isGitIgnored`, `evalMaxLines`, and similar.
3. Unexport individual constraint type structs if they are only consumed internally (e.g., `CommandConstraint`, `MaxLinesConstraint`, `MaxMatchesConstraint`, `NoMatchConstraint`).
4. Keep exported: `Config`, `LoadConfig`, `Evaluate`, `EvaluateFiles`, `Report`, `Result`, `FormatReport`, and anything referenced externally.
5. Do NOT change any behavior — only visibility.

**Acceptance criteria:**
- External consumers still compile
- All existing tests pass
- Export count reduced significantly
- `bash scripts/check_constitution.sh` still passes (export ratio should improve)

---

### 2.3 Narrow clarification public surface

**Package:** `internal/clarification/`
**Current state:** 35 exports, 472 LOC (ratio 0.074)
**Target:** ~8-12 exports

**Problem:** Exports internal types that consumers don't need: implementation detail types like individual struct fields and helper functions.

**Fix:**
1. Audit external references: `grep -rn 'clarification\.' internal/ --include='*.go' | grep -v 'internal/clarification/' | grep -v '_test.go'`
2. Keep exported only what external consumers need (likely: the main evaluation function, result type, and types referenced by the orchestrator).
3. Unexport everything else.
4. Do NOT change any behavior — only visibility.

**Acceptance criteria:**
- External consumers still compile
- All existing tests pass
- Export count reduced to ~8-12
- No new packages created

---

## Phase 3: ARCHITECTURE.md Update

### 3.1 Update ARCHITECTURE.md to reflect current codebase

**File:** `ARCHITECTURE.md`

**Problem:** The architecture document is stale in several areas:

**Missing from package listing:**
- `agentmon` — agent transcript monitoring and signal extraction
- `clarification` — plan clarification loop (assumption evaluation, question dedup, user feedback)
- `constraints` — constitution/quality constraint enforcement engine
- `ctxmon` — context window monitoring for agents
- `score` — quality scoring engine (TDD, Constitution, Documentation, Depth)
- `testutil` — shared test infrastructure (already documented in README's Writing Tests section but missing from the architecture diagram)

**Missing from cmd/ listing:**
- `cmd/check-constraints` — CLI for running constitution checks
- `cmd/ctxmon` — CLI for context window monitoring in agent worktrees

**Missing task lifecycle states (add to the lifecycle diagram and descriptions):**
- `needs_clarification` — entered when plan assumptions need human clarification before proceeding to plan_review
- `test_writing` — TDD phase: test agent is writing tests before implementation
- `test_review` — human gate: review written tests before implementation begins
- `rejected` — task rejected at a review gate

**Missing config options (add to Configuration table):**
- `context_warn_percent` — Context usage percentage that triggers a warning
- `context_stop_percent` — Context usage percentage that triggers a hard stop
- `context_fixer_percent` — Context usage percentage that triggers fixer escalation
- `test_command` — Build/test command for the project
- `compile_command` — Compile command for the project
- `scoped_tests` — Whether to run scoped tests per subtask
- `test_timeout` — Timeout for test runs

**Fix:** Update ARCHITECTURE.md to include all of the above. Keep the same style and structure as the existing document. Update the graduation path section if the constraint system has evolved.

**Acceptance criteria:**
- All packages in `internal/` are listed in the architecture diagram
- All `cmd/` tools are documented
- Task lifecycle diagram includes all states from `internal/model/enums.go`
- Configuration table includes all options from `cmd/drem/config.go`
- No stale information remains
- Document still reads coherently (not just a dump of additions)

---

## Phase 4: README Update

### 4.1 Add Plan Clarification section to README

**File:** `README.md`

**Problem:** The plan clarification feature (internal/clarification, wired into the orchestrator state machine, with TUI support) has no user-facing documentation. This is a significant workflow change — after a planner generates a plan, the system may now enter a clarification loop before plan_review.

**What to document:**
- What clarification is (the system evaluates plan assumptions and may ask the user questions before presenting the plan for review)
- When it triggers (after planning, before plan_review)
- How users interact with it in the TUI (what key, what format, the `/done` command to finish answering)
- How it affects the task lifecycle (new `needs_clarification` state)
- Brief mention that the supervisor evaluates assumption risk

**Where to add:** After the "Task Lifecycle" section, add a new "## Plan Clarification" section. Also update the task lifecycle diagram to include the `needs_clarification` state.

**Acceptance criteria:**
- New section explains clarification to a project operator (not a developer)
- Task lifecycle diagram updated with `needs_clarification` state
- TUI keybindings table updated if clarification has unique keys
- README still reads coherently

---

### 4.2 Add Constraints Configuration guide to README

**File:** `README.md`

**Problem:** The constraint system is critical infrastructure but README only mentions it in passing. Operators need to know how to author `.drem/constraints.toml` for their projects.

**What to document:**
- What constraints are (automated quality checks that enforce the constitution)
- Where they are defined (`.drem/constraints.toml`)
- The constraint types available (`command`, `max_lines`, `max_matches`, `no_match`) with one example of each
- How to run checks manually (`bash scripts/check_constitution.sh`)
- How constraints are enforced automatically (plan validation, post-agent gate, integration gate)

**Where to add:** Expand the existing "Running the Constitution Check" troubleshooting section into a full "## Quality Constraints" section, or add a new section after "Supervisor".

**Source material:** `docs/constraints-system/design.md` and `.drem/constraints.toml` for examples.

**Acceptance criteria:**
- Operators can read the README and author a new constraint without reading source code
- At least one example of each constraint type is shown
- Manual and automatic enforcement are both explained

---

### 4.3 Add Scoring Interpretation guide to README

**File:** `README.md`

**Problem:** The scoring system shows badges (T:85 C:100 D:0 Dp:67) but README only defines what each dimension measures. Users don't know what the numbers mean or how to improve them.

**What to document:**
- What each score dimension means in plain language
- Score ranges: what's good (>80), acceptable (60-80), concerning (<60)
- What to do when a specific score is low (e.g., low TDD → add test subtasks, low Constitution → check constraint violations)
- How scores differ between plan_review and testing_ready gates
- How to read the compact badge format

**Where to add:** Expand the existing "Step Scores" section under Task Lifecycle.

**Acceptance criteria:**
- A new operator can read the section and understand what to do when they see "T:40 C:100 D:0 Dp:33"
- Actionable guidance for each dimension

---

### 4.4 Update README task lifecycle diagram

**File:** `README.md`

**Problem:** The task lifecycle diagram is missing 4 states: `needs_clarification`, `test_writing`, `test_review`, `rejected`.

**Fix:** Update the ASCII diagram and state descriptions to include all states from `internal/model/enums.go`. Show where each new state fits in the flow:
- `needs_clarification` between `planning` and `plan_review`
- `test_writing` and `test_review` between `plan_review` and `in_progress`
- `rejected` as a terminal state reachable from review gates

**Acceptance criteria:**
- Diagram matches all states in `internal/model/enums.go`
- Each new state has a one-line description like the existing ones
- Transitions to/from new states are shown

---

### 4.5 Document undocumented configuration options in README

**File:** `README.md`

**Problem:** 7 config options are in `cmd/drem/config.go` but not in README's Configuration table.

**Missing options:**
- `context_warn_percent` — Context usage warning threshold (default from code)
- `context_stop_percent` — Context usage hard-stop threshold
- `context_fixer_percent` — Threshold for spawning fixer on context pressure
- `test_command` — Command to run tests (e.g., `go test ./...`)
- `compile_command` — Command to compile the project
- `scoped_tests` — Run tests scoped to subtask file changes
- `test_timeout` — Timeout for test command execution

**Fix:** Add these to the Configuration table with descriptions. Read `cmd/drem/config.go` for defaults and exact field names.

**Acceptance criteria:**
- All config options in `cmd/drem/config.go` appear in README's Configuration table
- Default values documented where available

---

## Phase 5: Test Coverage Improvements

### 5.1 Improve TUI test coverage

**Package:** `internal/tui/` (38.6% coverage — lowest in repo)

**Problem:** The TUI is the least-tested package. While UI testing is inherently harder, key logic paths should be covered.

**Fix:** Write tests for the non-rendering logic in the TUI package:
- Key handler dispatch (given a key event, does the right action fire?)
- State transitions (panel switching, mode changes)
- Data formatting (score badge rendering, status color mapping)
- Filter logic (task filtering, agent filtering)

Do NOT test Bubble Tea rendering output. Focus on the logic that drives the UI, not the visual output.

**Acceptance criteria:**
- Coverage rises from 38.6% to at least 55%
- Tests cover key handler dispatch for all documented keybindings
- Tests cover score badge formatting
- All existing tests still pass

---

### 5.2 Improve agent package test coverage

**Package:** `internal/agent/` (59.9% coverage)

**Problem:** Agent spawning and lifecycle management is core functionality with below-average coverage.

**Fix:** Add tests for:
- Agent configuration parsing
- Process spawn argument construction
- Heartbeat timeout detection
- Completion signal handling
- Exit info extraction

Use mocks/stubs for tmux and subprocess calls — do NOT spawn real processes in tests.

**Acceptance criteria:**
- Coverage rises from 59.9% to at least 70%
- No real tmux sessions or Claude processes spawned in tests
- All existing tests still pass

---

### 5.3 Improve orchestrator test coverage

**Package:** `internal/orchestrator/` (60.1% coverage)

**Problem:** The orchestrator is the largest package (7004 LOC) with moderate coverage. Key scheduling and lifecycle paths may be untested.

**Fix:** Identify untested functions by running `go test -coverprofile=coverage.out ./internal/orchestrator/ && go tool cover -func=coverage.out | grep -v '100.0%' | sort -t: -k3 -n`. Focus new tests on:
- Task scheduling decisions
- Dependency resolution
- Reconciliation edge cases
- State transition error paths

**Acceptance criteria:**
- Coverage rises from 60.1% to at least 68%
- New tests use `testutil.NewTestDB` and `testutil.SetupBareRepo`
- All existing tests still pass

---

## Dependency Order

```
Phase 1 (constitution fixes): 1.1, 1.2, 1.3, 1.4 — all independent, can run in parallel
Phase 2 (deep module narrowing): 2.1, 2.2, 2.3 — all independent, can run in parallel
Phase 3 (ARCHITECTURE.md): 3.1 — depends on Phase 1 and 2 being done (so doc reflects final state)
Phase 4 (README): 4.1, 4.2, 4.3, 4.4, 4.5 — all independent, can run in parallel; should run after Phase 3
Phase 5 (test coverage): 5.1, 5.2, 5.3 — all independent, can run in parallel; can run anytime
```
