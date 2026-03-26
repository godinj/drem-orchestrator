package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/tui"
)

// ────────────────────────────────────────────────────────────────────────────
// Mock TUIOrchestrator for testing
// ────────────────────────────────────────────────────────────────────────────

type mockOrchestrator struct {
	approvedTaskID           uuid.UUID
	approveErr               error
	rejectedTaskID           uuid.UUID
	rejectedFeedback         string
	rejectErr                error
	answeredTaskID           uuid.UUID
	answeredAnswer           string
	answerErr                error
	passedTaskID             uuid.UUID
	passErr                  error
	failedTaskID             uuid.UUID
	failErr                  error
	testReviewApprovedTaskID uuid.UUID
	testReviewApproveErr     error
	testReviewRejectedTaskID uuid.UUID
	testReviewRejectedFB     string
	testReviewRejectErr      error
}

func (m *mockOrchestrator) HandlePlanApproved(taskID uuid.UUID) error {
	m.approvedTaskID = taskID
	return m.approveErr
}

func (m *mockOrchestrator) HandlePlanRejected(taskID uuid.UUID) error {
	m.rejectedTaskID = taskID
	return m.rejectErr
}

func (m *mockOrchestrator) HandleTestReviewApproved(taskID uuid.UUID) error {
	m.testReviewApprovedTaskID = taskID
	return m.testReviewApproveErr
}

func (m *mockOrchestrator) HandleTestReviewRejected(taskID uuid.UUID, feedback string) error {
	m.testReviewRejectedTaskID = taskID
	m.testReviewRejectedFB = feedback
	return m.testReviewRejectErr
}

func (m *mockOrchestrator) HandleTestPassed(taskID uuid.UUID) error {
	m.passedTaskID = taskID
	return m.passErr
}

func (m *mockOrchestrator) HandleTestFailed(taskID uuid.UUID) error {
	m.failedTaskID = taskID
	return m.failErr
}

func (m *mockOrchestrator) HandleClarificationAnswer(taskID uuid.UUID, answer string) error {
	m.answeredTaskID = taskID
	m.answeredAnswer = answer
	return m.answerErr
}

func (m *mockOrchestrator) PauseTask(taskID uuid.UUID) error {
	return nil
}

func (m *mockOrchestrator) ResumeTask(taskID uuid.UUID) error {
	return nil
}

func (m *mockOrchestrator) RetryTask(taskID uuid.UUID) error {
	return nil
}

func (m *mockOrchestrator) CreateTask(title, description string, priority int) (*model.Task, error) {
	return nil, nil
}

func (m *mockOrchestrator) DeleteTask(taskID uuid.UUID) error {
	return nil
}

func (m *mockOrchestrator) AddComment(taskID uuid.UUID, author, body string) error {
	return nil
}

func (m *mockOrchestrator) DeleteComment(commentID uuid.UUID) error {
	return nil
}

func (m *mockOrchestrator) DeletePlanStep(taskID uuid.UUID, stepIndex int) error {
	return nil
}

func (m *mockOrchestrator) SpawnSupervisorSession(taskID uuid.UUID) (string, error) {
	return "", nil
}

func (m *mockOrchestrator) SpawnReviewerSession(taskID uuid.UUID) (string, error) {
	return "", nil
}

func (m *mockOrchestrator) SpawnFixerSession(taskID uuid.UUID) (string, error) {
	return "", nil
}

func (m *mockOrchestrator) GetAgentOutput(agentID uuid.UUID) (string, error) {
	return "", nil
}

func (m *mockOrchestrator) ReapOrphanedSessions() (int, error) {
	return 0, nil
}

func (m *mockOrchestrator) IntegrationWorktreePath(taskID uuid.UUID) string {
	return ""
}

var _ tui.TUIOrchestrator = (*mockOrchestrator)(nil)

// ────────────────────────────────────────────────────────────────────────────
// Helper functions for tests
// ────────────────────────────────────────────────────────────────────────────

// createApproveCmd creates a command invocation for the approve command.
func createApproveCmd(taskIDPrefix string) []string {
	return []string{"approve", taskIDPrefix}
}

// createRejectCmd creates a command invocation for the reject command.
func createRejectCmd(taskIDPrefix string, reason string) []string {
	if reason == "" {
		return []string{"reject", taskIDPrefix}
	}
	return []string{"reject", taskIDPrefix, "--reason=" + reason}
}

