# Agent: Task Import Package Test Coverage

You are working on the `master` branch of Drem Orchestrator, a Go multi-agent development orchestrator.
Your task is pre-refactor test hardening: raise `internal/taskimport/` coverage from 58% to 75%+.

## Context

Read these before starting:
- `internal/taskimport/import.go` (Import function, toJSONArray helper)
- `internal/taskimport/parse.go` (Parse, extractMeta, parseMetaLine, trimBody — already well-covered)
- `internal/taskimport/parse_test.go` (existing parse tests — study patterns)
- `internal/testutil/testutil.go` (NewTestDB helper)
- `internal/model/models.go` (Task, Project model structs)

## Deliverables

Create `internal/taskimport/import_test.go` for the Import function tests.

### 1. toJSONArray (`import_test.go`)

- `TestToJSONArray_Empty` — pass empty slice, verify nil returned
- `TestToJSONArray_NonEmpty` — pass `[]string{"a", "b"}`, verify result equals `model.JSONArray{"a", "b"}`

### 2. Import — happy path (`import_test.go`)

Use `testutil.NewTestDB(t)` for all database tests. Create a Project first for FK constraints.

- `TestImport_BasicParentTask` — markdown with one `## Parent` heading and description, verify:
  - Returns created count = 1
  - Task exists in DB with correct title, description, status=backlog, project_id
- `TestImport_ParentWithSubtasks` — markdown with one parent and 2 `### Subtask` headings:
  - Returns correct count (3)
  - Parent task exists with no ParentTaskID
  - Subtasks exist with ParentTaskID pointing to parent
- `TestImport_MultipleParents` — markdown with 2 parents, each with 1 subtask:
  - Returns count = 4
  - Each subtask's ParentTaskID matches its parent

### 3. Import — deduplication

- `TestImport_SkipsExistingParent` — pre-create a task with same title, import same markdown:
  - Returns 0 created
  - Original task unchanged
- `TestImport_SkipsExistingSubtask` — pre-create parent + subtask, import markdown with same parent + 2 subtasks:
  - Only the new subtask is created (count = 1 for just the new subtask; the parent is skipped since it exists, and the existing subtask is skipped)

### 4. Import — dependency resolution

- `TestImport_ResolvesDependencies` — markdown with 2 parents where second `Depends-on: First Task`:
  - Both created
  - Second task's DependencyIDs contains first task's UUID
- `TestImport_UnknownDependencyError` — markdown with `Depends-on: Nonexistent Task`:
  - Returns error containing "unknown task"

### 5. Import — metadata

- `TestImport_Priority` — markdown with `Priority: 3`, verify task has Priority=3
- `TestImport_Labels` — markdown with `Labels: backend, api`, verify task has Labels=["backend","api"]

### 6. Import — error handling

- `TestImport_ParseError` — pass a reader that returns an error (e.g., `iotest.ErrReader`), verify error wraps "parse"

### Input format

The Import function reads markdown in this format:

```markdown
## Task Title

Description text here.

Priority: 1
Labels: backend, api

### Subtask Title

Subtask description.

Depends-on: Other Task
```

Use `strings.NewReader()` to create the io.Reader input.

## Conventions

- Package: `taskimport`
- Use `testutil.NewTestDB(t)` for database setup (import `github.com/godinj/drem-orchestrator/internal/testutil`)
- Create a Project record first: `db.Create(&model.Project{ID: uuid.New(), Name: "test", BareRepoPath: "/tmp/test.git"})`
- Table-driven tests where appropriate
- Build verification: `go test ./internal/taskimport/ -cover`
- Target: 75%+ coverage on this package
