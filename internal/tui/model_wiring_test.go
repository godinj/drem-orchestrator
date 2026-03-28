package tui

import (
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// newMockModel builds a Model wired to a mockOrchestrator for testing.
func newMockModel(t *testing.T, mock *mockOrchestrator) Model {
	t.Helper()
	return Model{
		orch:     mock,
		keys:     defaultKeyMap(),
		focus:    FocusBoard,
		create:   NewCreateModel(),
		feedback: NewFeedbackModel(""),
		board:    NewBoardModel(),
		agents:   NewAgentsModel(),
		detail:   NewDetailModel(),
		width:    120,
		height:   40,
	}
}

// ---------------------------------------------------------------------------
// Plan review: approve (key "a" on plan_review task)
// ---------------------------------------------------------------------------

func TestModelWiring_ApprovePlanReview(t *testing.T) {
	mock := &mockOrchestrator{}
	m := newMockModel(t, mock)

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "plan task", Status: model.StatusPlanReview},
	}
	m.board.cursor = 0

	// Press 'a' to initiate confirmation, then 'y' to confirm.
	result, _ := m.Update(keyMsg("a"))
	got := result.(Model)
	got.Update(keyMsg("y"))

	if mock.callCount("HandlePlanApproved") != 1 {
		t.Fatalf("expected 1 call to HandlePlanApproved, got %d", mock.callCount("HandlePlanApproved"))
	}
	if got := mock.lastCall().args[0].(uuid.UUID); got != taskID {
		t.Errorf("HandlePlanApproved called with %v, want %v", got, taskID)
	}
}

// ---------------------------------------------------------------------------
// Plan review: reject (key "r" on plan_review task)
// ---------------------------------------------------------------------------

func TestModelWiring_RejectPlanReview(t *testing.T) {
	mock := &mockOrchestrator{}
	m := newMockModel(t, mock)

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "plan task", Status: model.StatusPlanReview},
	}
	m.board.cursor = 0

	// Press 'r' to initiate confirmation, then 'y' to confirm.
	result, _ := m.Update(keyMsg("r"))
	got := result.(Model)
	got.Update(keyMsg("y"))

	if mock.callCount("HandlePlanRejected") != 1 {
		t.Fatalf("expected 1 call to HandlePlanRejected, got %d", mock.callCount("HandlePlanRejected"))
	}
}

// ---------------------------------------------------------------------------
// Test review: approve (key "a" on test_review task)
// ---------------------------------------------------------------------------

func TestModelWiring_ApproveTestReview(t *testing.T) {
	mock := &mockOrchestrator{}
	m := newMockModel(t, mock)

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "test review task", Status: model.StatusTestReview},
	}
	m.board.cursor = 0

	// Press 'a' to initiate confirmation, then 'y' to confirm.
	result, _ := m.Update(keyMsg("a"))
	got := result.(Model)
	got.Update(keyMsg("y"))

	if mock.callCount("HandleTestReviewApproved") != 1 {
		t.Fatalf("expected 1 call to HandleTestReviewApproved, got %d", mock.callCount("HandleTestReviewApproved"))
	}
}

// ---------------------------------------------------------------------------
// Testing gate: pass (key "t" on testing_ready task)
// ---------------------------------------------------------------------------

func TestModelWiring_TestPassOnTestingReady(t *testing.T) {
	mock := &mockOrchestrator{}
	m := newMockModel(t, mock)

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "test task", Status: model.StatusTestingReady},
	}
	m.board.cursor = 0

	// Press 't' to initiate confirmation, then 'y' to confirm.
	result, _ := m.Update(keyMsg("t"))
	got := result.(Model)
	got.Update(keyMsg("y"))

	if mock.callCount("HandleTestPassed") != 1 {
		t.Fatalf("expected 1 call to HandleTestPassed, got %d", mock.callCount("HandleTestPassed"))
	}
}

// ---------------------------------------------------------------------------
// Testing gate: fail (key "f" on testing_ready task)
// ---------------------------------------------------------------------------

