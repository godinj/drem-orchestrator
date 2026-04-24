package watcher

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"
)

const (
	// defaultPollInterval is the default scan interval for InboxSignalTrigger
	// when no explicit interval is provided.
	defaultPollInterval = 2 * time.Second

	// triggerEventBufferSize is the capacity of the events channel. A buffer
	// of 16 prevents the poll goroutine from blocking when the consumer is
	// momentarily slow.
	triggerEventBufferSize = 16

	// inboxSignalSource identifies the trigger source in log messages.
	inboxSignalSource = "inbox-signal"

	// newDirectoryPollInterval is how often InboxSignalTrigger scans for newly
	// created agent directories under the base directory. Agents are added rarely
	// so a 30-second interval is sufficient to discover new directories without
	// wasting resources.
	newDirectoryPollInterval = 30 * time.Second
)

// InboxSignalTrigger watches for *.signal files in <baseDir>/<agent>/inbox/
// directories. When a .signal file appears, it emits a TriggerEvent for the
// corresponding agent and removes the file to prevent re-triggering.
//
// InboxSignalTrigger uses polling at a configurable interval to detect signal
// files. Both directories existing at Start time and those created later are
// discovered on each poll cycle via filepath.Glob.
//
// InboxSignalTrigger implements the Trigger interface.
type InboxSignalTrigger struct {
	baseDir      string
	pollInterval time.Duration
	events       chan TriggerEvent
	cancel       context.CancelFunc
	done         chan struct{}
}

// NewInboxSignalTrigger creates an InboxSignalTrigger that watches for signal
// files under <baseDir>/<agent>/inbox/. The pollInterval controls how often
// the trigger scans for new signal files and agent directories.
//
// Call Start to begin watching. The returned trigger does not watch any
// directories until Start is called.
func NewInboxSignalTrigger(baseDir string, pollInterval time.Duration) *InboxSignalTrigger {
	if baseDir == "" {
		panic("NewInboxSignalTrigger: baseDir must not be empty")
	}
	return &InboxSignalTrigger{
		baseDir:      baseDir,
		pollInterval: pollInterval,
		events:       make(chan TriggerEvent, triggerEventBufferSize),
		done:         make(chan struct{}),
	}
}

// Start begins watching for *.signal files in all agent inbox directories
// under the base directory. It is non-blocking — watching runs in a background
// goroutine. Returns an error if the watcher cannot be initialised.
func (t *InboxSignalTrigger) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	go t.pollLoop(ctx)
	return nil
}

// pollLoop runs in a background goroutine, scanning for signal files on each
// tick. It closes the events channel and signals done when the context is
// cancelled.
func (t *InboxSignalTrigger) pollLoop(ctx context.Context) {
	defer func() {
		close(t.events)
		close(t.done)
	}()

	ticker := time.NewTicker(t.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.poll()
		}
	}
}

// poll performs a single scan of the base directory for signal files.
// It checks both dotfiles (.signal, created by csuite-proto.sh and the
// bridge server) and regular signal files (*.signal) for compatibility.
func (t *InboxSignalTrigger) poll() {
	// Glob does not match dotfiles, so we check .signal files explicitly
	// by scanning agent directories, then also glob for *.signal.
	var matches []string

	// Check for .signal dotfiles in each agent inbox.
	agentDirs, _ := filepath.Glob(filepath.Join(t.baseDir, "*/inbox"))
	for _, inboxDir := range agentDirs {
		dotSignal := filepath.Join(inboxDir, ".signal")
		if _, err := os.Stat(dotSignal); err == nil {
			matches = append(matches, dotSignal)
		}
	}

	// Also check for non-dot signal files (*.signal) for backwards compat.
	globMatches, err := filepath.Glob(filepath.Join(t.baseDir, "*/inbox/*.signal"))
	if err != nil {
		log.Printf("%s: glob error: %v", inboxSignalSource, err)
	}
	matches = append(matches, globMatches...)

	for _, match := range matches {
		// Path structure: <baseDir>/<agent>/inbox/<file>
		// Agent name is the basename of the directory two levels above the file.
		inboxDir := filepath.Dir(match)
		agentDir := filepath.Dir(inboxDir)
		agentName := filepath.Base(agentDir)

		ev := TriggerEvent{AgentName: agentName, Source: inboxSignalSource, Timestamp: time.Now()}

		select {
		case t.events <- ev:
		default:
			log.Printf("%s: event channel full, dropping wake event for agent %s", inboxSignalSource, agentName)
		}

		if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
			log.Printf("%s: remove %s: %v", inboxSignalSource, match, err)
		}
	}
}

// Stop halts all file watching and waits for the background goroutine to exit.
// The Events channel is closed before Stop returns. Stop is idempotent —
// calling it more than once is safe.
func (t *InboxSignalTrigger) Stop() error {
	if t.cancel != nil {
		t.cancel()
		<-t.done
	}
	return nil
}

// Events returns the channel on which TriggerEvents are delivered.
// The channel is closed after Stop returns.
func (t *InboxSignalTrigger) Events() <-chan TriggerEvent {
	return t.events
}
