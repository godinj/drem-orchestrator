package tui

import (
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// Event is the TUI-local event type, decoupled from the orchestrator package.
// The caller bridges orchestrator.Event → tui.Event when constructing the
// events channel.
type Event struct {
	Type    string
	Payload any
}

// TUIOrchestrator defines the subset of orchestrator functionality consumed by
// the TUI. The interface lives at the consumption site (tui package) per the
// architecture constitution's "Interfaces at consumption sites" rule.
//
// Implementations:
//   - *orchestrator.Orchestrator (production)
//   - mockOrchestrator (tests)
type TUIOrchestrator interface {
	// --- Plan review ---
	HandlePlanApproved(taskID uuid.UUID) error
	HandlePlanRejected(taskID uuid.UUID) error

	// --- Test review ---
	HandleTestReviewApproved(taskID uuid.UUID) error
	HandleTestReviewRejected(taskID uuid.UUID, feedback string) error

	// --- Testing gate ---
	HandleTestPassed(taskID uuid.UUID) error
	HandleTestFailed(taskID uuid.UUID) error

	// --- Clarification ---
	HandleClarificationAnswer(taskID uuid.UUID, answer string) error

	// --- Task lifecycle ---
	PauseTask(taskID uuid.UUID) error
	ResumeTask(taskID uuid.UUID) error
	RetryTask(taskID uuid.UUID) error
	CreateTask(title, description string, priority int) (*model.Task, error)
	DeleteTask(taskID uuid.UUID) error

	// --- Comments ---
	AddComment(taskID uuid.UUID, author, body string) error
	DeleteComment(commentID uuid.UUID) error

	// --- Plan editing ---
	DeletePlanStep(taskID uuid.UUID, stepIndex int) error

	// --- Agent sessions ---
	SpawnSupervisorSession(taskID uuid.UUID) (string, error)
	SpawnReviewerSession(taskID uuid.UUID) (string, error)
	SpawnFixerSession(taskID uuid.UUID) (string, error)
	GetAgentOutput(agentID uuid.UUID) (string, error)
	ReapOrphanedSessions() (int, error)

	// --- Worktree ---
	IntegrationWorktreePath(taskID uuid.UUID) string
}