func TestModelWiring_TestFailOnTestingReady(t *testing.T) {
	mock := &mockOrchestrator{}
	m := newMockModel(t, mock)

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "test task", Status: model.StatusTestingReady},
	}
	m.board.cursor = 0

	// Press 'f' to initiate confirmation, then 'y' to confirm.
	result, _ := m.Update(keyMsg("f"))
	got := result.(Model)
	got.Update(keyMsg("y"))

	if mock.callCount("HandleTestFailed") != 1 {
		t.Fatalf("expected 1 call to HandleTestFailed, got %d", mock.callCount("HandleTestFailed"))
	}
}

// ---------------------------------------------------------------------------
// Pause / Resume (key "p")
// ---------------------------------------------------------------------------

func TestModelWiring_PauseTask(t *testing.T) {
	mock := &mockOrchestrator{}
	m := newMockModel(t, mock)

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "active task", Status: model.StatusInProgress},
	}
	m.board.cursor = 0

	m.Update(keyMsg("p"))

	if mock.callCount("PauseTask") != 1 {
		t.Fatalf("expected 1 call to PauseTask, got %d", mock.callCount("PauseTask"))
	}
}

func TestModelWiring_ResumeTask(t *testing.T) {
	mock := &mockOrchestrator{}
	m := newMockModel(t, mock)

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "paused task", Status: model.StatusPaused},
	}
	m.board.cursor = 0

	m.Update(keyMsg("p"))

	if mock.callCount("ResumeTask") != 1 {
		t.Fatalf("expected 1 call to ResumeTask, got %d", mock.callCount("ResumeTask"))
	}
}

// ---------------------------------------------------------------------------
// Retry (key "R" on failed task)
// ---------------------------------------------------------------------------

func TestModelWiring_RetryTask(t *testing.T) {
	mock := &mockOrchestrator{}
	m := newMockModel(t, mock)

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "failed task", Status: model.StatusFailed},
	}
	m.board.cursor = 0

	m.Update(keyMsg("R"))

	if mock.callCount("RetryTask") != 1 {
		t.Fatalf("expected 1 call to RetryTask, got %d", mock.callCount("RetryTask"))
	}
}

// ---------------------------------------------------------------------------
// Error propagation: orchestrator errors set m.err
// ---------------------------------------------------------------------------

func TestModelWiring_ErrorPropagation(t *testing.T) {
	mock := &mockOrchestrator{err: fmt.Errorf("test error")}
	m := newMockModel(t, mock)

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "plan task", Status: model.StatusPlanReview},
	}
	m.board.cursor = 0

	// Press 'a' to initiate confirmation, then 'y' to confirm and trigger error.
	result, _ := m.Update(keyMsg("a"))
	got := result.(Model)
	result, _ = got.Update(keyMsg("y"))
	got = result.(Model)

	if got.err == nil {
		t.Fatal("expected error to be set on model")
	}
	if got.err.Error() != "test error" {
		t.Errorf("error = %q, want %q", got.err, "test error")
	}
}

// ---------------------------------------------------------------------------
// Delete task (key "d")
// ---------------------------------------------------------------------------

func TestModelWiring_DeleteTask(t *testing.T) {
	mock := &mockOrchestrator{}
	m := newMockModel(t, mock)

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "doomed task", Status: model.StatusBacklog},
	}
	m.board.cursor = 0

	_, cmd := m.Update(keyMsg("d"))

	// Delete runs async — execute the returned command.
	if cmd == nil {
		t.Fatal("expected non-nil cmd from delete")
	}
	msg := cmd()
	result, ok := msg.(deleteResultMsg)
	if !ok {
		t.Fatalf("expected deleteResultMsg, got %T", msg)
	}
	if result.err != nil {
		t.Errorf("unexpected error: %v", result.err)
	}
	if mock.callCount("DeleteTask") != 1 {
		t.Fatalf("expected 1 call to DeleteTask, got %d", mock.callCount("DeleteTask"))
	}
}

// ---------------------------------------------------------------------------
// IntegrationWorktreePath (key "i" for shell)
// ---------------------------------------------------------------------------

func TestModelWiring_IntegrationWorktreePath(t *testing.T) {
	mock := &mockOrchestrator{worktreePath: ""}
	m := newMockModel(t, mock)

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "task", Status: model.StatusInProgress},
	}
	m.board.cursor = 0

	result, _ := m.Update(keyMsg("i"))
	got := result.(Model)

	if mock.callCount("IntegrationWorktreePath") != 1 {
		t.Fatalf("expected 1 call to IntegrationWorktreePath, got %d", mock.callCount("IntegrationWorktreePath"))
	}
	// With empty path, model should set an error.
	if got.err == nil {
		t.Fatal("expected error for empty worktree path")
	}
}

