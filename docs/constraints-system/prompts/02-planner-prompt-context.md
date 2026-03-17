# Agent: Planner Prompt Context Files

You are working on the `master` branch of Drem Orchestrator, a Go-based agent orchestration system.
Your task is to modify the prompt generation system so that planner and coder agents receive architectural context files defined in `.drem/constraints.toml`.

## Context

Read these before starting:
- `docs/constraints-system/design.md` (section 4.3 — Planner Prompt Integration)
- `internal/prompt/prompt.go` (the full file — understand how `Generate()` builds prompts and how `readBuildCommands()` reads from the worktree)
- `internal/constraints/config.go` (the `LoadConfig` function and `Config` type — you will call this)

## Dependencies

This agent depends on Agent 01 (constraints-package). The `internal/constraints/` package must exist with at least `LoadConfig(worktreeRoot string) (*Config, error)` and the `Config` struct with `ContextFiles []string`.

If `internal/constraints/config.go` doesn't exist yet, create a minimal stub:

```go
package constraints

type Config struct {
    ContextFiles []string `toml:"context_files"`
}

func LoadConfig(worktreeRoot string) (*Config, error) {
    return nil, nil
}
```

## Deliverables

### Modified file: `internal/prompt/prompt.go`

#### 1. Add a new function `readContextFiles`

```go
// readContextFiles reads the context files specified in .drem/constraints.toml
// and returns their contents as a formatted markdown section. Returns an empty
// string if no config exists or no context files are specified.
func readContextFiles(worktreePath string) string
```

Implementation:
1. Call `constraints.LoadConfig(worktreePath)`.
2. If config is nil or `ContextFiles` is empty, return `""`.
3. For each path in `ContextFiles`:
   - Resolve relative to `worktreePath`.
   - Read the file with `os.ReadFile`.
   - Skip files that don't exist (log nothing — absence is normal for some worktrees).
4. Build a markdown section:
   ```
   ## Architecture & Constraints

   The following project architecture constraints apply to your work.
   Respect file length ceilings, shrink-only rules for grandfathered files,
   and all other structural limits described below.

   ### <filename>

   <file contents>
   ```
5. Return the assembled string.

#### 2. Include context files in the `Generate` function

In `Generate()`, after the build commands section (which calls `readBuildCommands`), add a call to `readContextFiles(opts.WorktreePath)` and append the result to `sections` if non-empty.

The context files section should appear for **all agent types** (planner, coder, researcher, reviewer, fixer) — every agent benefits from knowing the architectural constraints.

#### 3. Add import for the constraints package

Add `"github.com/godinj/drem-orchestrator/internal/constraints"` to the import block.

### New file: `internal/prompt/prompt_context_test.go`

Test the `readContextFiles` function:

1. **With valid config and context file**: Create a temp dir with `.drem/constraints.toml` containing `context_files = ["ARCH.md"]` and an `ARCH.md` file. Call `readContextFiles`. Verify output contains the file contents and the section header.

2. **With no config file**: Call `readContextFiles` on a temp dir with no `.drem/` directory. Verify empty string returned.

3. **With config but missing context file**: Create `.drem/constraints.toml` with `context_files = ["missing.md"]`. Verify empty string returned (file skipped gracefully).

4. **With multiple context files**: Config lists two files, both exist. Verify both are included with their own `### <filename>` headers.

5. **Integration with Generate**: Create a minimal `Opts` with a `WorktreePath` pointing to a temp dir with a valid config and context file. Call `Generate()` and verify the output contains the context file contents.

For tests that need a `.drem/constraints.toml`, write a minimal valid TOML — only the `context_files` key is needed since `readContextFiles` only uses that field.

## Scope Limitation

- Only modify `internal/prompt/prompt.go` and create `internal/prompt/prompt_context_test.go`.
- Do NOT modify any files in `internal/orchestrator/`, `internal/constraints/`, or other packages.
- Do NOT change the structure of existing prompt sections — only add a new section.

## Conventions

- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./internal/prompt/...`
