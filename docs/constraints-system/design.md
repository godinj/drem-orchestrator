# Design: Project-Agnostic Constraint System

**Date**: 2026-03-15
**Status**: Draft
**Scope**: `internal/constraints/`, `internal/prompt/`, `internal/orchestrator/`, `.drem/`

---

## 1. Problem Statement

The orchestrator has no automated enforcement of architectural constraints during the agent workflow. The current `ARCHITECTURE.md` defines concrete, falsifiable rules and `scripts/check_constitution.sh` can check them — but neither is wired into the orchestrator's pipeline:

1. **Planner agents don't see architectural constraints.** The planner prompt includes `CLAUDE.md` (build commands) but not `ARCHITECTURE.md`. Planners produce plans that violate structural rules (e.g., adding methods to grandfathered files under a shrink-only rule).

2. **Plan validation doesn't check constraints.** `ValidatePlan()` checks plan structure (TDD phases, dependencies, file overlaps) but not whether `estimated_files` target constrained files.

3. **No post-agent quality gate.** After an agent completes, its branch is merged into integration with only build verification — no constitution compliance check. Violations land in the integration branch undetected.

4. **No integration gate.** When all subtasks complete and the parent transitions to `testing_ready`, there's no automated constraint check on the integration branch. The human reviewer is the only line of defense.

5. **Constraints are project-specific but hardcoded.** `check_constitution.sh` is hand-maintained bash with drem-orchestrator-specific rules. Other projects (e.g., drem-canvas with C++ and different thresholds) would need entirely separate scripts. There's no project-agnostic constraint definition format.

---

## 2. Goals

1. **Project-agnostic constraint format** — A `.drem/constraints.toml` file checked into each project repo defines all constraints. The orchestrator reads this file and enforces constraints without project-specific code.

2. **Planner awareness** — The planner prompt includes configurable context files (e.g., `ARCHITECTURE.md`) so the planner designs plans that respect structural constraints.

3. **Plan-level constraint checking** — The plan validator checks `estimated_files` against declared constraints (file length ceilings, shrink-only exceptions) and produces warnings/errors before the plan reaches `plan_review`.

4. **Post-agent constraint gate** — After an agent completes, constraints are evaluated on the agent's worktree before merging into integration. Violations trigger retry with feedback.

5. **Integration gate** — Before transitioning to `testing_ready`, constraints are evaluated on the integration worktree. Violations block the transition.

6. **Replace `check_constitution.sh`** — The bash script becomes either generated from `constraints.toml` or replaced by a Go runner that evaluates the same TOML-defined constraints.

## 3. Non-Goals

- Replacing language-specific linters (gofmt, clang-format) — these remain as `command` constraints configured per project.
- Enforcing naming conventions or code style beyond what regex can match — complex style rules stay as `command` constraints wrapping external tools.
- Real-time enforcement during agent execution — constraints are checked at pipeline boundaries (post-agent, pre-merge, integration gate), not continuously.

---

## 4. Design

### 4.1. Constraint Definition Format

All constraints live in `.drem/constraints.toml` at the repo root. The file is committed to the repo and available in every worktree.

#### 4.1.1. Top-Level Configuration

```toml
# Files to include verbatim in planner and coder agent prompts.
# Paths relative to repo root.
context_files = ["ARCHITECTURE.md"]
```

#### 4.1.2. Constraint Types

Four constraint types cover all rules from both drem-orchestrator's and drem-canvas's `ARCHITECTURE.md`:

**`command`** — Run a shell command in the worktree root. Exit 0 = pass.

```toml
[[command]]
name = "gofmt compliance"
run  = "gofmt -l ./internal/ ./cmd/"
expect = "empty_output"   # or "exit_zero" (default)

[[command]]
name = "go vet"
run  = "go vet ./..."
```

**`max_lines`** — Per-file line count ceiling with glob filtering and exceptions.

```toml
[[max_lines]]
name    = "File length ceiling"
glob    = "internal/**/*.go"
exclude = ["*_test.go"]
limit   = 800

[[max_lines.exception]]
path           = "internal/orchestrator/orchestrator.go"
rule           = "shrink-only"
baseline_lines = 2246
```

**`max_matches`** — Count regex matches per file or per directory. Fail if > limit.

```toml
[[max_matches]]
name    = "Exported function count"
glob    = "internal/**/*.go"
exclude = ["*_test.go"]
pattern = '^func [A-Z]'
limit   = 20
scope   = "file"          # or "directory"

[[max_matches.exception]]
path            = "internal/orchestrator/orchestrator.go"
rule            = "shrink-only"
baseline_count  = 44
```

