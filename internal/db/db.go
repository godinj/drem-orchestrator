// Package db provides database initialization and migration helpers for the
// Drem Orchestrator.
package db

import (
	"fmt"
	"log"
	"os"
	"reflect"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/metrics"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// Init opens a SQLite database at dbPath with WAL mode enabled, runs
// auto-migrations for all models, and returns the ready-to-use *gorm.DB.
// If logPath is non-empty, GORM query logs are written there; otherwise
// logging is silenced so it cannot corrupt the TUI.
func Init(dbPath string, logPath ...string) (*gorm.DB, error) {
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)

	// Keep GORM's logger off the terminal — Bubble Tea owns stdout/stderr.
	var gormLogger logger.Interface
	if len(logPath) > 0 && logPath[0] != "" {
		f, err := os.OpenFile(logPath[0], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			gormLogger = logger.New(log.New(f, "\n", log.LstdFlags), logger.Config{
				LogLevel: logger.Warn,
			})
		}
	}
	if gormLogger == nil {
		gormLogger = logger.New(log.New(os.Stderr, "", 0), logger.Config{
			LogLevel: logger.Silent,
		})
	}

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormLogger})
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", dbPath, err)
	}
	if err := AutoMigrate(db); err != nil {
		return nil, fmt.Errorf("auto-migrate: %w", err)
	}

	registerUUIDCallback(db)

	// Data migration: copy tmux_window → tmux_session for existing rows.
	db.Exec("UPDATE agents SET tmux_session = tmux_window WHERE tmux_session = '' AND tmux_window != ''")

	// Composite index for the hot /tasks lookup (project-scoped, newest-
	// first). Bug E W2.1: without this, SQLite picks the generic
	// idx_tasks_project_id and a TEMP B-TREE sort, which at 4 GiB DB
	// scale stretched /tasks latency to 7-28 s during the 2026-04-21
	// retry storm. GORM AutoMigrate does not emit composite indexes
	// with a direction modifier, so the migration is declared here as
	// raw SQL alongside the existing tmux back-fill.
	db.Exec("CREATE INDEX IF NOT EXISTS idx_tasks_project_created ON tasks(project_id, created_at DESC)")
	db.Exec(`UPDATE worker_attempts
		SET branch = COALESCE(
			NULLIF((SELECT worktree_branch FROM agents WHERE agents.id = worker_attempts.agent_id), ''),
			NULLIF((SELECT worktree_branch FROM tasks WHERE tasks.id = worker_attempts.task_id), ''),
			''
		)
		WHERE branch = ''`)
	db.Exec(`UPDATE worker_attempts
		SET state = ?, completed_at = CURRENT_TIMESTAMP
		WHERE completed_at IS NULL
		AND id NOT IN (
			SELECT id FROM worker_attempts keep
			WHERE keep.completed_at IS NULL
			AND keep.task_id = worker_attempts.task_id
			AND keep.agent_type = worker_attempts.agent_type
			AND keep.branch = worker_attempts.branch
			ORDER BY keep.created_at DESC, keep.id DESC
			LIMIT 1
		)`, model.WorkerAttemptSuperseded)
	db.Exec("DROP INDEX IF EXISTS idx_worker_attempt_active_task_role")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_worker_attempt_active_task_role_branch ON worker_attempts(task_id, agent_type, branch) WHERE completed_at IS NULL")
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_delivery_artifact_current_task ON delivery_artifacts(task_id) WHERE invalidated_at IS NULL").Error; err != nil {
		return nil, fmt.Errorf("create current delivery artifact index: %w", err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_host_rework_active_task ON host_rework_sessions(task_id) WHERE disposition = 'active'").Error; err != nil {
		return nil, fmt.Errorf("create active host rework session index: %w", err)
	}

	return db, nil
}

// AutoMigrate creates or updates all database tables to match the current
// model definitions.
func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&model.Project{},
		&model.ProjectPromptAsset{},
		&model.Task{},
		&model.TaskSpecification{},
		&model.TaskAcceptanceCriterion{},
		&model.Agent{},
		&model.WorkerAttempt{},
		&model.AttemptEvent{},
		&model.TaskEvent{},
		&model.Memory{},
		&model.TaskComment{},
		&model.BranchAcceptanceRecord{},
		&model.PreliminaryGateRun{},
		&model.DeliveryArtifact{},
		&model.VerificationRecord{},
		&model.VerificationInteraction{},
		&model.IntegrationAuthorization{},
		&model.DeliveryReworkRecord{},
		&model.HostReworkSession{},
		&model.HostReworkSubmission{},
		&model.MergeIntent{},
		&model.MergeCompletion{},
		&model.TaskMutationRecord{},
		&model.BugReport{},
		&model.BugReportComment{},
		&metrics.Metric{},
		&csuite.CsuiteAgent{},
		&csuite.CsuiteInboxMessage{},
		&gitref.BranchRef{},
	)
}

// registerUUIDCallback registers a single GORM callback that generates UUIDs
// for any model whose ID field is uuid.UUID and currently set to uuid.Nil.
// This replaces per-model BeforeCreate hooks with one centralised check.
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