// ---------------------------------------------------------------------------
// EventMsg: verifies that Event → EventMsg wrapping works
// ---------------------------------------------------------------------------

func TestModelWiring_EventMsg(t *testing.T) {
	mock := &mockOrchestrator{}
	m := newMockModel(t, mock)
	m.events = make(<-chan Event)

	result, cmd := m.Update(EventMsg{Type: "task_updated"})
	_ = result.(Model)

	// EventMsg should trigger a batch of refreshData + listenForEvents.
	if cmd == nil {
		t.Fatal("expected non-nil cmd from EventMsg")
	}
}

// ---------------------------------------------------------------------------
// NewModel wires interface correctly
// ---------------------------------------------------------------------------

func TestNewModel_AcceptsTUIOrchestrator(t *testing.T) {
	mock := &mockOrchestrator{}
	events := make(chan Event)
	defer close(events)

	m := NewModel(nil, mock, nil, uuid.New(), events, "", nil, nil, nil)

	// Verify the interface is stored.
	if m.orch == nil {
		t.Fatal("expected orch to be set")
	}

	// Verify we can call through the interface.
	taskID := uuid.New()
	_ = m.orch.HandlePlanApproved(taskID)

	if mock.callCount("HandlePlanApproved") != 1 {
		t.Fatal("expected HandlePlanApproved to be called through interface")
	}
}

// ---------------------------------------------------------------------------
// Approve from detail panel (key "a" in FocusDetail)
// ---------------------------------------------------------------------------

func TestModelWiring_ApproveFromDetailPanel(t *testing.T) {
	mock := &mockOrchestrator{}
	m := newMockModel(t, mock)
	m.focus = FocusDetail

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "plan task", Status: model.StatusPlanReview},
	}
	m.board.cursor = 0
	m.detail.task = &m.board.tasks[0]

	// Press 'a' to initiate confirmation, then 'y' to confirm.
	result, _ := m.Update(keyMsg("a"))
	got := result.(Model)
	got.Update(keyMsg("y"))

	if mock.callCount("HandlePlanApproved") != 1 {
		t.Fatalf("expected HandlePlanApproved from detail panel, got %d calls", mock.callCount("HandlePlanApproved"))
	}
}

// ---------------------------------------------------------------------------
// Reap (key "C") dispatches async
// ---------------------------------------------------------------------------

func TestModelWiring_Reap(t *testing.T) {
	mock := &mockOrchestrator{reapedCount: 3}
	m := newMockModel(t, mock)

	// Reap needs at least one task on the board for the board focus to accept keys.
	m.board.tasks = []model.Task{
		{ID: uuid.New(), Title: "task", Status: model.StatusInProgress},
	}
	m.board.cursor = 0

	_, cmd := m.Update(keyMsg("C"))

	if cmd == nil {
		t.Fatal("expected non-nil cmd from reap")
	}
	msg := cmd()
	result, ok := msg.(reapMsg)
	if !ok {
		t.Fatalf("expected reapMsg, got %T", msg)
	}
	if result.reaped != 3 {
		t.Errorf("reaped = %d, want 3", result.reaped)
	}
	if mock.callCount("ReapOrphanedSessions") != 1 {
		t.Fatalf("expected 1 call to ReapOrphanedSessions, got %d", mock.callCount("ReapOrphanedSessions"))
	}
}

// ---------------------------------------------------------------------------
// SpawnSupervisorSession (key "S") dispatches async
// ---------------------------------------------------------------------------

func TestModelWiring_SpawnSupervisor(t *testing.T) {
	mock := &mockOrchestrator{sessionName: "drem/sup-1234"}
	m := newMockModel(t, mock)

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "task", Status: model.StatusInProgress},
	}
	m.board.cursor = 0

	_, cmd := m.Update(keyMsg("S"))

	if cmd == nil {
		t.Fatal("expected non-nil cmd from supervisor spawn")
	}
	msg := cmd()
	result, ok := msg.(supervisorSpawnedMsg)
	if !ok {
		t.Fatalf("expected supervisorSpawnedMsg, got %T", msg)
	}
	if result.sessionName != "drem/sup-1234" {
		t.Errorf("sessionName = %q, want %q", result.sessionName, "drem/sup-1234")
	}
}

