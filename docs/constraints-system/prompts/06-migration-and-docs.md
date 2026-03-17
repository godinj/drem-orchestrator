# Agent: Migration and Documentation

You are working on the `master` branch of Drem Orchestrator, a Go-based agent orchestration system.
Your task is to replace the hand-maintained `check_constitution.sh` with a thin wrapper that uses the Go constraint runner, update `ARCHITECTURE.md` to reference the new system, and update `README.md` documentation.

## Context

Read these before starting:
- `docs/constraints-system/design.md` (section 4.7 — Replace check_constitution.sh)
- `scripts/check_constitution.sh` (the current script — understand every check it performs)
- `.drem/constraints.toml` (the TOML config that replaces the bash checks — verify every check from the script has a corresponding constraint)
- `ARCHITECTURE.md` (the Graduation Path section at the end — update to reference the constraint system)
- `internal/constraints/evaluate.go` (`Evaluate` function)
- `internal/constraints/report.go` (`FormatReport` function)

## Dependencies

This agent depends on all previous agents (01-05). The `internal/constraints/` package must be complete, `.drem/constraints.toml` must exist, and all integration points must be wired.

## Deliverables

### Modified file: `scripts/check_constitution.sh`

Replace the entire script with a thin wrapper that:

1. Loads and evaluates constraints from `.drem/constraints.toml` using the Go runner.
2. Preserves the same exit code behavior (0 = all pass, 1 = failures).
3. Preserves human-readable output format.

```bash
#!/usr/bin/env bash
set -euo pipefail

# Constitution enforcement via the Go constraint runner.
# This script is a thin wrapper around the constraints package.
# The actual rules are defined in .drem/constraints.toml.
#
# Usage: bash scripts/check_constitution.sh

cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"

go run ./cmd/check-constraints/...
```

### New file: `cmd/check-constraints/main.go`

A minimal CLI that loads and evaluates constraints:

```go
package main

import (
    "fmt"
    "os"

    "github.com/godinj/drem-orchestrator/internal/constraints"
)

func main() {
    // Use current directory as worktree root.
    wd, err := os.Getwd()
    if err != nil {
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        os.Exit(1)
    }

    cfg, err := constraints.LoadConfig(wd)
    if err != nil {
        fmt.Fprintf(os.Stderr, "error loading constraints: %v\n", err)
        os.Exit(1)
    }
    if cfg == nil {
        fmt.Println("No .drem/constraints.toml found, nothing to check.")
        os.Exit(0)
    }

    report, err := constraints.Evaluate(cfg, wd)
    if err != nil {
        fmt.Fprintf(os.Stderr, "error evaluating constraints: %v\n", err)
        os.Exit(1)
    }

    fmt.Print(constraints.FormatReport(report))

    if report.Failed > 0 {
        os.Exit(1)
    }
}
```

### Modified file: `ARCHITECTURE.md`

#### 1. Update the Graduation Path section

Replace the current Graduation Path section (at the end of the file) with:

```markdown
## Graduation Path

When a constitution rule can be reliably detected:

1. Add the constraint to `.drem/constraints.toml` using the appropriate type
   (`command`, `max_lines`, `max_matches`, or `no_match`)
2. Mark the rule in this document as `[enforced]`
3. The constraint system automatically enforces the rule at:
   - **Plan validation** — warns when plans target constrained files
   - **Post-agent gate** — checks file-based constraints after each agent merge
   - **Integration gate** — runs all constraints before testing_ready
4. Run manually: `bash scripts/check_constitution.sh`

The rule stays in this document for context, but `.drem/constraints.toml` is now
the authoritative enforcement definition.
```

#### 2. Update enforcement status markers

For each rule that has a corresponding entry in `.drem/constraints.toml`, change
`[not yet enforced]` to `[enforced]`. Based on the current constraints.toml, these rules should be marked as enforced:

- gofmt compliance → enforced (command constraint)
- File length ceiling → enforced (max_lines constraint)
- Function count ceiling → enforced (max_matches constraint)
- Package import ceiling → enforced (max_matches with scope=directory)
- No duplicate GORM hooks → enforced (max_matches with limit=1)
- testutil is the single source → enforced (no_match constraints)
- Test factory functions in testutil → enforced (no_match constraint)

Leave rules that don't have TOML equivalents (like "Search before creating", "Interfaces at consumption sites", "No bare numeric literals", "Minimize real I/O in unit tests") as `[not yet enforced]`.

### Verification

After making all changes, run the full test suite and the new check script:

```bash
# Verify the Go constraint runner works
go build ./cmd/check-constraints/...
go run ./cmd/check-constraints/...

# Verify the wrapper script works
bash scripts/check_constitution.sh

# Verify all tests pass
go test ./...

# Verify the output format is similar to the old script
# (PASS/FAIL lines with summary footer)
```

Compare the output of the new `check_constitution.sh` with the old version to ensure no checks were lost. Every PASS/FAIL from the old script should have a corresponding line in the new output.

## Scope Limitation

- Modify `scripts/check_constitution.sh`, `ARCHITECTURE.md`.
- Create `cmd/check-constraints/main.go`.
- Do NOT modify `internal/constraints/`, `internal/orchestrator/`, `internal/prompt/`, or other packages.
- Do NOT modify `.drem/constraints.toml` — if a check is missing, document it as a follow-up rather than adding it.

## Conventions

- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Build verification: `go build ./... && go test ./...`
