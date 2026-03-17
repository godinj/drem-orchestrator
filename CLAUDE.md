# Fix MainWorktreePath: allow bare repo root as main worktree

## Mission

Fix the `MainWorktreePath()` method in `internal/worktree/manager.go` so that it recognizes the bare repo root as a valid main worktree when the default branch is checked out there. Currently the merge step fails for any project where the repo root serves as the main working tree.

## Build & Run

```bash
go build -o drem ./cmd/drem
go test ./...
```

## What to Implement

1. **Remove the bare-repo skip** in `MainWorktreePath()` (lines 98-101 of `internal/worktree/manager.go`)
   - The 4-line block that skips entries matching `m.BareRepoPath` is unnecessary — truly bare repos emit `bare` instead of `branch refs/heads/<name>`, so the branch-matching condition never fires for them.

2. **Add 3 tests** to `internal/worktree/worktree_test.go`:
   - `TestMainWorktreePath_BareRepoIsMainWorktree` — non-bare repo where root IS the worktree
   - `TestMainWorktreePath_LinkedWorktree` — bare repo with a linked main worktree (existing case)
   - `TestMainWorktreePath_NotFound` — error case with nonexistent branch

## Key Files

| File | Change |
|------|--------|
| `internal/worktree/manager.go` | Remove 4-line bare-repo skip in `MainWorktreePath()` |
| `internal/worktree/worktree_test.go` | Add 3 new tests |

## Verification

```bash
go build ./internal/worktree/
go test ./internal/worktree/ -v -run TestMainWorktreePath
go test ./internal/worktree/ -v
go test ./...
```

## Scope Limitation

- Do NOT modify any other methods in manager.go
- Do NOT change function signatures
- Do NOT modify existing tests
- Do NOT add test helpers to testutil

## Conventions

- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Use `context.Context` for cancellation
- Table-driven tests
