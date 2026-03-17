# Agent: Prompt Helper Unit Tests

You are working on the `master` branch of drem-orchestrator, a Go TUI tool that orchestrates Claude Code agents via tmux.
Your task is to add unit tests for untested prompt generation functions in `internal/prompt/prompt.go`.

## Context

Read these before starting:
- `CLAUDE.md` (build commands, conventions)
- `internal/prompt/prompt.go` (function signatures and implementations — especially `researcherInstructions()` at line 523, `fixerInstructions()` at line 672, `defaultInstructions()` at line 714, `readBuildCommands()` at line 726)
- `internal/prompt/prompt_test.go` (existing tests — append to this file, do NOT modify existing tests)

## Deliverables

### Modified file

#### 1. `internal/prompt/prompt_test.go`

Append new test functions to the existing file. Do NOT modify any existing tests or helpers.

**Tests for `researcherInstructions()`:**

```go
func TestResearcherInstructions(t *testing.T)
```

- Verify the output contains key phrases: `"You are a researcher agent"`, `"research-report.md"`
- Verify it does NOT contain coder-specific phrases like `"commit all changes"`

**Tests for `fixerInstructions()`:**

```go
func TestFixerInstructions(t *testing.T)
```

Uses `minimalOpts()` (already defined in the test file). Set `opts.AgentType = model.AgentFixer` and populate `opts.Diagnosis`, `opts.AffectedFiles`, `opts.SuggestedFix`.

- Verify the output contains `"You are a fixer agent"`
- Verify with `Diagnosis` set → output contains the diagnosis string
- Verify with `AffectedFiles` set → output contains the file names
- Verify with `SuggestedFix` set → output contains the fix text
- Already tested: empty diagnosis, empty affected files, empty suggested fix (tests exist at lines 591-633)

**Tests for `defaultInstructions()`:**

```go
func TestDefaultInstructions(t *testing.T)
```

- Verify the output contains `"Complete the task as described above"` (this is the fallback)
- Already tested indirectly by `TestGenerate_DefaultAgentDispatch` at line 579, but add an explicit direct test

**Tests for `readBuildCommands()` with real directories:**

```go
func TestReadBuildCommands(t *testing.T)
```

Table-driven test using `t.TempDir()`:
1. **go.mod present** — create a `go.mod` file and `CLAUDE.md` with build commands → verify output contains build command content
2. **CLAUDE.md present** — create a `CLAUDE.md` with `## Build` section → verify it's extracted
3. **no build files** — empty dir → verify empty string returned
4. **already tested:** empty path and nonexistent path (lines 707-719)

Note: `readBuildCommands()` reads `CLAUDE.md` from the worktree path and extracts build-related sections. Examine the implementation at line 726 before writing tests to match actual behavior.

## Scope Limitation

- Do NOT modify any existing tests or helper functions in `prompt_test.go`
- Do NOT modify `prompt.go`
- Do NOT add test helpers to `testutil`
- Only append new test functions at the end of the file

## Verification

```bash
go build ./internal/prompt/
go test ./internal/prompt/ -v -run "TestResearcherInstructions|TestFixerInstructions|TestDefaultInstructions|TestReadBuildCommands"
go test ./internal/prompt/ -v
go test ./...
```

## Conventions

- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Table-driven tests with `t.Run` subtests where appropriate
- Use `t.TempDir()` for filesystem operations
- Reuse the existing `minimalOpts()` helper (defined at line 103 of `prompt_test.go`) for `Opts` construction
