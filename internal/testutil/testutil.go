// Package testutil provides shared test helpers for the Drem Orchestrator
// test suite. All test setup helpers (DB init, git repo scaffolding, entity
// factories) live here to eliminate duplication across packages.
package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/metrics"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/watcher"
)

// ---------------------------------------------------------------------------
// Database helpers
// ---------------------------------------------------------------------------

// NewTestDB creates an in-memory SQLite database with cache=shared mode and
// runs auto-migration for all models. Each call uses a unique name so
// concurrent tests get isolated databases.
func NewTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := uuid.New().String()
	dsn := "file:" + name + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Project{},
		&model.Task{},
		&model.Agent{},
		&model.TaskEvent{},
		&model.Memory{},
		&model.TaskComment{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

// NewTestDBWithModels creates an in-memory SQLite database and runs
// auto-migration for both the core orchestrator models and the supplied
// extra models. Use this when testing packages that define their own GORM
// models (e.g., internal/csuite) so that all tables exist without resorting
// to local gorm.Open calls (which violate the constitution).
func NewTestDBWithModels(t *testing.T, extraModels ...any) *gorm.DB {
	t.Helper()
	name := uuid.New().String()
	dsn := "file:" + name + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	coreModels := []any{
		&model.Project{},
		&model.Task{},
		&model.Agent{},
		&model.TaskEvent{},
		&model.Memory{},
		&model.TaskComment{},
	}
	allModels := append(coreModels, extraModels...)
	if err := db.AutoMigrate(allModels...); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	registerUUIDCallback(db)
	return db
}

// NewSharedTestDB creates an in-memory SQLite database with cache=shared.
// Use this for tests that need a single shared in-memory DB.
func NewSharedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Project{},
		&model.Task{},
		&model.Agent{},
		&model.TaskEvent{},
		&model.Memory{},
		&model.TaskComment{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

// ---------------------------------------------------------------------------
// Entity factory helpers
// ---------------------------------------------------------------------------

// CreateProject creates a test project in the database and returns it.
func CreateProject(t *testing.T, db *gorm.DB, name, bareRepoPath, defaultBranch string) model.Project {
	t.Helper()
	p := model.Project{
		ID:            uuid.New(),
		Name:          name,
		BareRepoPath:  bareRepoPath,
		DefaultBranch: defaultBranch,
	}
	if err := db.Create(&p).Error; err != nil {
		t.Fatalf("create test project: %v", err)
	}
	return p
}

// CreateTask creates a test task in the database and returns it.
func CreateTask(t *testing.T, db *gorm.DB, projectID uuid.UUID, title string, status model.TaskStatus) model.Task {
	t.Helper()
	task := model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       title,
		Description: title,
		Status:      status,
		Category:    model.CategoryStandard,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create test task: %v", err)
	}
	return task
}

// CreateQuickFixTask creates a test task with CategoryQuickFix in the database
// and returns it.
func CreateQuickFixTask(t *testing.T, db *gorm.DB, projectID uuid.UUID, title string, status model.TaskStatus) *model.Task {
	t.Helper()
	task := &model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       title,
		Description: title,
		Status:      status,
		Category:    model.CategoryQuickFix,
	}
	if err := db.Create(task).Error; err != nil {
		t.Fatalf("create quick fix task: %v", err)
	}
	return task
}

// CreateAgent creates a test agent in the database and returns it.
// Optional AgentOption values can be provided to set enrichment fields.
func CreateAgent(t *testing.T, db *gorm.DB, taskID uuid.UUID, agentType model.AgentType, status model.AgentStatus, opts ...AgentOption) model.Agent {
	t.Helper()
	ag := model.Agent{
		ID:        uuid.New(),
		AgentType: agentType,
		Status:    status,
	}
	if taskID != uuid.Nil {
		ag.CurrentTaskID = &taskID
	}
	for _, opt := range opts {
		opt(&ag)
	}
	if err := db.Create(&ag).Error; err != nil {
		t.Fatalf("create test agent: %v", err)
	}
	return ag
}

// CreateAgentWithOptions creates a test agent in the database with specified
// ModelID and Effort fields and returns it.
func CreateAgentWithOptions(t *testing.T, db *gorm.DB, taskID uuid.UUID, agentType model.AgentType, status model.AgentStatus, modelID, effort string) model.Agent {
	t.Helper()
	ag := model.Agent{
		ID:        uuid.New(),
		AgentType: agentType,
		Status:    status,
		ModelID:   modelID,
		Effort:    effort,
	}
	if taskID != uuid.Nil {
		ag.CurrentTaskID = &taskID
	}
	if err := db.Create(&ag).Error; err != nil {
		t.Fatalf("create test agent with options: %v", err)
	}
	return ag
}

// ---------------------------------------------------------------------------
// C-Suite entity factory helpers
// ---------------------------------------------------------------------------

// CreateCsuiteAgent creates a test CsuiteAgent in the database and returns it.
func CreateCsuiteAgent(t *testing.T, db *gorm.DB, name string, status csuite.AgentMonStatus) csuite.CsuiteAgent {
	t.Helper()
	ag := csuite.CsuiteAgent{
		ID:     uuid.New(),
		Name:   name,
		Status: status,
	}
	if err := db.Create(&ag).Error; err != nil {
		t.Fatalf("create test csuite agent: %v", err)
	}
	return ag
}