// ---------------------------------------------------------------------------
// Test review reject opens feedback dialog (does not call mock directly)
// ---------------------------------------------------------------------------

func TestModelWiring_TestReviewRejectOpensFeedback(t *testing.T) {
	mock := &mockOrchestrator{}
	m := newMockModel(t, mock)

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "test review task", Status: model.StatusTestReview},
	}
	m.board.cursor = 0

	result, _ := m.Update(keyMsg("r"))
	got := result.(Model)

	// Reject on test_review should open feedback dialog, not call the mock.
	if got.focus != FocusFeedback {
		t.Errorf("focus = %v, want FocusFeedback", got.focus)
	}
	if got.feedbackAction != feedbackTestReviewReject {
		t.Errorf("feedbackAction = %v, want feedbackTestReviewReject", got.feedbackAction)
	}
	if mock.callCount("HandleTestReviewRejected") != 0 {
		t.Error("HandleTestReviewRejected should not be called before feedback is submitted")
	}
}

// ---------------------------------------------------------------------------
// Feedback submit: test review reject with feedback body
// ---------------------------------------------------------------------------

func TestModelWiring_FeedbackSubmit_TestReviewReject(t *testing.T) {
	mock := &mockOrchestrator{}
	m := newMockModel(t, mock)
	m.focus = FocusFeedback
	m.feedbackAction = feedbackTestReviewReject

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "test review task", Status: model.StatusTestReview},
	}
	m.board.cursor = 0

	// Set feedback text.
	m.feedback.Show()
	m.feedback.input.SetValue("needs more edge cases")

	m.Update(keyMsg("enter"))

	if mock.callCount("HandleTestReviewRejected") != 1 {
		t.Fatalf("expected 1 call to HandleTestReviewRejected, got %d", mock.callCount("HandleTestReviewRejected"))
	}
	call := mock.lastCall()
	if call.args[0].(uuid.UUID) != taskID {
		t.Errorf("taskID = %v, want %v", call.args[0], taskID)
	}
	if call.args[1].(string) != "needs more edge cases" {
		t.Errorf("feedback = %q, want %q", call.args[1], "needs more edge cases")
	}
}

// ---------------------------------------------------------------------------
// Feedback submit: add comment
// ---------------------------------------------------------------------------

func TestModelWiring_FeedbackSubmit_AddComment(t *testing.T) {
	mock := &mockOrchestrator{}
	m := newMockModel(t, mock)
	m.focus = FocusFeedback
	m.feedbackAction = feedbackAddComment

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "plan review task", Status: model.StatusPlanReview},
	}
	m.board.cursor = 0

	m.feedback.Show()
	m.feedback.input.SetValue("looks good")

	m.Update(keyMsg("enter"))

	if mock.callCount("AddComment") != 1 {
		t.Fatalf("expected 1 call to AddComment, got %d", mock.callCount("AddComment"))
	}
}

// ---------------------------------------------------------------------------
// Feedback submit: clarification answer
// ---------------------------------------------------------------------------

func TestModelWiring_FeedbackSubmit_ClarificationAnswer(t *testing.T) {
	mock := &mockOrchestrator{}
	m := newMockModel(t, mock)
	m.focus = FocusFeedback
	m.feedbackAction = feedbackClarificationAnswer

	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "clarification task", Status: model.StatusNeedsClarification},
	}
	m.board.cursor = 0

	m.feedback.Show()
	m.feedback.input.SetValue("the answer is 42")

	m.Update(keyMsg("enter"))

	if mock.callCount("HandleClarificationAnswer") != 1 {
		t.Fatalf("expected 1 call to HandleClarificationAnswer, got %d", mock.callCount("HandleClarificationAnswer"))
	}
	// Clarification answer also saves as a comment.
	if mock.callCount("AddComment") != 1 {
		t.Fatalf("expected 1 call to AddComment for clarification, got %d", mock.callCount("AddComment"))
	}
}