**`no_match`** — Regex must NOT appear in matching files.

```toml
[[no_match]]
name         = "DB init outside testutil"
glob         = "internal/**/*_test.go"
exclude_path = ["internal/testutil/"]
pattern      = 'gorm\.Open\(sqlite'
```

#### 4.1.3. Exception Rules

Exceptions modify how a constraint applies to a specific file or directory:

- **`shrink-only`** — The metric (lines, match count) must not exceed `baseline_lines` or `baseline_count`. The baseline is stored in the TOML and represents the value at the time the exception was declared. Any change that increases the metric above the baseline is a violation.

#### 4.1.4. Full Example: drem-orchestrator

```toml
context_files = ["ARCHITECTURE.md"]

# ── Commands ──────────────────────────────────────────────────

[[command]]
name   = "gofmt compliance"
run    = "gofmt -l ./internal/ ./cmd/"
expect = "empty_output"

[[command]]
name = "go vet"
run  = "go vet ./..."

# ── File Length ───────────────────────────────────────────────

[[max_lines]]
name    = "File length ceiling"
glob    = "internal/**/*.go"
exclude = ["*_test.go"]
limit   = 800

[[max_lines.exception]]
path           = "internal/orchestrator/orchestrator.go"
rule           = "shrink-only"
baseline_lines = 2246

# ── Symbol Counts ────────────────────────────────────────────

[[max_matches]]
name    = "Exported function count"
glob    = "internal/**/*.go"
exclude = ["*_test.go"]
pattern = '^func [A-Z]'
limit   = 20
scope   = "file"

[[max_matches.exception]]
path           = "internal/orchestrator/orchestrator.go"
rule           = "shrink-only"
baseline_count = 44

[[max_matches]]
name    = "Internal import ceiling"
glob    = "internal/**/*.go"
exclude = ["*_test.go"]
pattern = '".*internal/'
limit   = 6
scope   = "directory"

[[max_matches.exception]]
path           = "internal/orchestrator/"
rule           = "shrink-only"
baseline_count = 8

[[max_matches]]
name    = "GORM hook consolidation"
glob    = "internal/model/models.go"
pattern = 'func.*BeforeCreate'
limit   = 1

# ── Forbidden Patterns ───────────────────────────────────────

[[no_match]]
name         = "DB init outside testutil"
glob         = "internal/**/*_test.go"
exclude_path = ["internal/testutil/"]
pattern      = 'gorm\.Open\(sqlite'

[[no_match]]
name         = "Git helpers outside testutil"
glob         = "internal/**/*_test.go"
exclude_path = ["internal/testutil/"]
pattern      = 'func setupBareRepo|func initBareRepo|func addWorktree|func commitFile'

[[no_match]]
name         = "Test factories outside testutil"
glob         = "internal/**/*_test.go"
exclude_path = ["internal/testutil/"]
pattern      = 'func createTest|func newTest|func mockTestDB|func lifecycleTestDB'
```

### 4.2. Go Package: `internal/constraints/`

New package that parses and evaluates `.drem/constraints.toml`.

#### 4.2.1. Core Types

```go
// Config is the top-level structure of .drem/constraints.toml.
type Config struct {
    ContextFiles []string          `toml:"context_files"`
    Commands     []CommandConstraint   `toml:"command"`
    MaxLines     []MaxLinesConstraint  `toml:"max_lines"`
    MaxMatches   []MaxMatchesConstraint `toml:"max_matches"`
    NoMatch      []NoMatchConstraint   `toml:"no_match"`
}

// CommandConstraint runs a shell command and checks exit code or output.
type CommandConstraint struct {
    Name   string `toml:"name"`
    Run    string `toml:"run"`
    Expect string `toml:"expect"` // "exit_zero" (default) or "empty_output"
}

// MaxLinesConstraint enforces per-file line count ceilings.
type MaxLinesConstraint struct {
    Name       string          `toml:"name"`
    Glob       string          `toml:"glob"`
    Exclude    []string        `toml:"exclude"`
    Limit      int             `toml:"limit"`
    Exceptions []LinesException `toml:"exception"`
}

type LinesException struct {
    Path          string `toml:"path"`
    Rule          string `toml:"rule"`           // "shrink-only"
    BaselineLines int    `toml:"baseline_lines"`
}

// MaxMatchesConstraint counts regex matches per file or directory.
type MaxMatchesConstraint struct {
    Name       string            `toml:"name"`
    Glob       string            `toml:"glob"`
    Exclude    []string          `toml:"exclude"`
    Pattern    string            `toml:"pattern"`
    Limit      int               `toml:"limit"`
    Scope      string            `toml:"scope"` // "file" (default) or "directory"
    Exceptions []MatchesException `toml:"exception"`
}

type MatchesException struct {
    Path          string `toml:"path"`
    Rule          string `toml:"rule"`            // "shrink-only"
    BaselineCount int    `toml:"baseline_count"`
}

// NoMatchConstraint ensures a pattern does NOT appear in matching files.
type NoMatchConstraint struct {
    Name        string   `toml:"name"`
    Glob        string   `toml:"glob"`
    ExcludePath []string `toml:"exclude_path"`
    Pattern     string   `toml:"pattern"`
}
```

