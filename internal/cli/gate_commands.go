// Package cli implements the headless CLI subcommand for the drem
// orchestrator. It provides programmatic read/write access to the
// orchestrator database for C-Suite agents and temp workers.
package cli

import (
	"fmt"
	"io"

	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/tui"
)

// gateHandlers holds a reference to the database and TUIOrchestrator
// for executing gate action commands (approve, reject, answer, pass, fail).
type gateHandlers struct {
	db   *gorm.DB
	orch tui.TUIOrchestrator
}

// newGateHandlers creates a new gateHandlers instance.
func newGateHandlers(db *gorm.DB, orch tui.TUIOrchestrator) *gateHandlers {
	return &gateHandlers{
		db:   db,
		orch: orch,
	}
}

// handleApprove processes the approve command.
// Approves a task at either plan_review or test_review gate.
// Usage: drem cli approve <task-id>
func (gh *gateHandlers) handleApprove(args []string, w io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: drem cli approve <task-id>")
	}

	taskPrefix := args[0]
	task, err := resolveTaskByPrefix(gh.db, taskPrefix)
	if err != nil {
		return err
	}

	// Check if task is in a valid state for approval
	switch task.Status {
	case model.StatusPlanReview:
		return gh.orch.HandlePlanApproved(task.ID)
	case model.StatusTestReview:
		return gh.orch.HandleTestReviewApproved(task.ID)
	default:
		return fmt.Errorf("task %s is in wrong status %q for approval (expected plan_review or test_review)", task.ID.String()[:8], task.Status)
	}
}

// handleReject processes the reject command.
// Rejects a task at either plan_review or test_review gate.
// Usage: drem cli reject <task-id> [--reason=REASON]
func (gh *gateHandlers) handleReject(args []string, w io.Writer) error {
	reason, remaining := parseFlag(args, "reason")

	if len(remaining) == 0 {
		return fmt.Errorf("usage: drem cli reject <task-id> [--reason=REASON]")
	}

	taskPrefix := remaining[0]
	task, err := resolveTaskByPrefix(gh.db, taskPrefix)
	if err != nil {
		return err
	}

	// Check if task is in a valid state for rejection
	switch task.Status {
	case model.StatusPlanReview:
		return gh.orch.HandlePlanRejected(task.ID)
	case model.StatusTestReview:
		return gh.orch.HandleTestReviewRejected(task.ID, reason)
	default:
		return fmt.Errorf("task %s is in wrong status %q for rejection (expected plan_review or test_review)", task.ID.String()[:8], task.Status)
	}
}

// handleAnswer processes the answer command.
// Answers a clarification question for a task in needs_clarification status.
// Usage: drem cli answer <task-id> --body=BODY
func (gh *gateHandlers) handleAnswer(args []string, w io.Writer) error {
	body, remaining := parseFlag(args, "body")

	if len(remaining) == 0 {
		return fmt.Errorf("usage: drem cli answer <task-id> --body=BODY")
	}

	if body == "" {
		return fmt.Errorf("--body is required")
	}

	taskPrefix := remaining[0]
	task, err := resolveTaskByPrefix(gh.db, taskPrefix)
	if err != nil {
		return err
	}

	// Check if task is in clarification status
	if task.Status != model.StatusNeedsClarification {
		return fmt.Errorf("task %s is in wrong status %q for answering clarification (expected needs_clarification)", task.ID.String()[:8], task.Status)
	}

	return gh.orch.HandleClarificationAnswer(task.ID, body)
}

// handlePass processes the pass command.
// Passes a task in testing_ready status.
// Usage: drem cli pass <task-id>
func (gh *gateHandlers) handlePass(args []string, w io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: drem cli pass <task-id>")
	}

	taskPrefix := args[0]
	task, err := resolveTaskByPrefix(gh.db, taskPrefix)
	if err != nil {
		return err
	}

	// Check if task is in testing_ready status
	if task.Status != model.StatusTestingReady {
		return fmt.Errorf("task %s is in wrong status %q for passing (expected testing_ready)", task.ID.String()[:8], task.Status)
	}

	return gh.orch.HandleTestPassed(task.ID)
}

// handleFail processes the fail command.
// Fails a task in testing_ready status.
// Usage: drem cli fail <task-id>
func (gh *gateHandlers) handleFail(args []string, w io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: drem cli fail <task-id>")
	}

	taskPrefix := args[0]
	task, err := resolveTaskByPrefix(gh.db, taskPrefix)
	if err != nil {
		return err
	}

	// Check if task is in testing_ready status
	if task.Status != model.StatusTestingReady {
		return fmt.Errorf("task %s is in wrong status %q for failing (expected testing_ready)", task.ID.String()[:8], task.Status)
	}

	return gh.orch.HandleTestFailed(task.ID)
}

// RegisterGateCommands registers the gate action commands with the CLI Run dispatcher.
// This is called once during CLI initialization to add the 5 gate commands
// to the dispatcher. Should be called from Run() in cli.go.
//
// Registered commands:
//   - approve: approve plan_review or test_review gate
//   - reject: reject with optional feedback
//   - answer: answer clarification gate
//   - pass: pass testing gate
//   - fail: fail testing gate
func RegisterGateCommands(db *gorm.DB, orch tui.TUIOrchestrator, subcommand string, args []string, w io.Writer) (handled bool, err error) {
	gh := newGateHandlers(db, orch)

	switch subcommand {
	case "approve":
		return true, gh.handleApprove(args, w)
	case "reject":
		return true, gh.handleReject(args, w)
	case "answer":
		return true, gh.handleAnswer(args, w)
	case "pass":
		return true, gh.handlePass(args, w)
	case "fail":
		return true, gh.handleFail(args, w)
	default:
		return false, nil // Not a gate command; let caller handle
	}
}
