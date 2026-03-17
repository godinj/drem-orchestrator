# Agent: Missing Prompt Package Tests

You are working on the `master` branch of drem-orchestrator, a Go TUI tool that orchestrates Claude Code agents via tmux.
Your task is to add tests for the untested functions in `internal/prompt`, raising coverage from 42.4% toward ~65%.

## Context

Read these before starting:
- `CLAUDE.md` (build commands, conventions)
- `docs/test-coverage-overhaul/prd-test-coverage.md` (Phase 1b section)
- `internal/prompt/prompt.go` (source — 727 LOC)
- `internal/prompt/prompt_test.go` (existing tests — 22 test functions already exist)

The following functions are already tested (do NOT duplicate):
- `plannerInstructions` (9 tests)
- `planReviewerInstructions` (2 tests)
- `coderInstructions` — test phase, impl phase, default phase, integration phase (9 tests)
- `featureReviewerInstructions` (1 test)

The following functions have NO tests:
- `researcherInstructions()` — returns `[]string` of researcher guidance
- `fixerInstructions(opts Opts)` — returns `[]string` of fixer directives
- `defaultInstructions()` — returns `[]string` of fallback instructions
- `readBuildCommands(worktreePath string) string` — reads filesystem to detect build system
- `Generate(opts Opts) string` — top-level composition function

## Deliverables

### Modified files

#### 1. `internal/prompt/prompt_test.go` (append ~100–120 LOC)

Add test functions following the existing pattern in the file (string content assertions with `strings.Contains`).

```go
func TestResearcherInstructions(t *testing.T)
```
- Call `researcherInstructions()`, verify result is non-empty
- Verify it contains research-specific guidance (check for key phrases like "research", "investigate", or similar terms found in the actual function output)

```go
func TestFixerInstructions(t *testing.T)
```
- Call `fixerInstructions(opts)` with a populated Opts struct
- Verify result is non-empty
- Verify it contains fix-specific directives (check for key phrases about fixing, errors, or failures)
- Verify it includes context from opts (e.g., task title or error details if the function uses them)

```go
func TestDefaultInstructions(t *testing.T)
```
- Call `defaultInstructions()`, verify result is non-empty
- Verify it provides generic fallback guidance

```go
func TestReadBuildCommands(t *testing.T)
```
Table-driven test using `t.TempDir()` for filesystem setup:
- Directory with `go.mod` → result contains "go" build command
- Directory with `Makefile` → result contains "make"
- Directory with `package.json` → result contains "npm" or "node"
- Empty directory → returns empty string
- Nonexistent directory → returns empty string (no panic)

```go
func TestGenerate_Planner(t *testing.T)
```
- Call `Generate(Opts{AgentType: model.AgentTypePlanner, ...})` with minimal required fields
- Verify result is non-empty string
- Verify it contains planner-specific content (from `plannerInstructions()`)

```go
func TestGenerate_Coder(t *testing.T)
```
- Call `Generate(Opts{AgentType: model.AgentTypeCoder, ...})` with minimal required fields
- Verify result is non-empty string
- Verify it contains coder-specific content

```go
func TestGenerate_Researcher(t *testing.T)
```
- Call `Generate(Opts{AgentType: model.AgentTypeResearcher, ...})` with minimal required fields
- Verify result contains researcher-specific content

## Scope Limitation

- Only modify `internal/prompt/prompt_test.go`
- Do NOT modify `prompt.go`
- Do NOT duplicate existing tests — read the existing test functions first
- Match the assertion style already used in the file (likely `strings.Contains` checks)

## Verification

```bash
go test ./internal/prompt/ -v -cover
```

Target: coverage should reach ~65%. All existing and new tests must pass.

## Conventions

- `gofmt` for formatting
- Table-driven tests with `t.Run(tc.name, ...)`
- `t.Helper()` on all test helper functions
- Match the style of existing tests in `prompt_test.go`
