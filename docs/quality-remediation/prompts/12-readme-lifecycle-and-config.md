# Agent: Update README Task Lifecycle Diagram and Configuration Table

You are working on the `master` branch of drem-orchestrator, a Go-based task orchestrator.
Your task is to update the task lifecycle diagram and add missing configuration options to README.md.

## Context

Read these before starting:
- `README.md` (find "Task Lifecycle" section with the ASCII diagram, and "Configuration" section with the table)
- `internal/model/enums.go` (authoritative list of all task status constants)
- `internal/state/state.go` or `internal/state/transitions.go` (valid state transitions)
- `cmd/drem/config.go` (authoritative list of all config fields and defaults)

## Dependencies

This agent depends on Agent 08 (ARCHITECTURE.md update). If it hasn't landed, work independently.

## Deliverables

### 1. Update task lifecycle diagram

The current ASCII diagram is missing 4 states. Update it to include ALL states from `internal/model/enums.go`:

Missing states and where they fit:
- `needs_clarification` — between `planning` and `plan_review`
- `test_writing` — between `plan_review` and `in_progress` (TDD: write tests first)
- `test_review` — between `test_writing` and `in_progress` (human gate: approve tests)
- `rejected` — terminal state reachable from review gates

Read the actual state transition table in `internal/state/` to get the exact valid transitions. The diagram must accurately reflect the real transitions — do not guess.

### 2. Update state descriptions

Add to the bullet list below the diagram:
- **needs_clarification** — Plan assumptions need human clarification before proceeding to review
- **test_writing** — Test agent is writing tests before implementation begins (TDD)
- **test_review** — Human gate: verify written tests before implementation
- **rejected** — Task rejected at a review gate (terminal)

### 3. Add missing configuration options

Read `cmd/drem/config.go` and compare every config struct field against the README Configuration table. Add any missing options.

Known missing (verify defaults from code):
| Setting | Description |
|---------|-------------|
| `context_warn_percent` | Context usage % that triggers a warning |
| `context_stop_percent` | Context usage % that triggers a hard stop |
| `context_fixer_percent` | Context usage % that triggers fixer agent escalation |
| `test_command` | Command to run tests (e.g., `go test ./...`) |
| `compile_command` | Command to compile the project |
| `scoped_tests` | Run tests scoped to subtask file changes only |
| `test_timeout` | Timeout for test command execution |

Read the code to get accurate defaults and exact TOML key names. There may be additional undocumented options — add them all.

### Scope Limitation

- Only modify `README.md`
- Keep the existing ASCII diagram style — extend it, don't redesign it
- Use the same table format as the existing Configuration table
- Verify all transitions against the actual state machine code

## Verification

```bash
# All states from enums.go appear in README:
grep 'Status[A-Z]' internal/model/enums.go | sed 's/.*= "//' | sed 's/"//' | while read state; do
  grep -q "$state" README.md || echo "MISSING STATE: $state"
done

# All config fields appear in README (approximate — check config struct manually):
grep -c 'context_warn_percent\|context_stop_percent\|test_command\|compile_command\|scoped_tests\|test_timeout\|context_fixer_percent' README.md
```
