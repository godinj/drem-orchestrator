# Agent: Model Package Test Coverage

You are working on the `master` branch of Drem Orchestrator, a Go multi-agent development orchestrator.
Your task is pre-refactor test hardening: raise `internal/model/` coverage from 56% to 80%+.

## Context

Read these before starting:
- `internal/model/models.go` (GORM models with BeforeCreate hooks)
- `internal/model/enums.go` (TaskStatus, AgentType, AgentStatus enums with parse/string methods)
- `internal/model/json.go` (JSONField and JSONArray custom GORM types)
- `internal/model/models_test.go` (existing tests — testDB helper, TDD field round-trip tests)
- `internal/model/enums_test.go` (existing tests — ParseTaskStatus, IsActionable, IsHumanGate)
- `internal/testutil/testutil.go` (NewTestDB helper for isolated in-memory SQLite)

## Deliverables

Add tests to the existing test files. Do NOT create new test files — extend `models_test.go` and `enums_test.go`, and create `json_test.go` for JSON type tests.

### 1. BeforeCreate hooks (`models_test.go`)

Project and Task BeforeCreate are already tested. Add tests for the 4 untested hooks:

- `TestAgentBeforeCreate` — verify Agent gets UUID when ID is nil; verify pre-set ID is preserved
- `TestTaskEventBeforeCreate` — verify TaskEvent gets UUID when ID is nil; verify pre-set ID is preserved
- `TestMemoryBeforeCreate` — verify Memory gets UUID when ID is nil; verify pre-set ID is preserved
- `TestTaskCommentBeforeCreate` — verify TaskComment gets UUID when ID is nil; verify pre-set ID is preserved

Use the existing `testDB()` helper pattern from models_test.go. Each test should:
1. Create a record with `uuid.Nil` ID via `db.Create()`, reload from DB, verify ID is non-nil
2. Create a record with pre-set UUID, reload from DB, verify ID matches

Note: Agent, TaskEvent, Memory, and TaskComment all have foreign key constraints. Create a Project and Task first to satisfy FK requirements.

### 2. ParseAgentType (`enums_test.go`)

- `TestParseAgentType` — table-driven test covering all 6 valid agent types: "orchestrator", "planner", "coder", "researcher", "reviewer", "fixer"
- Test invalid input: `ParseAgentType("invalid_agent")` should return error
- Test empty input: `ParseAgentType("")` should return error

### 3. String methods (`enums_test.go`)

- `TestTaskStatusString` — verify `StatusBacklog.String() == "backlog"`, etc. (at least 3 statuses)
- `TestAgentTypeString` — verify `AgentPlanner.String() == "planner"`, etc. (at least 3 types)
- `TestAgentStatusString` — verify `AgentWorking.String() == "working"`, etc. (all 4 statuses)

### 4. ParseTaskStatus error path (`enums_test.go`)

Extend existing tests to cover:
- `ParseTaskStatus("nonexistent")` returns error
- `ParseTaskStatus("")` returns error

### 5. JSONField and JSONArray error paths (`json_test.go`)

Create `internal/model/json_test.go` with:

- `TestJSONFieldScan_UnsupportedType` — call `Scan(123)` (int), verify error contains "unsupported type"
- `TestJSONFieldScan_MalformedJSON` — call `Scan("{broken}")`, verify error contains "unmarshal"
- `TestJSONFieldScan_NilValue` — call `Scan(nil)`, verify result is nil map
- `TestJSONFieldScan_ByteSlice` — call `Scan([]byte(...))`, verify correct map
- `TestJSONFieldValue_NilMap` — verify nil JSONField produces nil driver.Value
- `TestJSONFieldValue_ValidMap` — verify non-nil JSONField produces JSON string

- `TestJSONArrayScan_UnsupportedType` — call `Scan(123)`, verify error
- `TestJSONArrayScan_MalformedJSON` — call `Scan("[broken]")`, verify error
- `TestJSONArrayScan_NilValue` — call `Scan(nil)`, verify nil slice
- `TestJSONArrayScan_ByteSlice` — call `Scan([]byte(...))`, verify correct slice
- `TestJSONArrayValue_NilSlice` — verify nil JSONArray produces nil
- `TestJSONArrayValue_ValidSlice` — verify non-nil JSONArray produces JSON string

Use table-driven tests where appropriate. These are pure unit tests — no database needed.

## Conventions

- Package: `model`
- Use `t.Helper()` in helper functions
- Table-driven tests with `t.Run(tc.name, ...)`
- `t.Fatalf()` for setup failures, `t.Errorf()` for assertions
- Build verification: `go test ./internal/model/ -cover`
- Target: 80%+ coverage on this package
