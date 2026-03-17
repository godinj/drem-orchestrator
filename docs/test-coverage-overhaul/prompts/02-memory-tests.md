# Agent: Memory Package Tests

You are working on the `master` branch of drem-orchestrator, a Go TUI tool that orchestrates Claude Code agents via tmux.
Your task is to add comprehensive tests for `internal/memory`, which currently has 0% test coverage.

## Context

Read these before starting:
- `CLAUDE.md` (build commands, conventions)
- `docs/test-coverage-overhaul/prd-test-coverage.md` (Phase 1a section)
- `internal/memory/memory.go` (the source file — 374 LOC, all functions)
- `internal/model/models.go` (model types used by memory: `Memory`, `Task`, `Agent`, `Project`)

## Dependencies

This agent depends on Agent 01 (testutil). If `internal/testutil/testutil.go` doesn't exist yet, create a minimal version with just the `NewTestDB` function:

```go
package testutil

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/godinj/drem-orchestrator/internal/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func NewTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=private", uuid.New().String())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.Project{}, &model.Task{}, &model.Agent{}, &model.TaskEvent{}, &model.Memory{}, &model.TaskComment{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}
```

## Deliverables

### New files

#### 1. `internal/memory/memory_test.go` (~200–250 LOC)

All tests use `testutil.NewTestDB(t)` for database setup. Use table-driven tests where multiple cases test the same function.

**Test functions:**

```go
func TestStoreMemory(t *testing.T)
```
- Store a memory with all fields populated, query it back, verify all fields match
- Store with nil taskID — verify it works
- Store with metadata map — verify JSON round-trip

```go
func TestGetMemories(t *testing.T)
```
Table-driven with these cases:
- Filter by agentID only — returns all memories for that agent
- Filter by agentID + taskID — returns intersection
- Filter by memoryType — returns only matching type
- Limit parameter — returns at most N results
- No matches — returns empty slice, no error
- Multiple agents — returns only the requested agent's memories

```go
func TestGetProjectMemories(t *testing.T)
```
Setup: Create a project, multiple tasks under it, memories linked to those tasks via agents.
- Returns memories across all tasks in the project (JOIN logic)
- Filter by memoryType slice — returns only matching types
- Limit parameter respected
- Empty project — returns empty slice

```go
func TestCompactAgentMemory(t *testing.T)
```
- Store 5+ memories for an agent, compact — returns non-empty summary string
- Compact again (idempotent) — doesn't create duplicate summaries
- Agent with no memories — returns empty string, no error

```go
func TestBuildAgentContext(t *testing.T)
```
- Agent with memories — returns formatted context string
- Token budget truncation — long memories get truncated to fit maxTokens
- No memories — returns empty string

```go
func TestExtractMemoriesFromOutput(t *testing.T)
```
Table-driven with cases matching the regex patterns in memory.go:
- Output containing a decision line — extracts "decision" memory type
- Output containing a blocker line — extracts "blocker" memory type
- Output containing file change references — extracts "file_change" type
- Output containing completion markers — extracts "completion" type
- Output with multiple pattern matches — extracts all
- Output with no matches — returns empty slice, no error
- Empty output string — returns empty slice

**Test helpers in the file:**

```go
// createTestProject sets up a project with a task and agent for memory tests.
func createTestProject(t *testing.T, db *gorm.DB) (projectID, taskID, agentID uuid.UUID)
```

## Scope Limitation

- Only test the `internal/memory` package
- Do NOT modify `memory.go` — test it as-is
- If private helpers (`titleCase`, `matchPatterns`) need testing, test them indirectly through the public API
- Do NOT add tests for other packages

## Verification

```bash
go test ./internal/memory/ -v -cover
```

Target: coverage should reach ~75% or higher. All tests must pass.

## Conventions

- `gofmt` for formatting
- Table-driven tests with `t.Run(tc.name, ...)`
- `t.Helper()` on all test helper functions
- Error assertions with `if err != nil { t.Fatalf(...) }`
