# Agent: Improve Agent Package Test Coverage

You are working on the `master` branch of drem-orchestrator, a Go-based task orchestrator.
Your task is to increase test coverage of `internal/agent/` from 59.9% to at least 70%.

## Context

Read these before starting:
- `internal/agent/` — all `.go` files (both source and existing tests)
- `internal/testutil/testutil.go` (shared test helpers — use `testutil.NewTestDB`, `testutil.CreateAgent`)
- `internal/model/` (agent model types, status enums)

## Deliverables

### Add tests for untested agent logic

Identify untested functions first:
```bash
go test -coverprofile=coverage.out ./internal/agent/
go tool cover -func=coverage.out | grep -v '100.0%' | sort -t: -k3 -n
```

Focus on these categories:

#### 1. Agent configuration and setup

- Config struct initialization with defaults
- Validation of required fields
- Edge cases: empty paths, missing binaries

#### 2. Process argument construction

- Verify Claude CLI arguments are constructed correctly for each agent type
- Headless vs tmux mode argument differences
- Effort level flags (`--effort low` for supervisor, `--effort medium` for agents)

#### 3. Heartbeat and liveness

- Heartbeat timeout detection (agent is stale after `stale_timeout`)
- Heartbeat update logic
- Edge cases: zero time, future time

#### 4. Completion signal handling

- Normal completion (exit 0)
- Error completion (non-zero exit)
- Agent already stopped
- Agent not found

#### 5. Exit info extraction

- Parse exit log JSONL
- Match entry to agent ID
- Missing or malformed entries
- Empty log file

### Conventions

- Use `testutil.NewTestDB(t)` for database tests
- Use `testutil.CreateAgent(t, db, taskID, agentType, status)` for agent records
- Mock tmux and subprocess calls — do NOT spawn real processes
- Use table-driven tests for parameterized cases
- Use `t.TempDir()` for filesystem operations

### Scope Limitation

- Only add/modify test files in `internal/agent/`
- Do NOT modify source files
- Do NOT spawn real tmux sessions or Claude processes
- If mocking is needed and no interface exists, test around the concrete dependency (test the logic, not the I/O)

## Verification

```bash
# Coverage must be >= 70%:
go test -cover ./internal/agent/...

# All tests must pass:
go test ./internal/agent/...

# No regressions:
go test ./...
```
