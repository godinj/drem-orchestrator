// Package db provides database initialization and migration helpers for the
// Drem Orchestrator.
package db

import (
	"fmt"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// Init opens a SQLite database at dbPath with WAL mode enabled, runs
// auto-migrations for all models, and returns the ready-to-use *gorm.DB.
func Init(dbPath string) (*gorm.DB, error) {
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", dbPath, err)
	}
	if err := AutoMigrate(db); err != nil {
		return nil, fmt.Errorf("auto-migrate: %w", err)
	}
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
	)
}
