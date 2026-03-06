// Package db provides database initialization and migration helpers for the
// Drem Orchestrator.
package db

import (
	"fmt"
	"log"
	"os"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

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

	// Data migration: copy tmux_window → tmux_session for existing rows.
	db.Exec("UPDATE agents SET tmux_session = tmux_window WHERE tmux_session = '' AND tmux_window != ''")

	return db, nil
}

// AutoMigrate creates or updates all database tables to match the current
// model definitions.
func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&model.Project{},
		&model.Task{},
		&model.Agent{},
		&model.TaskEvent{},
		&model.Memory{},
		&model.TaskComment{},
	)
}
