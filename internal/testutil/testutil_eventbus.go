package testutil

import (
	"path/filepath"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/eventbus"
)

// NewTestBus creates a temporary eventbus.Bus backed by a SQLite file in
// t.TempDir(). The bus is closed automatically via t.Cleanup.
func NewTestBus(t *testing.T) *eventbus.Bus {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "testbus.db")
	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("eventbus.New: %v", err)
	}
	t.Cleanup(func() { bus.Close() })
	return bus
}
