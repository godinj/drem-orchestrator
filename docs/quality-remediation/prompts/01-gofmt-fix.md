# Agent: Fix gofmt Violation

You are working on the `master` branch of drem-orchestrator, a Go-based task orchestrator.
Your task is to fix a gofmt formatting violation in one file.

## Context

Read these before starting:
- `ARCHITECTURE.md` (Formatting section — gofmt compliance rule)
- `.drem/constraints.toml` (the `gofmt compliance` command constraint)

## Deliverables

### Fix formatting

#### 1. `internal/supervisor/types.go`

Run `gofmt -w internal/supervisor/types.go` to auto-format the file.

## Verification

```bash
# Must produce no output:
gofmt -l internal/supervisor/types.go

# Must pass gofmt check:
bash scripts/check_constitution.sh

# All tests must pass:
go test ./internal/supervisor/...
```