// NewTestStore creates an isolated DB with csuite models migrated and returns
// a ready-to-use csuite.Store. Use this instead of creating stores in test files.
func NewTestStore(t *testing.T) *csuite.Store {
	t.Helper()
	db := NewTestDBWithModels(t,
		&csuite.CsuiteAgent{},
		&csuite.CsuiteInboxMessage{},
	)
	return csuite.NewStore(db)
}

// CreateCsuiteInboxMessage creates a test CsuiteInboxMessage in the database
// and returns it.
func CreateCsuiteInboxMessage(t *testing.T, db *gorm.DB, from, to, subject string, priority csuite.InboxPriority, msgType csuite.InboxMessageType) csuite.CsuiteInboxMessage {
	t.Helper()
	msg := csuite.CsuiteInboxMessage{
		ID:        uuid.New(),
		FromAgent: from,
		ToAgent:   to,
		Subject:   subject,
		Priority:  priority,
		Type:      msgType,
	}
	if err := db.Create(&msg).Error; err != nil {
		t.Fatalf("create test csuite inbox message: %v", err)
	}
	return msg
}

// ---------------------------------------------------------------------------
// Watcher entity factory helpers
// ---------------------------------------------------------------------------

// NewTestWatcherStore creates an isolated DB with the TurnMetric model
// migrated and returns a ready-to-use watcher.Store.
func NewTestWatcherStore(t *testing.T) *watcher.Store {
	t.Helper()
	db := NewTestDBWithModels(t, &watcher.TurnMetric{})
	return watcher.NewStore(db)
}

// CreateTurnMetric inserts a TurnMetric row directly into the database and
// returns it. Use this to set up test data for QueryTurns tests without
// depending on RecordTurn's implementation.
func CreateTurnMetric(t *testing.T, db *gorm.DB, agent string, exitStatus int) watcher.TurnMetric {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	m := watcher.TurnMetric{
		ID:              uuid.New(),
		Agent:           agent,
		StartedAt:       now.Add(-2 * time.Second),
		EndedAt:         now,
		DurationMs:      2000,
		TokensIn:        100,
		TokensOut:       50,
		EventsProcessed: 2,
		MessagesSent:    1,
		ExitStatus:      exitStatus,
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("create test turn metric: %v", err)
	}
	return m
}

// ---------------------------------------------------------------------------
// Metrics entity factory helpers
// ---------------------------------------------------------------------------

// NewTestMetricsStore creates an isolated DB with the Metric model migrated
// and returns a ready-to-use metrics.Store.
func NewTestMetricsStore(t *testing.T) *metrics.Store {
	t.Helper()
	db := NewTestDBWithModels(t, &metrics.Metric{})
	return metrics.NewStore(db)
}

// CreateMetric inserts a Metric row directly into the database and returns it.
// Use this to seed test data for Query tests without depending on Record's
// implementation.
func CreateMetric(t *testing.T, db *gorm.DB, agentID uuid.UUID, name string, value float64, ts time.Time) metrics.Metric {
	t.Helper()
	m := metrics.Metric{
		ID:        uuid.New(),
		AgentID:   agentID,
		Name:      name,
		Value:     value,
		Timestamp: ts,
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("create test metric: %v", err)
	}
	return m
}

// ---------------------------------------------------------------------------
// GORM callback helpers
// ---------------------------------------------------------------------------

// registerUUIDCallback registers a GORM callback that auto-generates UUIDs
// for models with a uuid.UUID ID field set to uuid.Nil. This mirrors the
// production callback in internal/db without importing that package.
func registerUUIDCallback(db *gorm.DB) {
	db.Callback().Create().Before("gorm:create").Register("generate_uuid", func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Dest == nil {
			return
		}
		val := reflect.ValueOf(tx.Statement.Dest)
		if val.Kind() == reflect.Ptr {
			val = val.Elem()
		}
		if val.Kind() != reflect.Struct {
			return
		}
		idField := val.FieldByName("ID")
		if !idField.IsValid() || idField.Type() != reflect.TypeOf(uuid.UUID{}) {
			return
		}
		if idField.Interface().(uuid.UUID) == uuid.Nil {
			idField.Set(reflect.ValueOf(uuid.New()))
		}
	})
}

// ---------------------------------------------------------------------------
// Supervisor mock helpers
// ---------------------------------------------------------------------------

// NewMockSupervisor creates a supervisor backed by a fake claude binary that
// echoes the given response string. Useful for testing supervisor-dependent
// code paths without calling the real LLM.
func NewMockSupervisor(t *testing.T, response string) *supervisor.Supervisor {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-claude")
	// Use printf to avoid echo adding a trailing newline interpretation issue.
	script := fmt.Sprintf("#!/bin/sh\ncat <<'FAKE_EOF'\n%s\nFAKE_EOF\n", response)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude bin: %v", err)
	}
	return supervisor.New(bin, 5*time.Second, model.AgentCLIConfig{Effort: "low"})
}
