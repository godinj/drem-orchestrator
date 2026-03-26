package orchestrator

import (
	"log/slog"

	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// NewForCLI creates a minimal Orchestrator suitable for headless CLI gate
// commands (approve, reject, answer, pass, fail). Only the fields needed by
// the handler methods in handlers.go are initialised: db, worktree, logger,
// and an internal events channel that is drained in the background.
//
// This avoids pulling in agent.Runner, merge queues, tmux, and the many other
// dependencies required by the full New() constructor.
func NewForCLI(db *gorm.DB, wt *worktree.Manager) *Orchestrator {
	// Buffered channel + background goroutine so emit() never blocks.
	events := make(chan Event, 16)
	go func() {
		for range events {
			// discard — no TUI listening in CLI mode
		}
	}()
	return &Orchestrator{
		db:       db,
		worktree: wt,
		events:   events,
		logger:   slog.Default().With("component", "orchestrator-cli"),
	}
}
