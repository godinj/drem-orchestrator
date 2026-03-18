# Agent: Constraints Depth Integration

You are working on the `master` branch of drem-orchestrator, a Go-based multi-agent orchestrator with TDD enforcement and constitution-based quality constraints.
Your task is to wire depth checks into the constitution checking pipeline and add depth constraint definitions to `.drem/constraints.toml`.

## Context

Read these specs before starting:
- `docs/depth-enforcement-prd.md` (Constraint Configuration, Three Static Depth Heuristics, User stories 1, 11-17)
- `internal/constraints/config.go` (Config struct, constraint types, LoadConfig, TOML tags)
- `internal/constraints/evaluate.go` (Evaluate, EvaluateFiles, evalFileConstraints, globFiles)
- `internal/constraints/report.go` (Result, Report, FormatReport)
- `internal/constraints/depth/depth.go` (DepthReport, Analyze — created by Agent 01)
- `internal/constraints/depth/growth.go` (GrowthReport, CompareGrowth — created by Agent 01)
- `.drem/constraints.toml` (existing constraint format and entries)

## Dependencies

This agent depends on Agent 01 (depth-analysis-engine). If `internal/constraints/depth/` doesn't exist yet, create stub files with these interfaces and implement against them:

```go
// internal/constraints/depth/depth.go
package depth

type DepthReport struct {
    ExportRatio      float64
    ExportedSymbols  int
    TotalLOC         int
    PassThroughFuncs []PassThrough
    PackagePath      string
}

type PassThrough struct {
    FuncName    string
    DelegatePkg string
    File        string
    Line        int
}

func Analyze(worktreeRoot, pkgPath string) (*DepthReport, error) { panic("stub") }
func AnalyzeAll(worktreeRoot string, pkgPaths []string) (map[string]*DepthReport, error) { panic("stub") }
```

## Deliverables

### Migration (internal/constraints/)

#### 1. config.go

Add a new depth constraint type to Config and its supporting types:

```go
// Add to Config struct:
type Config struct {
    ContextFiles []string               `toml:"context_files"`
    Commands     []CommandConstraint    `toml:"command"`
    MaxLines     []MaxLinesConstraint   `toml:"max_lines"`
    MaxMatches   []MaxMatchesConstraint `toml:"max_matches"`
    NoMatch      []NoMatchConstraint    `toml:"no_match"`
    Depth        []DepthConstraint      `toml:"depth"`       // NEW
}

// New constraint type:

// DepthConstraint enforces module depth heuristics on Go packages.
type DepthConstraint struct {
    Name             string           `toml:"name"`
    Glob             string           `toml:"glob"`             // glob to find Go packages (matches directories)
    Exclude          []string         `toml:"exclude"`          // patterns to exclude
    MaxExportRatio   float64          `toml:"max_export_ratio"` // ceiling for exported symbols / total LOC
    MaxPassThroughs  int              `toml:"max_pass_throughs"`// max pass-through functions per package
    Exceptions       []DepthException `toml:"exception"`
}

// DepthException grandfathers a package for depth constraints.
type DepthException struct {
    Path              string  `toml:"path"`
    Rule              string  `toml:"rule"`               // "grandfathered"
    BaselineRatio     float64 `toml:"baseline_ratio"`     // current export ratio (shrink-only)
    BaselinePassThrus int     `toml:"baseline_pass_thrus"`// current pass-through count (shrink-only)
}
```

#### 2. evaluate.go

Add depth constraint evaluation to `evalFileConstraints`:

- Add `evalDepth(c DepthConstraint, worktreeRoot string, fileSet map[string]bool) (Result, error)`:
  - Use `globFiles` to find Go source files matching the constraint's glob
  - Group files by directory to identify package paths
  - Skip packages that only have `_test.go` files
  - If `fileSet` is non-nil (plan validation mode), only evaluate packages that contain at least one file in `fileSet`
  - Call `depth.Analyze(worktreeRoot, pkgPath)` for each package
  - Check `DepthReport.ExportRatio` against `MaxExportRatio` (respecting exceptions)
  - Check `len(DepthReport.PassThroughFuncs)` against `MaxPassThroughs` (respecting exceptions)
  - For grandfathered exceptions: use shrink-only semantics (current value must not exceed baseline)
  - Return a `Result` with violation messages listing package path, actual value, and limit
- Wire `evalDepth` into `evalFileConstraints` by iterating `cfg.Depth` alongside existing constraint types

#### 3. evaluate_test.go (or depth_constraint_test.go)

Test the depth constraint evaluation:

- Create fixture Go packages in `t.TempDir()` with known depth characteristics
- Test export ratio violation is detected (package with ratio above ceiling)
- Test export ratio within limit passes
- Test pass-through violation is detected
- Test grandfathered exception allows existing violations but rejects growth
- Test `_test.go` files are excluded from analysis
- Test plan validation mode (fileSet) only evaluates relevant packages
- Test packages with no non-test Go files are skipped

### Migration (.drem/)

#### 4. constraints.toml

Add depth constraint entries after the existing `# ── Forbidden Patterns ──` section:

```toml
# ── Module Depth ─────────────────────────────────────────────

[[depth]]
name              = "Export ratio ceiling"
glob              = "internal/**/*.go"
exclude           = ["*_test.go"]
max_export_ratio  = 0.15
max_pass_throughs = 3

[[depth.exception]]
path               = "internal/tui/"
rule               = "grandfathered"
baseline_ratio     = 1.0
baseline_pass_thrus = 100
```

Note: `tui/` is grandfathered with generous baselines (per PRD user story 12). `orchestrator/` is deliberately NOT grandfathered (per PRD user story 13) — it must conform or be refactored.

The specific `max_export_ratio` and `max_pass_throughs` values (0.15 and 3) are initial estimates. The PRD notes these may need tuning after rollout.

## Scope Limitation

- Do NOT modify `internal/constraints/depth/` — that package is owned by Agent 01
- Do NOT modify `internal/orchestrator/` — wiring is a separate agent's work
- Do NOT implement interface growth rate as a constraint — growth rate comparison requires git diff context that isn't available in the constraint evaluation pipeline. The `CompareGrowth` function exists for supervisor use, not constraint use.
- Do NOT modify `report.go` — the existing `Result` and `Report` types are sufficient

## Conventions

- Package: `package constraints`
- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Exported types have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Table-driven tests with `t.Run()` sub-tests and `t.TempDir()`
- Build verification: `go build ./internal/constraints/... && go test ./internal/constraints/... -v`