#### 4.2.2. Core API

```go
// LoadConfig reads and parses .drem/constraints.toml from the given worktree root.
// Returns nil config (no error) if the file does not exist.
func LoadConfig(worktreeRoot string) (*Config, error)

// Result is the outcome of evaluating a single constraint.
type Result struct {
    Name     string   // constraint name
    Type     string   // "command", "max_lines", "max_matches", "no_match"
    Passed   bool
    Messages []string // details (violations, warnings)
}

// Report is the aggregate outcome of evaluating all constraints.
type Report struct {
    Results  []Result
    Passed   int
    Failed   int
}

// Evaluate runs all constraints in the config against the given worktree root.
// Returns a Report with per-constraint results.
func Evaluate(cfg *Config, worktreeRoot string) (*Report, error)

// EvaluateFiles checks only file-based constraints (max_lines, max_matches, no_match)
// against a specific set of file paths. Used by the plan validator to check
// estimated_files without running command constraints.
func EvaluateFiles(cfg *Config, worktreeRoot string, files []string) (*Report, error)

// FormatReport renders a Report as human-readable text (same format as
// check_constitution.sh output: PASS/FAIL/WARN lines with a summary footer).
func FormatReport(report *Report) string
```

#### 4.2.3. Shrink-Only Evaluation

For `shrink-only` exceptions:
- `max_lines`: Compare current `wc -l` of the file against `baseline_lines`. If current > baseline, fail. If current <= baseline, pass (the file is within or shrinking from its baseline).
- `max_matches`: Compare current match count against `baseline_count`. Same logic.

The regular constraint limit is ignored for excepted files — only the baseline matters.

#### 4.2.4. TOML Parsing

Use the `github.com/BurntSushi/toml` library (standard Go TOML parser). If not already a dependency, add it. Check `go.mod` first.

### 4.3. Planner Prompt Integration

Modify `internal/prompt/prompt.go` to:

1. Call `constraints.LoadConfig(worktreePath)` to read `.drem/constraints.toml`.
2. For each path in `cfg.ContextFiles`, read the file from the worktree and append its contents to the planner prompt as a new section (similar to how build commands from `CLAUDE.md` are included today).
3. The section header should be `## Architecture & Constraints` (or similar) and include the full file contents.
4. Also include context files in coder agent prompts, not just planner prompts. Both agent types need architectural awareness.

**Key constraint**: The `readBuildCommands` function in `prompt.go` already reads `CLAUDE.md`. The new logic should follow the same pattern (read from worktree, include in prompt, gracefully handle missing files).

### 4.4. Plan Validation Integration

Modify `internal/orchestrator/plan_validation.go` to add a new validation function:

```go
// ValidatePlanConstraints checks a plan's estimated_files against the
// constraint config. Returns warnings for files that target constrained
// or grandfathered paths.
func ValidatePlanConstraints(subtasks []planEntry, cfg *constraints.Config) PlanValidationResult
```

This function:
1. Collects all `estimated_files` / `files` from each subtask.
2. For each `max_lines` constraint with exceptions, checks if any estimated file matches an exception path. If so, warns: "Subtask N targets grandfathered file X (shrink-only, baseline: Y lines) — verify changes don't increase line count."
3. For each `max_matches` constraint with exceptions, same check.
4. For `max_lines` constraints without exceptions, checks if any targeted file is already near the limit (>90% of max) and warns.

The result is merged with the existing `ValidatePlan` result so both structural and constraint warnings appear at `plan_review`.

**Where this is called**: In `agent_results.go`, in `onPlannerCompleted()`, after the existing `ValidatePlan()` call. Load the constraint config once and pass it to both validators.

