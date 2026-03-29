package watcher

import (
	"context"
	"log"
	"time"

	"gorm.io/gorm"
)

// RunnerConfig holds configuration for the watcher main loop.
type RunnerConfig struct {
	// AllowedAgents lists the agent names permitted to run turns.
	AllowedAgents []string

	// InboxBaseDir is the root directory containing <agent>/inbox/ subdirectories
	// watched by InboxSignalTrigger for *.signal files.
	InboxBaseDir string

	// PromptDir is the directory containing agent prompt files named
	// {agent}.md (e.g. mike.md, seth.md). When set, the watcher loads
	// the prompt file before spawning the agent subprocess. If empty or
	// if a prompt file does not exist, the turn is skipped with a log warning.
	PromptDir string

	// InboxPollInterval controls how often InboxSignalTrigger scans for new
	// signal files. Zero uses the InboxSignalTrigger default (2s).
	InboxPollInterval time.Duration

	// SafetyInterval controls how often the SafetyTimer wakes "mike".
	// Zero uses the SafetyTimer default (5m).
	SafetyInterval time.Duration

	// HeartbeatInterval controls how often heartbeats are written to the DB.
	// Zero uses the HeartbeatWriter default (30s).
	HeartbeatInterval time.Duration

	// Precheck, when non-nil, is checked before each turn subprocess is
	// launched. If HasWork returns false, the turn is skipped. This is
	// passed through to Config.Precheck on the LifecycleManager.
	Precheck TurnPrecheck
}

// Runner is the watcher main loop. It starts all trigger sources, fans in
// wake-ups to the lifecycle manager, and writes heartbeats to prove liveness.
//
// Use NewRunner to create and Run to start. Run blocks until the provided
// context is cancelled, then stops all components cleanly.
type Runner struct {
	cfg    RunnerConfig
	db     *gorm.DB
	runner CommandRunner

	// notifications is the channel for EventDeliveryTrigger. Production
	// callers feed it from the event routing layer; tests inject a buffered
	// channel directly.
	notifications chan []string
}

// NewRunner creates a Runner. The CommandRunner is used by the lifecycle
// manager to execute agent turn subprocesses.
//
// Call Run to start the main loop.
func NewRunner(cfg RunnerConfig, db *gorm.DB, runner CommandRunner) *Runner {
	return &Runner{
		cfg:           cfg,
		db:            db,
		runner:        runner,
		notifications: make(chan []string, 16),
	}
}

// Notifications returns the channel that feeds the EventDeliveryTrigger.
// Production callers push agent name lists onto this channel from the event
// routing layer.
func (r *Runner) Notifications() chan<- []string {
	return r.notifications
}

// Run starts all watcher components and blocks until ctx is cancelled.
// On cancellation it stops all components in reverse start order and returns nil.
//
// Run auto-migrates the Heartbeat table before starting.
//
// Component start order:
//  1. Heartbeat writer
//  2. Lifecycle manager
//  3. Safety timer
//  4. Event delivery trigger
//  5. Inbox signal trigger
//  6. Inbox event fan-in goroutine
//
// Shutdown is reverse order: inbox fan-in stops (via context), inbox trigger
// stops, event delivery trigger stops (via context), safety timer stops,
// lifecycle manager closes, heartbeat writer stops.
func (r *Runner) Run(ctx context.Context) error {
	// Auto-migrate the heartbeat table.
	if err := r.db.AutoMigrate(&Heartbeat{}); err != nil {
		return err
	}

	lcfg := Config{AllowedAgents: r.cfg.AllowedAgents, Precheck: r.cfg.Precheck}
	lm := NewLifecycleManager(r.db, lcfg, r.runner)

	hb := NewHeartbeatWriter(r.db, r.cfg.HeartbeatInterval)
	hb.Start()

	safety := NewSafetyTimer(r.cfg.SafetyInterval, lm)
	safety.Start()

	// EventDeliveryTrigger runs in a goroutine; it reads from r.notifications
	// and calls lm.TriggerAgent for each agent.
	edCtx, edCancel := context.WithCancel(ctx)
	ed := NewEventDeliveryTrigger(r.notifications, lm)
	edDone := make(chan struct{})
	go func() {
		defer close(edDone)
		ed.Run(edCtx)
	}()

	// InboxSignalTrigger emits TriggerEvents on its channel.
	var inbox *InboxSignalTrigger
	if r.cfg.InboxBaseDir != "" {
		inbox = NewInboxSignalTrigger(r.cfg.InboxBaseDir, r.cfg.InboxPollInterval)
		if err := inbox.Start(ctx); err != nil {
			// Non-fatal: log and continue without inbox trigger.
			log.Printf("watcher: inbox trigger start failed: %v", err)
			inbox = nil
		}
	}

	// Fan-in goroutine: read InboxSignalTrigger events → lifecycle manager.
	fanInDone := make(chan struct{})
	go func() {
		defer close(fanInDone)
		if inbox == nil {
			// No inbox trigger — block until context is cancelled.
			<-ctx.Done()
			return
		}
		for {
			select {
			case ev, ok := <-inbox.Events():
				if !ok {
					return
				}
				lm.TriggerAgent(ev.AgentName)
			case <-ctx.Done():
				return
			}
		}
	}()

	log.Printf("watcher: main loop started (agents=%v)", r.cfg.AllowedAgents)

	// Block until context is cancelled (e.g., SIGTERM/SIGINT).
	<-ctx.Done()

	log.Println("watcher: shutting down")

	// Shutdown in reverse order.
	// 1. Fan-in goroutine stops when ctx is cancelled (already done).
	<-fanInDone

	// 2. Inbox trigger.
	if inbox != nil {
		_ = inbox.Stop()
	}

	// 3. Event delivery trigger.
	edCancel()
	<-edDone

	// 4. Safety timer.
	safety.Stop()

	// 5. Lifecycle manager — waits for in-flight turns.
	lm.Close()

	// 6. Heartbeat writer.
	hb.Stop()

	log.Println("watcher: shutdown complete")

	return nil
}
