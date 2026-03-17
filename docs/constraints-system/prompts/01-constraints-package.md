# Agent: Constraint Parser and Evaluator

You are working on the `master` branch of Drem Orchestrator, a Go-based agent orchestration system.
Your task is to create the `internal/constraints/` package — a project-agnostic constraint system that parses `.drem/constraints.toml` and evaluates constraints against a worktree.

## Context

Read these before starting:
- `docs/constraints-system/design.md` (sections 4.1, 4.2 — constraint format and Go package design)
- `ARCHITECTURE.md` (the rules this system will enforce — use as reference for the `.drem/constraints.toml` you'll create)
- `scripts/check_constitution.sh` (the bash script being replaced — every check here must be expressible in the TOML format)
- `go.mod` (confirm `github.com/BurntSushi/toml` is already a dependency)

## Deliverables

### New package: `internal/constraints/`

#### 1. `config.go` — TOML parsing and types

Define the constraint configuration types and parser:

```go
package constraints

// Config is the top-level structure of .drem/constraints.toml.
type Config struct {
    ContextFiles []string             `toml:"context_files"`
    Commands     []CommandConstraint   `toml:"command"`
    MaxLines     []MaxLinesConstraint  `toml:"max_lines"`
    MaxMatches   []MaxMatchesConstraint `toml:"max_matches"`
    NoMatch      []NoMatchConstraint   `toml:"no_match"`
}

type CommandConstraint struct {
    Name   string `toml:"name"`
    Run    string `toml:"run"`
    Expect string `toml:"expect"` // "exit_zero" (default) or "empty_output"
}

type MaxLinesConstraint struct {
    Name       string           `toml:"name"`
    Glob       string           `toml:"glob"`
    Exclude    []string         `toml:"exclude"`
    Limit      int              `toml:"limit"`
    Exceptions []LinesException `toml:"exception"`
}

type LinesException struct {
    Path          string `toml:"path"`
    Rule          string `toml:"rule"`           // "shrink-only"
    BaselineLines int    `toml:"baseline_lines"`
}

type MaxMatchesConstraint struct {
    Name       string             `toml:"name"`
    Glob       string             `toml:"glob"`
    Exclude    []string           `toml:"exclude"`
    Pattern    string             `toml:"pattern"`
    Limit      int                `toml:"limit"`
    Scope      string             `toml:"scope"` // "file" (default) or "directory"
    Exceptions []MatchesException `toml:"exception"`
}

type MatchesException struct {
    Path          string `toml:"path"`
    Rule          string `toml:"rule"`            // "shrink-only"
    BaselineCount int    `toml:"baseline_count"`
}

type NoMatchConstraint struct {
    Name        string   `toml:"name"`
    Glob        string   `toml:"glob"`
    ExcludePath []string `toml:"exclude_path"`
    Pattern     string   `toml:"pattern"`
}

// LoadConfig reads and parses .drem/constraints.toml from the given worktree root.
// Returns nil config (no error) if the file does not exist.
func LoadConfig(worktreeRoot string) (*Config, error)
```

Use `github.com/BurntSushi/toml` for parsing. The config file path is `<worktreeRoot>/.drem/constraints.toml`.

#### 2. `evaluate.go` — Constraint evaluation logic

Implement the evaluation engine:

```go
// Evaluate runs all constraints in the config against the given worktree root.
func Evaluate(cfg *Config, worktreeRoot string) (*Report, error)

// EvaluateFiles checks only file-based constraints (max_lines, max_matches, no_match)
// against a specific set of file paths. Used by the plan validator to check
// estimated_files without running command constraints. Files are relative to
// worktreeRoot.
func EvaluateFiles(cfg *Config, worktreeRoot string, files []string) (*Report, error)
```

Evaluation logic for each constraint type:

- **`command`**: Run the command via `exec.Command("bash", "-c", run)` with `Dir` set to `worktreeRoot`. Check exit code (default) or stdout emptiness (`expect = "empty_output"`). Capture stdout+stderr as violation message on failure.

- **`max_lines`**: Use `filepath.Glob` (or `doublestar` if needed for `**` patterns) to find matching files. Exclude files matching any `exclude` pattern. For each file, count lines (`bytes.Count(data, '\n')`). If a file matches an exception path with `rule = "shrink-only"`, compare against `baseline_lines` instead of `limit`. Fail if lines > limit (or > baseline for exceptions).

- **`max_matches`**: Find files via glob/exclude. Compile `pattern` as a regexp. For `scope = "file"` (default): count matches per file, fail if > limit. For `scope = "directory"`: count distinct matches across all files in each directory, fail if > limit for that directory. Exception handling same as max_lines but using `baseline_count`.

- **`no_match`**: Find files via glob. Exclude files whose path starts with any `exclude_path` entry. Compile `pattern` as a regexp. Any match in any file is a failure. Report the file path and matching line.

**Important**: For `**` glob patterns (e.g., `internal/**/*.go`), the standard `filepath.Glob` does not support `**`. Use `filepath.WalkDir` with manual pattern matching, or use the `github.com/bmatcuk/doublestar/v4` library. Check `go.mod` first — if it's not present, implement `**` matching with `filepath.WalkDir` and `filepath.Match` on the filename part, filtering directory prefixes manually. Keep it simple.

**Shrink-only evaluation**: For files matching a `shrink-only` exception:
- The regular `limit` is ignored for that file.
- Instead, compare the current metric against `baseline_lines` or `baseline_count`.
- If current > baseline, fail with message like "file X has Y lines, exceeds shrink-only baseline of Z".
- If current <= baseline, pass (file is within or shrinking from baseline).

#### 3. `report.go` — Result types and formatting

```go
// Result is the outcome of evaluating a single constraint.
type Result struct {
    Name     string
    Type     string   // "command", "max_lines", "max_matches", "no_match"
    Passed   bool
    Messages []string // violation details
}

// Report is the aggregate outcome of evaluating all constraints.
type Report struct {
    Results []Result
    Passed  int
    Failed  int
}

// FormatReport renders a Report as human-readable text.
// Format matches check_constitution.sh output:
//   PASS: <name>
//   FAIL: <name>
//     <violation detail>
//   ──────────────────────────
//   N checks passed, M failed
func FormatReport(report *Report) string
```

#### 4. `config_test.go` — Tests for TOML parsing

Table-driven tests:
- Parse a valid constraints.toml with all constraint types and verify all fields.
- Parse a config with exceptions and verify exception fields.
- Parse a config with missing optional fields (expect, scope) and verify defaults.
- LoadConfig on a nonexistent file returns nil config, no error.
- Parse an invalid TOML file returns an error.

#### 5. `evaluate_test.go` — Tests for constraint evaluation

Use `t.TempDir()` to create test worktrees with known file contents.

Table-driven tests for each constraint type:

**max_lines**:
- File under limit: pass.
- File over limit: fail with message containing file path and line count.
- File matching exclude pattern: skipped.
- File matching shrink-only exception under baseline: pass.
- File matching shrink-only exception over baseline: fail.

**max_matches**:
- File with matches under limit (scope=file): pass.
- File with matches over limit: fail.
- Scope=directory: aggregates across files in same directory.
- Shrink-only exception under baseline: pass.
- Shrink-only exception over baseline: fail.

**no_match**:
- File with no matches: pass.
- File with a match: fail with file path and matching line.
- File under exclude_path: skipped.

**command**:
- Command exits 0: pass.
- Command exits non-zero: fail.
- Command with `expect = "empty_output"` and empty stdout: pass.
- Command with `expect = "empty_output"` and non-empty stdout: fail.

**EvaluateFiles**:
- Only evaluates file-based constraints (no commands).
- Only checks the specified files, not all files matching the glob.

**FormatReport**:
- Format output matches expected PASS/FAIL/summary format.

### New file: `.drem/constraints.toml`

Create the drem-orchestrator constraint definitions. Migrate every check from `scripts/check_constitution.sh` into this TOML format.

```toml
# Project-agnostic constraint definitions for drem-orchestrator.
# The orchestrator reads this file to enforce quality constraints at:
# - Plan validation (before plan_review)
# - Post-agent checks (before merge into integration)
# - Integration gate (before testing_ready)

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
pattern = "^func [A-Z]"
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
pattern = "\".*internal/"
limit   = 6
scope   = "directory"

[[max_matches.exception]]
path           = "internal/orchestrator/"
rule           = "shrink-only"
baseline_count = 8

[[max_matches]]
name    = "GORM hook consolidation"
glob    = "internal/model/models.go"
pattern = "func.*BeforeCreate"
limit   = 1

# ── Forbidden Patterns ───────────────────────────────────────

[[no_match]]
name         = "DB init outside testutil"
glob         = "internal/**/*_test.go"
exclude_path = ["internal/testutil/"]
pattern      = "gorm\\.Open\\(sqlite"

[[no_match]]
name         = "Git helpers outside testutil"
glob         = "internal/**/*_test.go"
exclude_path = ["internal/testutil/"]
pattern      = "func setupBareRepo|func initBareRepo|func addWorktree|func commitFile"

[[no_match]]
name         = "Test factories outside testutil"
glob         = "internal/**/*_test.go"
exclude_path = ["internal/testutil/"]
pattern      = "func createTest|func newTest|func mockTestDB|func lifecycleTestDB"
```

Verify the current baseline values are accurate by checking the actual file:
- `wc -l internal/orchestrator/orchestrator.go` for `baseline_lines`
- `grep -c '^func [A-Z]' internal/orchestrator/orchestrator.go` for exported function `baseline_count`
- Count distinct `internal/` imports across non-test files in `internal/orchestrator/` for import `baseline_count`

Update the baselines in the TOML if the actual values differ from the ones above.

## Scope Limitation

- Do NOT modify any existing source files outside `internal/constraints/`.
- Do NOT modify `scripts/check_constitution.sh` — that's a later task.
- Do NOT modify `internal/prompt/`, `internal/orchestrator/`, or any other existing package.
- The `.drem/` directory is new — create it at the repo root.

## Conventions

- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./internal/constraints/...`
