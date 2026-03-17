# Agent: Merge Helper Unit Tests

You are working on the `master` branch of drem-orchestrator, a Go TUI tool that orchestrates Claude Code agents via tmux.
Your task is to add unit tests for the two exported pure helper functions in `internal/merge/merge.go`: `Intersect()` and `DetectBuildCommand()`.

## Context

Read these before starting:
- `CLAUDE.md` (build commands, conventions)
- `internal/merge/merge.go` (lines 490-529 — `DetectBuildCommand` and `Intersect` implementations)
- `internal/merge/merge_test.go` (existing tests — append to this file, do NOT modify existing tests)

## Deliverables

### Modified file

#### 1. `internal/merge/merge_test.go`

Append new test functions to the existing file. Do NOT modify any existing tests or helper types.

**Tests for `Intersect`:**

```go
func TestIntersect(t *testing.T)
```

Table-driven test with these cases:
1. **overlapping** — `a=["x","y","z"]`, `b=["y","z","w"]` → `["y","z"]`
2. **no overlap** — `a=["a","b"]`, `b=["c","d"]` → `nil` (or empty)
3. **empty a** — `a=[]`, `b=["x"]` → `nil`
4. **empty b** — `a=["x"]`, `b=[]` → `nil`
5. **both empty** — `a=[]`, `b=[]` → `nil`
6. **identical** — `a=["a","b"]`, `b=["a","b"]` → `["a","b"]`

**Tests for `DetectBuildCommand`:**

```go
func TestDetectBuildCommand(t *testing.T)
```

Table-driven test using `t.TempDir()` to create temporary directories with marker files:
1. **go.mod present** — create `go.mod` file → expect `cmd="go"`, `args=["test","./..."]`
2. **Makefile present** — create `Makefile` → expect `cmd="make"`, `args=["test"]`
3. **package.json present** — create `package.json` → expect `cmd="npm"`, `args=["test"]`
4. **nothing present** — empty dir → expect `cmd=""`, `args=nil`
5. **go.mod takes priority** — create both `go.mod` and `Makefile` → expect `cmd="go"` (go.mod is checked first)

For each marker file, an empty file is sufficient (`os.WriteFile(path, nil, 0o644)`).

Note: skip `pyproject.toml` testing since it depends on whether `uv` is installed on the test machine.

## Scope Limitation

- Do NOT modify any existing tests or types in `merge_test.go`
- Do NOT modify `merge.go`
- Do NOT add test helpers to `testutil`
- Only append new test functions at the end of the file

## Verification

```bash
go build ./internal/merge/
go test ./internal/merge/ -v -run "TestIntersect|TestDetectBuildCommand"
go test ./internal/merge/ -v
go test ./...
```

## Conventions

- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Table-driven tests with `t.Run` subtests
- Use `t.TempDir()` for filesystem operations
- Use `t.Helper()` in any helper functions
