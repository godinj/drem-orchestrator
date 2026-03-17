# Agent: GORM BeforeCreate Hook Consolidation

You are working on the `master` branch of Drem Orchestrator, a Go-based agent orchestration system.
Your task is to eliminate 6 identical `BeforeCreate` UUID generation hooks by registering a single GORM callback.

## Context

Read these before starting:
- `ARCHITECTURE.md` (Models section — no duplicate GORM hooks rule)
- `internal/model/models.go` (the 6 identical BeforeCreate methods)
- `internal/db/db.go` (database initialization — where the callback should be registered)

## Deliverables

### Modified files

#### 1. `internal/model/models.go`

Delete all 6 `BeforeCreate` methods:
- `func (p *Project) BeforeCreate(tx *gorm.DB) error`
- `func (t *Task) BeforeCreate(tx *gorm.DB) error`
- `func (a *Agent) BeforeCreate(tx *gorm.DB) error`
- `func (e *TaskEvent) BeforeCreate(tx *gorm.DB) error`
- `func (m *Memory) BeforeCreate(tx *gorm.DB) error`
- `func (c *TaskComment) BeforeCreate(tx *gorm.DB) error`

Each currently does the same thing:
```go
if X.ID == uuid.Nil {
    X.ID = uuid.New()
}
return nil
```

#### 2. `internal/db/db.go`

After `db.AutoMigrate(...)`, register a single GORM callback that generates UUIDs for any model with an `ID uuid.UUID` field. Use GORM's callback API:

```go
db.Callback().Create().Before("gorm:create").Register("generate_uuid", func(tx *gorm.DB) {
    // Use reflection or a type switch to check if the model's ID field
    // is uuid.Nil, and if so, set it to uuid.New()
})
```

The simplest correct approach is a type switch over the 6 known model types. An interface-based approach (e.g. `type HasUUID interface { GetID() uuid.UUID; SetID(uuid.UUID) }`) is also acceptable if it's cleaner, but do NOT over-engineer — the goal is to replace 6 identical methods with 1 registration.

## Scope Limitation

Do NOT modify any other files. Do NOT change model field types, tags, or struct layouts. The only change is where UUID generation happens (model hooks → DB callback).

## Verification

```bash
# Must show exactly 0 BeforeCreate methods in models.go
grep -c 'func.*BeforeCreate' internal/model/models.go
# Should output: 0

# All tests must pass — this is a pure refactor
go test ./...
```

## Conventions

- Error wrapping with `fmt.Errorf("context: %w", err)`
- Exported functions have doc comments
- Build verification: `go test ./...`
