# Agent: Merge Package Pure Helper Tests

You are working on the `master` branch of drem-orchestrator, a Go TUI tool that orchestrates Claude Code agents via tmux.
Your task is to export and test the private pure helper functions in `internal/merge`, raising coverage from 13.9% toward ~30%.

## Context

Read these before starting:
- `CLAUDE.md` (build commands, conventions)
- `docs/test-coverage-overhaul/prd-test-coverage.md` (Phase 1c section)
- `internal/merge/merge.go` (source — 512 LOC)
- `internal/merge/merge_test.go` (existing tests — 9 test functions for retry logic and basic merge)

The following private functions in `merge.go` are pure and should be exported for testing:
- `intersect(a, b []string) []string` — set intersection of two string slices
- `detectBuildCommand(worktreePath string) (string, []string)` — filesystem detection of build system
- `fileExists(path string) bool` — simple file existence check

## Deliverables

### Modified files

#### 1. `internal/merge/merge.go`

Export the three private pure helpers by capitalizing them:
- `intersect` → `Intersect`
- `detectBuildCommand` → `DetectBuildCommand`
- `fileExists` → `FileExists`

Update all call sites within `merge.go` to use the new capitalized names.

Add doc comments to each:

```go
// Intersect returns elements present in both slices a and b.
func Intersect(a, b []string) []string

// DetectBuildCommand inspects the worktree path for known build system files
// and returns the command and arguments to run a build.
func DetectBuildCommand(worktreePath string) (string, []string)

// FileExists reports whether the named file exists.
func FileExists(path string) bool
```

#### 2. `internal/merge/merge_test.go` (append ~60–80 LOC)

Add test functions for the newly-exported helpers.

```go
func TestIntersect(t *testing.T)
```
Table-driven:
- `["a","b","c"]` ∩ `["b","c","d"]` → `["b","c"]`
- `["a","b"]` ∩ `["c","d"]` → `[]` (empty, no overlap)
- `[]` ∩ `["a","b"]` → `[]` (empty input)
- `["a","b"]` ∩ `[]` → `[]` (empty input)
- `[]` ∩ `[]` → `[]` (both empty)
- `["a"]` ∩ `["a"]` → `["a"]` (single element match)

```go
func TestDetectBuildCommand(t *testing.T)
```
Table-driven using `t.TempDir()` to create directories with specific files:
- Directory with `go.mod` → returns go build command
- Directory with `Makefile` → returns make command
- Directory with `package.json` → returns npm/node command
- Directory with `CMakeLists.txt` → returns cmake command (if supported)
- Empty directory → returns empty command
- Read the actual `DetectBuildCommand` implementation to verify the exact return values for each case

```go
func TestFileExists(t *testing.T)
```
- Existing file → true
- Nonexistent file → false
- Existing directory → true (or false, match actual behavior)

## Scope Limitation

- Only modify files in `internal/merge/`
- Only export the three listed functions — do NOT change function logic
- Do NOT modify or duplicate existing test functions
- If exporting a function requires changing its call sites in merge.go, update them

## Verification

```bash
go build ./...
go test ./internal/merge/ -v -cover
```

All existing and new tests must pass. Coverage should increase from 13.9%.

## Conventions

- `gofmt` for formatting
- Exported functions have doc comments
- Table-driven tests with `t.Run(tc.name, ...)`
