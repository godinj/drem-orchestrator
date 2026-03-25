package tui

import (
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// mockOrchestrator is a test double for TUIOrchestrator that records method
// calls and returns configurable results.
type mockOrchestrator struct {
	// call tracking
	calls []mockCall

	// configurable return values
	err          error
	taskResult   *model.Task
	sessionName  string
	agentOutput  string
	worktreePath string
	reapedCount  int
}

// mockCall records a single method invocation for assertion.
type mockCall struct {
	method string
	args   []any
}

// compile-time assertion
var _ TUIOrchestrator = (*mockOrchestrator)(nil)

func (m *mockOrchestrator) record(method string, args ...any) {
	m.calls = append(m.calls, mockCall{method: method, args: args})
}

func (m *mockOrchestrator) HandlePlanApproved(taskID uuid.UUID) error {
	m.record("HandlePlanApproved", taskID)
	return m.err
}

func (m *mockOrchestrator) HandlePlanRejected(taskID uuid.UUID) error {
	m.record("HandlePlanRejected", taskID)
	return m.err
}

func (m *mockOrchestrator) HandleTestReviewApproved(taskID uuid.UUID) error {
	m.record("HandleTestReviewApproved", taskID)
	return m.err
}

func (m *mockOrchestrator) HandleTestReviewRejected(taskID uuid.UUID, feedback string) error {
	m.record("HandleTestReviewRejected", taskID, feedback)
	return m.err
}

func (m *mockOrchestrator) HandleTestPassed(taskID uuid.UUID) error {
	m.record("HandleTestPassed", taskID)
	return m.err
}

func (m *mockOrchestrator) HandleTestFailed(taskID uuid.UUID) error {
	m.record("HandleTestFailed", taskID)
	return m.err
}

func (m *mockOrchestrator) HandleClarificationAnswer(taskID uuid.UUID, answer string) error {
	m.record("HandleClarificationAnswer", taskID, answer)
	return m.err
}

func (m *mockOrchestrator) PauseTask(taskID uuid.UUID) error {
	m.record("PauseTask", taskID)
	return m.err
}

func (m *mockOrchestrator) ResumeTask(taskID uuid.UUID) error {
	m.record("ResumeTask", taskID)
	return m.err
}

func (m *mockOrchestrator) RetryTask(taskID uuid.UUID) error {
	m.record("RetryTask", taskID)
	return m.err
}

func (m *mockOrchestrator) CreateTask(title, description string, priority int) (*model.Task, error) {
	m.record("CreateTask", title, description, priority)
	return m.taskResult, m.err
}

func (m *mockOrchestrator) DeleteTask(taskID uuid.UUID) error {
	m.record("DeleteTask", taskID)
	return m.err
}

func (m *mockOrchestrator) AddComment(taskID uuid.UUID, author, body string) error {
	m.record("AddComment", taskID, author, body)
	return m.err
}

func (m *mockOrchestrator) DeleteComment(commentID uuid.UUID) error {
	m.record("DeleteComment", commentID)
	return m.err
}

func (m *mockOrchestrator) DeletePlanStep(taskID uuid.UUID, stepIndex int) error {
	m.record("DeletePlanStep", taskID, stepIndex)
	return m.err
}

func (m *mockOrchestrator) SpawnSupervisorSession(taskID uuid.UUID) (string, error) {
	m.record("SpawnSupervisorSession", taskID)
	return m.sessionName, m.err
}

func (m *mockOrchestrator) SpawnReviewerSession(taskID uuid.UUID) (string, error) {
	m.record("SpawnReviewerSession", taskID)
	return m.sessionName, m.err
}

func (m *mockOrchestrator) SpawnFixerSession(taskID uuid.UUID) (string, error) {
	m.record("SpawnFixerSession", taskID)
	return m.sessionName, m.err
}

func (m *mockOrchestrator) GetAgentOutput(agentID uuid.UUID) (string, error) {
	m.record("GetAgentOutput", agentID)
	return m.agentOutput, m.err
}

func (m *mockOrchestrator) ReapOrphanedSessions() (int, error) {
	m.record("ReapOrphanedSessions")
	return m.reapedCount, m.err
}

func (m *mockOrchestrator) IntegrationWorktreePath(taskID uuid.UUID) string {
	m.record("IntegrationWorktreePath", taskID)
	return m.worktreePath
}

// lastCall returns the most recent call, or an empty mockCall if none exist.
func (m *mockOrchestrator) lastCall() mockCall {
	if len(m.calls) == 0 {
		return mockCall{}
	}
	return m.calls[len(m.calls)-1]
}

// callCount returns how many times a method was invoked.
func (m *mockOrchestrator) callCount(method string) int {
	n := 0
	for _, c := range m.calls {
		if c.method == method {
			n++
		}
	}
	return n
}
