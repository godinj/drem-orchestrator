# Drem Orchestrator (Go)

## Build & Run

```bash
go build -o drem ./cmd/drem
./drem --repo /path/to/bare-repo.git
```

## Test

```bash
go test ./...
```

## Architecture

- Go 1.22+, Bubble Tea TUI, GORM + SQLite, tmux
- `cmd/drem/` — entry point and config
- `internal/model/` — GORM models, enums, JSON types
- `internal/db/` — database init and migrations
- `internal/state/` — task status state machine
- `internal/worktree/` — git worktree management
- `internal/agent/` — agent process lifecycle via tmux
- `internal/tmux/` — tmux session/window management
- `internal/orchestrator/` — main scheduling loop
- `internal/prompt/` — agent prompt generation
- `internal/merge/` — merge orchestration
- `internal/memory/` — agent memory persistence
- `internal/tui/` — Bubble Tea dashboard

## Conventions

- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Use `context.Context` for cancellation
- Table-driven tests
