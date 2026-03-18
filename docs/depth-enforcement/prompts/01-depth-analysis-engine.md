# Agent: Depth Analysis Engine

You are working on the `master` branch of drem-orchestrator, a Go-based multi-agent orchestrator with TDD enforcement and constitution-based quality constraints.
Your task is to build the depth analysis engine: a new `internal/constraints/depth/` sub-package that computes module depth metrics for Go packages.

## Context

Read these specs before starting:
- `docs/depth-enforcement-prd.md` (Three Static Depth Heuristics, Module Structure — `internal/constraints/depth/`)
- `internal/constraints/config.go` (existing constraint types and config patterns)
- `internal/constraints/evaluate.go` (existing evaluation patterns, glob helpers, file traversal)
- `internal/constraints/report.go` (Result and Report types)

## Deliverables

### New files (internal/constraints/depth/)

#### 1. depth.go

Core depth analysis engine. Public interface: a single `Analyze` function that takes a package directory and returns a depth report.

- `type DepthReport struct` — aggregate depth metrics for a single Go package:
  - `ExportRatio float64` — exported symbols / total LOC (non-test `.go` files only)
  - `ExportedSymbols int` — count of exported functions, types, vars, consts
  - `TotalLOC int` — total lines of code (non-test `.go` files)
  - `PassThroughFuncs []PassThrough` — functions that delegate to another package without adding logic
  - `PackagePath string` — the analyzed package path (relative to worktree root)
- `type PassThrough struct` — a single pass-through function:
  - `FuncName string` — name of the function
  - `DelegatePkg string` — the package it delegates to
  - `File string` — source file containing the function
  - `Line int` — line number
- `func Analyze(worktreeRoot, pkgPath string) (*DepthReport, error)` — analyze a single Go package directory. `pkgPath` is relative to `worktreeRoot` (e.g., `"internal/orchestrator"`). Must:
  - Parse all non-test `.go` files in the directory using `go/ast` and `go/parser`
  - Count exported symbols (functions, types, vars, consts with uppercase first letter)
  - Count total lines of code
  - Compute export ratio: `exportedSymbols / totalLOC`
  - Detect pass-through functions: functions whose body is a single return statement or expression statement that calls a function from a different package, with arguments that are just the function's own parameters (possibly reordered) or literals — no additional computation
  - Skip `_test.go` files entirely (per PRD user story 17)
- `func AnalyzeAll(worktreeRoot string, pkgPaths []string) (map[string]*DepthReport, error)` — convenience wrapper that calls `Analyze` for each package path and returns a map keyed by package path

#### 2. growth.go

Interface growth rate detection. Compares depth metrics between two states to detect when a package's exported surface grows faster than its implementation.

- `type GrowthReport struct`:
  - `PackagePath string`
  - `OldExportedSymbols int`
  - `NewExportedSymbols int`
  - `OldTotalLOC int`
  - `NewTotalLOC int`
  - `ExportGrowthRate float64` — percentage change in exported symbols
  - `LOCGrowthRate float64` — percentage change in LOC
  - `Disproportionate bool` — true if export growth rate > LOC growth rate
- `func CompareGrowth(old, new *DepthReport) *GrowthReport` — compare two depth reports for the same package and flag disproportionate interface growth

#### 3. depth_test.go

Tests for the depth analysis engine using fixture Go packages.

- Create fixture packages as temp directories with known depth characteristics:
  - **Deep module fixture**: many lines of code, few exported symbols, no pass-throughs (e.g., 200 LOC, 3 exports → low ratio)
  - **Shallow module fixture**: few lines of code, many exported symbols (e.g., 50 LOC, 15 exports → high ratio)
  - **Pass-through fixture**: functions that just delegate to another package (single return statement calling an imported function with the same args)
  - **Mixed fixture**: some pass-throughs, some real logic
  - **Test file exclusion fixture**: package with `_test.go` files containing exported symbols — verify these are excluded from metrics
- Table-driven tests using `t.TempDir()` for fixture packages
- Test `Analyze` returns correct `ExportRatio`, `ExportedSymbols`, `TotalLOC`, and `PassThroughFuncs`
- Test `CompareGrowth` correctly flags disproportionate growth
- Test `AnalyzeAll` processes multiple packages

Fixture Go files must be syntactically valid so that `go/parser` can parse them. They do NOT need to compile (imports can reference fake packages). Example fixture for pass-through detection:

```go
package mypkg

import "other/pkg"

func ForwardCall(a int, b string) error {
    return pkg.DoThing(a, b)
}

func RealLogic(a int) int {
    result := a * 2
    if result > 100 {
        result = 100
    }
    return result
}
```

## Scope Limitation

- Do NOT modify any existing files outside `internal/constraints/depth/`
- Do NOT wire depth checks into the parent `constraints/` package — that is a separate agent's work
- Do NOT add entries to `.drem/constraints.toml`
- Do NOT implement interface growth rate tracking across git diffs — `CompareGrowth` compares two in-memory `DepthReport` values only

## Conventions

- Package: `package depth`
- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Table-driven tests with `t.Run()` sub-tests
- Use `go/ast`, `go/parser`, `go/token` for AST analysis
- Build verification: `go build ./internal/constraints/depth/ && go test ./internal/constraints/depth/ -v`