// createAnswerCmd creates a command invocation for the answer command.
func createAnswerCmd(taskIDPrefix string, body string) []string {
	if body == "" {
		return []string{"answer", taskIDPrefix}
	}
	return []string{"answer", taskIDPrefix, "--body=" + body}
}

// createPassCmd creates a command invocation for the pass command.
func createPassCmd(taskIDPrefix string) []string {
	return []string{"pass", taskIDPrefix}
}

// createFailCmd creates a command invocation for the fail command.
func createFailCmd(taskIDPrefix string) []string {
	return []string{"fail", taskIDPrefix}
}

// ────────────────────────────────────────────────────────────────────────────
// APPROVE Command Tests
// ────────────────────────────────────────────────────────────────────────────

func TestApproveCommandPlanReviewStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusPlanReview)

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Approve a task in plan_review status
	var buf bytes.Buffer
	err := gateHandlers.handleApprove([]string{task.ID.String()[:8]}, &buf)

	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	if mock.approvedTaskID != task.ID {
		t.Errorf("expected HandlePlanApproved called with taskID %s, got %s", task.ID, mock.approvedTaskID)
	}
}

func TestApproveCommandTestReviewStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusTestReview)

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Approve a task in test_review status
	var buf bytes.Buffer
	err := gateHandlers.handleApprove([]string{task.ID.String()[:8]}, &buf)

	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	if mock.testReviewApprovedTaskID != task.ID {
		t.Errorf("expected HandleTestReviewApproved called with taskID %s, got %s", task.ID, mock.testReviewApprovedTaskID)
	}
}

func TestApproveCommandWrongStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusInProgress)

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Try to approve a task NOT in a gate status
	var buf bytes.Buffer
	err := gateHandlers.handleApprove([]string{task.ID.String()[:8]}, &buf)

	if err == nil {
		t.Error("expected error when approving task in wrong status")
	}
	if !strings.Contains(err.Error(), "wrong status") && !strings.Contains(err.Error(), "cannot approve") {
		t.Errorf("expected error mentioning status, got: %v", err)
	}
}

func TestApproveCommandInvalidTaskID(t *testing.T) {
	db := testutil.NewTestDB(t)
	testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Try to approve a non-existent task
	var buf bytes.Buffer
	err := gateHandlers.handleApprove([]string{"nonexistent"}, &buf)

	if err == nil {
		t.Error("expected error when task not found")
	}
}

func TestApproveCommandMissingTaskID(t *testing.T) {
	db := testutil.NewTestDB(t)
	testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Try to approve without task ID
	var buf bytes.Buffer
	err := gateHandlers.handleApprove([]string{}, &buf)

	if err == nil {
		t.Error("expected error when task ID is missing")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// REJECT Command Tests
// ────────────────────────────────────────────────────────────────────────────

func TestRejectCommandPlanReviewStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusPlanReview)

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Reject a task in plan_review status with feedback
	var buf bytes.Buffer
	feedback := "needs more detail"
	err := gateHandlers.handleReject([]string{task.ID.String()[:8], "--reason=" + feedback}, &buf)

	if err != nil {
		t.Fatalf("reject failed: %v", err)
	}
	if mock.rejectedTaskID != task.ID {
		t.Errorf("expected HandlePlanRejected called with taskID %s, got %s", task.ID, mock.rejectedTaskID)
	}
}

func TestRejectCommandTestReviewStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusTestReview)

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Reject a task in test_review status with feedback
	var buf bytes.Buffer
	feedback := "tests incomplete"
	err := gateHandlers.handleReject([]string{task.ID.String()[:8], "--reason=" + feedback}, &buf)

	if err != nil {
		t.Fatalf("reject failed: %v", err)
	}
	if mock.testReviewRejectedTaskID != task.ID {
		t.Errorf("expected HandleTestReviewRejected called with taskID %s, got %s", task.ID, mock.testReviewRejectedTaskID)
	}
	if mock.testReviewRejectedFB != feedback {
		t.Errorf("expected feedback %q, got %q", feedback, mock.testReviewRejectedFB)
	}
}

func TestRejectCommandWithoutReason(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusPlanReview)

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Reject without feedback (should use empty string)
	var buf bytes.Buffer
	err := gateHandlers.handleReject([]string{task.ID.String()[:8]}, &buf)

	if err != nil {
		t.Fatalf("reject failed: %v", err)
	}
	if mock.rejectedTaskID != task.ID {
		t.Errorf("expected task rejected, got %s", mock.rejectedTaskID)
	}
}

func TestRejectCommandWrongStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusInProgress)

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Try to reject a task NOT in a gate status
	var buf bytes.Buffer
	err := gateHandlers.handleReject([]string{task.ID.String()[:8], "--reason=bad"}, &buf)

	if err == nil {
		t.Error("expected error when rejecting task in wrong status")
	}
}

func TestRejectCommandInvalidTaskID(t *testing.T) {
	db := testutil.NewTestDB(t)
	testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Try to reject a non-existent task
	var buf bytes.Buffer
	err := gateHandlers.handleReject([]string{"nonexistent", "--reason=bad"}, &buf)

	if err == nil {
		t.Error("expected error when task not found")
	}
}

func TestRejectCommandMissingTaskID(t *testing.T) {
	db := testutil.NewTestDB(t)
	testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Try to reject without task ID
	var buf bytes.Buffer
	err := gateHandlers.handleReject([]string{}, &buf)

	if err == nil {
		t.Error("expected error when task ID is missing")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// ANSWER Command Tests
// ────────────────────────────────────────────────────────────────────────────

func TestAnswerCommandClarificationStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusNeedsClarification)

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Answer a clarification question
	var buf bytes.Buffer
	answerBody := "Here is the clarification"
	err := gateHandlers.handleAnswer([]string{task.ID.String()[:8], "--body=" + answerBody}, &buf)

	if err != nil {
		t.Fatalf("answer failed: %v", err)
	}
	if mock.answeredTaskID != task.ID {
		t.Errorf("expected HandleClarificationAnswer called with taskID %s, got %s", task.ID, mock.answeredTaskID)
	}
	if mock.answeredAnswer != answerBody {
		t.Errorf("expected answer %q, got %q", answerBody, mock.answeredAnswer)
	}
}

func TestAnswerCommandWithoutBody(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusNeedsClarification)

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Try to answer without body (should fail)
	var buf bytes.Buffer
	err := gateHandlers.handleAnswer([]string{}, &buf)

	if err == nil {
		t.Error("expected error when body is missing")
	}
}

func TestAnswerCommandWrongStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusInProgress)

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Try to answer on a task NOT in clarification status
	var buf bytes.Buffer
	err := gateHandlers.handleAnswer([]string{task.ID.String()[:8], "--body=answer"}, &buf)

	if err == nil {
		t.Error("expected error when answering task in wrong status")
	}
}

func TestAnswerCommandInvalidTaskID(t *testing.T) {
	db := testutil.NewTestDB(t)
	testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Try to answer on a non-existent task
	var buf bytes.Buffer
	err := gateHandlers.handleAnswer([]string{"nonexistent", "--body=answer"}, &buf)

	if err == nil {
		t.Error("expected error when task not found")
	}
}

func TestAnswerCommandMissingTaskID(t *testing.T) {
	db := testutil.NewTestDB(t)
	testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Try to answer without task ID
	var buf bytes.Buffer
	err := gateHandlers.handleAnswer([]string{"--body=answer"}, &buf)

	if err == nil {
		t.Error("expected error when task ID is missing")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// PASS Command Tests
// ────────────────────────────────────────────────────────────────────────────

func TestPassCommandTestingReadyStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusTestingReady)

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Pass a task in testing_ready status
	var buf bytes.Buffer
	err := gateHandlers.handlePass([]string{task.ID.String()[:8]}, &buf)

	if err != nil {
		t.Fatalf("pass failed: %v", err)
	}
	if mock.passedTaskID != task.ID {
		t.Errorf("expected HandleTestPassed called with taskID %s, got %s", task.ID, mock.passedTaskID)
	}
}

func TestPassCommandWrongStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusInProgress)

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Try to pass a task NOT in testing_ready status
	var buf bytes.Buffer
	err := gateHandlers.handlePass([]string{task.ID.String()[:8]}, &buf)

	if err == nil {
		t.Error("expected error when passing task in wrong status")
	}
}

func TestPassCommandInvalidTaskID(t *testing.T) {
	db := testutil.NewTestDB(t)
	testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Try to pass a non-existent task
	var buf bytes.Buffer
	err := gateHandlers.handlePass([]string{"nonexistent"}, &buf)

	if err == nil {
		t.Error("expected error when task not found")
	}
}

