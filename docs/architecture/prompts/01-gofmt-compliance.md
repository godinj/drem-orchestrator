# Agent: gofmt Compliance Fix

You are working on the `master` branch of Drem Orchestrator, a Go-based agent orchestration system.
Your task is to fix all `gofmt` formatting drift across the codebase.

## Context

Read these before starting:
- `ARCHITECTURE.md` (Formatting section — gofmt compliance rule)
- `CLAUDE.md` (Conventions — `gofmt` for formatting)

## Deliverables

Run `gofmt -w` on every file reported by `gofmt -l ./internal/ ./cmd/`. As of the last check, the following 13 files have drift:

1. `internal/agent/runner.go`
2. `internal/memory/memory_test.go`
3. `internal/model/models.go`
4. `internal/orchestrator/agent_result_test.go`
5. `internal/orchestrator/orchestrator.go`
6. `internal/orchestrator/test_writing_test.go`
7. `internal/supervisor/supervisor_test.go`
8. `internal/supervisor/types.go`
9. `internal/tui/agents_test.go`
10. `internal/tui/board.go`
11. `internal/tui/board_test.go`
12. `internal/tui/keys.go`
13. `internal/tui/styles.go`

Run `gofmt -w` on each file. Do NOT make any other changes — no renaming, no refactoring, no import reordering. This is a formatting-only change.

## Verification

After formatting, confirm:

```bash
# Must produce no output
gofmt -l ./internal/ ./cmd/

# All tests must still pass
go test ./...
```

## Conventions

- This is a formatting-only change. Do not modify logic, comments, or imports.
- Build verification: `go test ./...`