### 4.5. Post-Agent Constraint Gate

Modify `internal/orchestrator/agent_results.go` to add constraint evaluation after agent completion:

In the agent completion handler (where build verification already runs after merge):
1. Load constraint config from the agent's worktree.
2. Call `constraints.Evaluate(cfg, worktreePath)`.
3. If any constraint fails:
   - Store the violation report in the subtask's context (`task.Context["constraint_violations"]`).
   - Log the violations.
   - The subtask is marked as failed with the constraint report as feedback, so the next retry attempt knows what to fix.
4. If all constraints pass, proceed with the existing flow.

**Important**: Only run non-command constraints (max_lines, max_matches, no_match) at the post-agent stage. Command constraints (like `go vet`, `gofmt`) may be expensive and are already covered by build verification or can run at the integration gate. Use `EvaluateFiles` rather than full `Evaluate` for speed.

### 4.6. Integration Gate

Modify `internal/orchestrator/task_processing.go` in `checkFeatureCompletion()`:

Before transitioning the parent task to `testing_ready`:
1. Load constraint config from the integration worktree.
2. Call `constraints.Evaluate(cfg, integrationWorktreePath)` — full evaluation including command constraints.
3. If any constraint fails:
   - Store violations in the parent task's context.
   - Block the transition to `testing_ready`.
   - Log the violations.
   - The task remains in `in_progress` with constraint feedback visible in the TUI.
4. If all constraints pass, proceed with the transition.

### 4.7. Replace `check_constitution.sh`

Once the Go constraint runner is working, `check_constitution.sh` can be replaced with a thin wrapper:

```bash
#!/usr/bin/env bash
# Evaluate constraints from .drem/constraints.toml
go run ./cmd/drem-check/... "$@"
```

Or provide a subcommand: `drem check` that loads and evaluates constraints. This is optional and lower priority than the pipeline integration.

---

## 5. File Ownership

### New Files

| File | Description |
|------|-------------|
| `internal/constraints/config.go` | TOML parsing, Config types |
| `internal/constraints/evaluate.go` | Constraint evaluation logic (Evaluate, EvaluateFiles) |
| `internal/constraints/report.go` | Result/Report types, FormatReport |
| `internal/constraints/config_test.go` | Tests for TOML parsing |
| `internal/constraints/evaluate_test.go` | Tests for constraint evaluation |
| `.drem/constraints.toml` | drem-orchestrator's constraint definitions |

### Modified Files

| File | Change |
|------|--------|
| `internal/prompt/prompt.go` | Read context_files from constraints config, include in agent prompts |
| `internal/orchestrator/plan_validation.go` | Add `ValidatePlanConstraints()` |
| `internal/orchestrator/agent_results.go` | Post-agent constraint evaluation; load config for plan validation |
| `internal/orchestrator/task_processing.go` | Integration gate constraint check in `checkFeatureCompletion()` |
| `go.mod` / `go.sum` | Add `github.com/BurntSushi/toml` if not already present |

---

## 6. Agent Decomposition

### Phase 1 — Foundation (sequential)

**Agent 01: Constraint parser and evaluator**

Create `internal/constraints/` package with full TOML parsing, constraint evaluation, and tests. Also create `.drem/constraints.toml` for drem-orchestrator (migrating all rules from `ARCHITECTURE.md` and `check_constitution.sh`).

This is the foundation — all other agents depend on this package existing.

### Phase 2 — Integration points (parallel, 4 agents)

These agents are independent — they touch different files and can run concurrently.

**Agent 02: Planner prompt integration**
- Modify `prompt.go` to read `context_files` and include in planner + coder prompts.

**Agent 03: Plan validation integration**
- Add `ValidatePlanConstraints()` to `plan_validation.go`.
- Wire it into `onPlannerCompleted()` in `agent_results.go`.

**Agent 04: Post-agent constraint gate**
- Add constraint evaluation after agent completion in `agent_results.go`.
- Use `EvaluateFiles` for speed.

**Agent 05: Integration gate**
- Add full `Evaluate` call in `checkFeatureCompletion()` in `task_processing.go`.

### Phase 3 — Cleanup (sequential)

**Agent 06: Migration and documentation**
- Replace `check_constitution.sh` with a wrapper that uses the Go runner.
- Update `ARCHITECTURE.md` graduation path to reference the constraint system.
- Update README with `.drem/constraints.toml` documentation.
- Run full test suite to verify no regressions.