func TestPassCommandMissingTaskID(t *testing.T) {
	db := testutil.NewTestDB(t)
	testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Try to pass without task ID
	var buf bytes.Buffer
	err := gateHandlers.handlePass([]string{}, &buf)

	if err == nil {
		t.Error("expected error when task ID is missing")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// FAIL Command Tests
// ────────────────────────────────────────────────────────────────────────────

func TestFailCommandTestingReadyStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusTestingReady)

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Fail a task in testing_ready status
	var buf bytes.Buffer
	err := gateHandlers.handleFail([]string{task.ID.String()[:8]}, &buf)

	if err != nil {
		t.Fatalf("fail failed: %v", err)
	}
	if mock.failedTaskID != task.ID {
		t.Errorf("expected HandleTestFailed called with taskID %s, got %s", task.ID, mock.failedTaskID)
	}
}

func TestFailCommandWrongStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusInProgress)

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Try to fail a task NOT in testing_ready status
	var buf bytes.Buffer
	err := gateHandlers.handleFail([]string{task.ID.String()[:8]}, &buf)

	if err == nil {
		t.Error("expected error when failing task in wrong status")
	}
}

func TestFailCommandInvalidTaskID(t *testing.T) {
	db := testutil.NewTestDB(t)
	testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Try to fail a non-existent task
	var buf bytes.Buffer
	err := gateHandlers.handleFail([]string{"nonexistent"}, &buf)

	if err == nil {
		t.Error("expected error when task not found")
	}
}

func TestFailCommandMissingTaskID(t *testing.T) {
	db := testutil.NewTestDB(t)
	testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")

	mock := &mockOrchestrator{}
	gateHandlers := newGateHandlers(db, mock)

	// Try to fail without task ID
	var buf bytes.Buffer
	err := gateHandlers.handleFail([]string{}, &buf)

	if err == nil {
		t.Error("expected error when task ID is missing")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Integration Tests (Testing orchestrator handler callbacks)
// ────────────────────────────────────────────────────────────────────────────

func TestApproveIntegrationHandlerError(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusPlanReview)

	mock := &mockOrchestrator{
		approveErr: gorm.ErrRecordNotFound, // Simulate error from handler
	}
	gateHandlers := newGateHandlers(db, mock)

	// Handler error should propagate
	var buf bytes.Buffer
	err := gateHandlers.handleApprove([]string{task.ID.String()[:8]}, &buf)

	if err == nil {
		t.Error("expected error to propagate from handler")
	}
}

func TestRejectIntegrationHandlerError(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusPlanReview)

	mock := &mockOrchestrator{
		rejectErr: gorm.ErrRecordNotFound, // Simulate error from handler
	}
	gateHandlers := newGateHandlers(db, mock)

	// Handler error should propagate
	var buf bytes.Buffer
	err := gateHandlers.handleReject([]string{task.ID.String()[:8], "--reason=bad"}, &buf)

	if err == nil {
		t.Error("expected error to propagate from handler")
	}
}

func TestAnswerIntegrationHandlerError(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusNeedsClarification)

	mock := &mockOrchestrator{
		answerErr: gorm.ErrRecordNotFound, // Simulate error from handler
	}
	gateHandlers := newGateHandlers(db, mock)

	// Handler error should propagate
	var buf bytes.Buffer
	err := gateHandlers.handleAnswer([]string{task.ID.String()[:8], "--body=answer"}, &buf)

	if err == nil {
		t.Error("expected error to propagate from handler")
	}
}

func TestPassIntegrationHandlerError(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusTestingReady)

	mock := &mockOrchestrator{
		passErr: gorm.ErrRecordNotFound, // Simulate error from handler
	}
	gateHandlers := newGateHandlers(db, mock)

	// Handler error should propagate
	var buf bytes.Buffer
	err := gateHandlers.handlePass([]string{task.ID.String()[:8]}, &buf)

	if err == nil {
		t.Error("expected error to propagate from handler")
	}
}

func TestFailIntegrationHandlerError(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Test Task", model.StatusTestingReady)

	mock := &mockOrchestrator{
		failErr: gorm.ErrRecordNotFound, // Simulate error from handler
	}
	gateHandlers := newGateHandlers(db, mock)

	// Handler error should propagate
	var buf bytes.Buffer
	err := gateHandlers.handleFail([]string{task.ID.String()[:8]}, &buf)

	if err == nil {
		t.Error("expected error to propagate from handler")
	}
}
