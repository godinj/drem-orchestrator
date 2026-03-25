package tui

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// newGateTestModel builds a Model with a mock orchestrator and a single task at
// the given status for gate action testing.
func newGateTestModel(t *testing.T, status model.TaskStatus) (Model, *mockOrchestrator, uuid.UUID) {
	t.Helper()
	mock := &mockOrchestrator{}
	taskID := uuid.New()

	m := newKeyTestModel(t, FocusBoard)
	m.orch = mock
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "Test Task", Status: status},
	}
	m.board.cursor = 0
	m.detail.task = &m.board.tasks[0]

	return m, mock, taskID
}

// ---------------------------------------------------------------------------
// Approve shows confirmation (does not execute immediately)
// ---------------------------------------------------------------------------

func TestApproveAtPlanReview_ShowsConfirmation(t *testing.T) {
	m, mock, _ := newGateTestModel(t, model.StatusPlanReview)

	result, cmd := m.Update(keyMsg("a"))
	got := result.(Model)

	if got.confirm != confirmPlanApprove {
		t.Errorf("confirm = %d, want confirmPlanApprove (%d)", got.confirm, confirmPlanApprove)
	}
	if cmd != nil {
		t.Error("approve should not produce a cmd before confirmation")
	}
	if mock.callCount("HandlePlanApproved") != 0 {
		t.Error("orchestrator should NOT be called before confirmation")
	}
}

func TestApproveAtTestReview_ShowsConfirmation(t *testing.T) {
	m, mock, _ := newGateTestModel(t, model.StatusTestReview)

	result, cmd := m.Update(keyMsg("a"))
	got := result.(Model)

	if got.confirm != confirmTestReviewApprove {
		t.Errorf("confirm = %d, want confirmTestReviewApprove (%d)", got.confirm, confirmTestReviewApprove)
	}
	if cmd != nil {
		t.Error("approve should not produce a cmd before confirmation")
	}
	if mock.callCount("HandleTestReviewApproved") != 0 {
		t.Error("orchestrator should NOT be called before confirmation")
	}
}

func TestApproveAtTestingReady_ShowsConfirmation(t *testing.T) {
	m, mock, _ := newGateTestModel(t, model.StatusTestingReady)

	result, cmd := m.Update(keyMsg("a"))
	got := result.(Model)

	if got.confirm != confirmTestPass {
		t.Errorf("confirm = %d, want confirmTestPass (%d)", got.confirm, confirmTestPass)
	}
	if cmd != nil {
		t.Error("approve should not produce a cmd before confirmation")
	}
	if mock.callCount("HandleTestPassed") != 0 {
		t.Error("orchestrator should NOT be called before confirmation")
	}
}

// ---------------------------------------------------------------------------
// Reject shows confirmation
// ---------------------------------------------------------------------------

func TestRejectAtPlanReview_ShowsConfirmation(t *testing.T) {
	m, mock, _ := newGateTestModel(t, model.StatusPlanReview)

	result, cmd := m.Update(keyMsg("r"))
	got := result.(Model)

	if got.confirm != confirmPlanReject {
		t.Errorf("confirm = %d, want confirmPlanReject (%d)", got.confirm, confirmPlanReject)
	}
	if cmd != nil {
		t.Error("reject should not produce a cmd before confirmation")
	}
	if mock.callCount("HandlePlanRejected") != 0 {
		t.Error("orchestrator should NOT be called before confirmation")
	}
}

func TestRejectAtTestReview_OpensFeedbackDialog(t *testing.T) {
	m, _, _ := newGateTestModel(t, model.StatusTestReview)

	result, _ := m.Update(keyMsg("r"))
	got := result.(Model)

	// Test review rejection uses feedback dialog, not confirmation.
	if got.confirm != confirmNone {
		t.Errorf("confirm = %d, want confirmNone (feedback dialog instead)", got.confirm)
	}
	if got.focus != FocusFeedback {
		t.Errorf("focus = %d, want FocusFeedback (%d)", got.focus, FocusFeedback)
	}
	if got.feedbackAction != feedbackTestReviewReject {
		t.Errorf("feedbackAction = %d, want feedbackTestReviewReject", got.feedbackAction)
	}
}

func TestRejectAtTestingReady_ShowsConfirmation(t *testing.T) {
	m, mock, _ := newGateTestModel(t, model.StatusTestingReady)

	result, cmd := m.Update(keyMsg("r"))
	got := result.(Model)

	if got.confirm != confirmTestFail {
		t.Errorf("confirm = %d, want confirmTestFail (%d)", got.confirm, confirmTestFail)
	}
	if cmd != nil {
		t.Error("reject should not produce a cmd before confirmation")
	}
	if mock.callCount("HandleTestFailed") != 0 {
		t.Error("orchestrator should NOT be called before confirmation")
	}
}

// ---------------------------------------------------------------------------
// TestPass / TestFail show confirmation
// ---------------------------------------------------------------------------

func TestTestPassAtTestingReady_ShowsConfirmation(t *testing.T) {
	m, mock, _ := newGateTestModel(t, model.StatusTestingReady)

	result, cmd := m.Update(keyMsg("t"))
	got := result.(Model)

	if got.confirm != confirmTestPass {
		t.Errorf("confirm = %d, want confirmTestPass (%d)", got.confirm, confirmTestPass)
	}
	if cmd != nil {
		t.Error("test pass should not produce a cmd before confirmation")
	}
	if mock.callCount("HandleTestPassed") != 0 {
		t.Error("orchestrator should NOT be called before confirmation")
	}
}

func TestTestFailAtTestingReady_ShowsConfirmation(t *testing.T) {
	m, mock, _ := newGateTestModel(t, model.StatusTestingReady)

	result, cmd := m.Update(keyMsg("f"))
	got := result.(Model)

	if got.confirm != confirmTestFail {
		t.Errorf("confirm = %d, want confirmTestFail (%d)", got.confirm, confirmTestFail)
	}
	if cmd != nil {
		t.Error("test fail should not produce a cmd before confirmation")
	}
	if mock.callCount("HandleTestFailed") != 0 {
		t.Error("orchestrator should NOT be called before confirmation")
	}
}

// ---------------------------------------------------------------------------
// Confirm yes → executes action
// ---------------------------------------------------------------------------

func TestConfirmYes_PlanApprove_CallsOrchestrator(t *testing.T) {
	m, mock, taskID := newGateTestModel(t, model.StatusPlanReview)

	// Press 'a' to set confirmation.
	result, _ := m.Update(keyMsg("a"))
	got := result.(Model)

	// Press 'y' to confirm.
	result, cmd := got.Update(keyMsg("y"))
	got = result.(Model)

	if got.confirm != confirmNone {
		t.Error("confirm should be cleared after confirmation")
	}
	if mock.callCount("HandlePlanApproved") != 1 {
		t.Errorf("HandlePlanApproved call count = %d, want 1", mock.callCount("HandlePlanApproved"))
	}
	if mock.lastCall().args[0] != taskID {
		t.Errorf("HandlePlanApproved called with wrong task ID")
	}
	if cmd == nil {
		t.Error("confirmed action should return a refresh cmd")
	}
}

func TestConfirmYes_PlanReject_CallsOrchestrator(t *testing.T) {
	m, mock, taskID := newGateTestModel(t, model.StatusPlanReview)

	result, _ := m.Update(keyMsg("r"))
	got := result.(Model)

	result, cmd := got.Update(keyMsg("y"))
	got = result.(Model)

	if got.confirm != confirmNone {
		t.Error("confirm should be cleared after confirmation")
	}
	if mock.callCount("HandlePlanRejected") != 1 {
		t.Errorf("HandlePlanRejected call count = %d, want 1", mock.callCount("HandlePlanRejected"))
	}
	if mock.lastCall().args[0] != taskID {
		t.Errorf("HandlePlanRejected called with wrong task ID")
	}
	if cmd == nil {
		t.Error("confirmed action should return a refresh cmd")
	}
}

func TestConfirmYes_TestPass_CallsOrchestrator(t *testing.T) {
	m, mock, taskID := newGateTestModel(t, model.StatusTestingReady)

	result, _ := m.Update(keyMsg("a"))
	got := result.(Model)

	result, cmd := got.Update(keyMsg("y"))
	got = result.(Model)

	if mock.callCount("HandleTestPassed") != 1 {
		t.Errorf("HandleTestPassed call count = %d, want 1", mock.callCount("HandleTestPassed"))
	}
	if mock.lastCall().args[0] != taskID {
		t.Errorf("HandleTestPassed called with wrong task ID")
	}
	if cmd == nil {
		t.Error("confirmed action should return a refresh cmd")
	}
}

func TestConfirmYes_TestFail_CallsOrchestrator(t *testing.T) {
	m, mock, taskID := newGateTestModel(t, model.StatusTestingReady)

	result, _ := m.Update(keyMsg("r"))
	got := result.(Model)

	result, cmd := got.Update(keyMsg("y"))
	got = result.(Model)

	if mock.callCount("HandleTestFailed") != 1 {
		t.Errorf("HandleTestFailed call count = %d, want 1", mock.callCount("HandleTestFailed"))
	}
	if mock.lastCall().args[0] != taskID {
		t.Errorf("HandleTestFailed called with wrong task ID")
	}
	if cmd == nil {
		t.Error("confirmed action should return a refresh cmd")
	}
}

func TestConfirmYes_TestReviewApprove_CallsOrchestrator(t *testing.T) {
	m, mock, taskID := newGateTestModel(t, model.StatusTestReview)

	result, _ := m.Update(keyMsg("a"))
	got := result.(Model)

	result, cmd := got.Update(keyMsg("y"))
	got = result.(Model)

	if mock.callCount("HandleTestReviewApproved") != 1 {
		t.Errorf("HandleTestReviewApproved call count = %d, want 1", mock.callCount("HandleTestReviewApproved"))
	}
	if mock.lastCall().args[0] != taskID {
		t.Errorf("HandleTestReviewApproved called with wrong task ID")
	}
	if cmd == nil {
		t.Error("confirmed action should return a refresh cmd")
	}
}

// ---------------------------------------------------------------------------
// Cancel confirmation (esc and n)
// ---------------------------------------------------------------------------

func TestConfirmEsc_CancelsConfirmation(t *testing.T) {
	m, mock, _ := newGateTestModel(t, model.StatusPlanReview)

	result, _ := m.Update(keyMsg("a"))
	got := result.(Model)

	result, cmd := got.Update(keyMsg("esc"))
	got = result.(Model)

	if got.confirm != confirmNone {
		t.Error("esc should clear confirmation")
	}
	if mock.callCount("HandlePlanApproved") != 0 {
		t.Error("orchestrator should NOT be called after cancellation")
	}
	if cmd != nil {
		t.Error("cancelled confirmation should return nil cmd")
	}
}

func TestConfirmN_CancelsConfirmation(t *testing.T) {
	m, mock, _ := newGateTestModel(t, model.StatusPlanReview)

	result, _ := m.Update(keyMsg("a"))
	got := result.(Model)

	result, cmd := got.Update(keyMsg("n"))
	got = result.(Model)

	if got.confirm != confirmNone {
		t.Error("n should clear confirmation")
	}
	if mock.callCount("HandlePlanApproved") != 0 {
		t.Error("orchestrator should NOT be called after cancellation")
	}
	if cmd != nil {
		t.Error("cancelled confirmation should return nil cmd")
	}
}

// ---------------------------------------------------------------------------
// Confirmation intercepts unrelated keys
// ---------------------------------------------------------------------------

func TestConfirmMode_IgnoresOtherKeys(t *testing.T) {
	m, mock, _ := newGateTestModel(t, model.StatusPlanReview)

	result, _ := m.Update(keyMsg("a"))
	got := result.(Model)

	// Press various unrelated keys.
	for _, key := range []string{"j", "k", "tab", "a", "r", "?", " "} {
		result, cmd := got.Update(keyMsg(key))
		got = result.(Model)

		if got.confirm != confirmPlanApprove {
			t.Errorf("key %q should not change confirm state", key)
		}
		if cmd != nil {
			t.Errorf("key %q during confirmation should return nil cmd", key)
		}
	}

	if mock.callCount("HandlePlanApproved") != 0 {
		t.Error("no orchestrator calls should have been made")
	}
}

// ---------------------------------------------------------------------------
// No-op when not on a gate task
// ---------------------------------------------------------------------------

func TestApproveOnNonGateTask_NoConfirmation(t *testing.T) {
	nonGateStatuses := []model.TaskStatus{
		model.StatusBacklog,
		model.StatusPlanning,
		model.StatusInProgress,
		model.StatusMerging,
		model.StatusPaused,
		model.StatusDone,
		model.StatusFailed,
		model.StatusRejected,
	}

	for _, status := range nonGateStatuses {
		t.Run(string(status), func(t *testing.T) {
			m, _, _ := newGateTestModel(t, status)

			result, cmd := m.Update(keyMsg("a"))
			got := result.(Model)

			if got.confirm != confirmNone {
				t.Errorf("approve on %s should not set confirmation", status)
			}
			if cmd != nil {
				t.Errorf("approve on %s should return nil cmd", status)
			}
		})
	}
}

func TestRejectOnNonGateTask_NoConfirmation(t *testing.T) {
	nonGateStatuses := []model.TaskStatus{
		model.StatusBacklog,
		model.StatusPlanning,
		model.StatusInProgress,
		model.StatusMerging,
		model.StatusPaused,
		model.StatusDone,
		model.StatusFailed,
		model.StatusRejected,
	}

	for _, status := range nonGateStatuses {
		t.Run(string(status), func(t *testing.T) {
			m, _, _ := newGateTestModel(t, status)

			result, cmd := m.Update(keyMsg("r"))
			got := result.(Model)

			if got.confirm != confirmNone {
				t.Errorf("reject on %s should not set confirmation", status)
			}
			if cmd != nil {
				t.Errorf("reject on %s should return nil cmd", status)
			}
		})
	}
}

func TestApproveWithNoSelection_NoConfirmation(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	m.orch = &mockOrchestrator{}
	// No tasks on the board.

	result, cmd := m.Update(keyMsg("a"))
	got := result.(Model)

	if got.confirm != confirmNone {
		t.Error("approve with no selection should not set confirmation")
	}
	if cmd != nil {
		t.Error("approve with no selection should return nil cmd")
	}
}

// ---------------------------------------------------------------------------
// Confirmation prompt renders in view
// ---------------------------------------------------------------------------

func TestConfirmPrompt_RenderedInView(t *testing.T) {
	m, _, _ := newGateTestModel(t, model.StatusPlanReview)
	m.width = 120
	m.height = 40
	m.updatePanelSizes()

	// Set confirmation.
	result, _ := m.Update(keyMsg("a"))
	got := result.(Model)

	view := got.View()
	if !strings.Contains(view, "Confirm") {
		t.Error("view should contain 'Confirm' when confirmation is pending")
	}
	if !strings.Contains(view, "Approve plan") {
		t.Error("view should contain 'Approve plan' label")
	}
	if !strings.Contains(view, "Test Task") {
		t.Error("view should contain the task title")
	}
	if !strings.Contains(view, "[y]es") {
		t.Error("view should contain '[y]es' hint")
	}
	if !strings.Contains(view, "[n]o") {
		t.Error("view should contain '[n]o' hint")
	}
}

func TestConfirmPrompt_NotRenderedWhenInactive(t *testing.T) {
	m, _, _ := newGateTestModel(t, model.StatusPlanReview)
	m.width = 120
	m.height = 40
	m.updatePanelSizes()

	// No confirmation set.
	view := m.View()
	if strings.Contains(view, "Confirm:") {
		t.Error("view should NOT contain 'Confirm:' when no confirmation is pending")
	}
}

// ---------------------------------------------------------------------------
// Confirmation from detail panel
// ---------------------------------------------------------------------------

func TestApproveFromDetailPanel_ShowsConfirmation(t *testing.T) {
	m, mock, _ := newGateTestModel(t, model.StatusPlanReview)
	m.focus = FocusDetail

	result, cmd := m.Update(keyMsg("a"))
	got := result.(Model)

	if got.confirm != confirmPlanApprove {
		t.Errorf("confirm = %d, want confirmPlanApprove from detail panel", got.confirm)
	}
	if cmd != nil {
		t.Error("approve from detail should not produce a cmd before confirmation")
	}
	if mock.callCount("HandlePlanApproved") != 0 {
		t.Error("orchestrator should NOT be called before confirmation from detail")
	}

	// Confirm from detail.
	result, cmd = got.Update(keyMsg("y"))
	got = result.(Model)

	if mock.callCount("HandlePlanApproved") != 1 {
		t.Error("HandlePlanApproved should be called after confirmation from detail")
	}
	if cmd == nil {
		t.Error("confirmed action should return a refresh cmd")
	}
}

// ---------------------------------------------------------------------------
// confirmActionLabel tests
// ---------------------------------------------------------------------------

func TestConfirmActionLabel(t *testing.T) {
	tests := []struct {
		action confirmAction
		want   string
	}{
		{confirmPlanApprove, "Approve plan"},
		{confirmPlanReject, "Reject plan"},
		{confirmTestReviewApprove, "Approve test review"},
		{confirmTestPass, "Pass testing"},
		{confirmTestFail, "Fail testing"},
		{confirmNone, ""},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := confirmActionLabel(tt.action)
			if got != tt.want {
				t.Errorf("confirmActionLabel(%d) = %q, want %q", tt.action, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Help overlay shows confirmation bindings
// ---------------------------------------------------------------------------

func TestHelpOverlay_ShowsConfirmBindings(t *testing.T) {
	m, _, _ := newGateTestModel(t, model.StatusPlanReview)
	m.width = 120
	m.height = 40
	m.updatePanelSizes()

	// Set confirmation.
	result, _ := m.Update(keyMsg("a"))
	got := result.(Model)

	// Get context actions.
	actions := got.contextActions()

	// Should have confirm-specific bindings.
	var keys []string
	for _, a := range actions {
		keys = append(keys, a.Key)
	}

	foundY := false
	foundN := false
	foundEsc := false
	for _, a := range actions {
		switch a.Key {
		case "y":
			foundY = true
			if !strings.Contains(a.Desc, "Approve plan") {
				t.Errorf("y binding desc = %q, want to contain 'Approve plan'", a.Desc)
			}
		case "n":
			foundN = true
		case "esc":
			foundEsc = true
		}
	}

	if !foundY {
		t.Error("confirm mode help should include 'y' binding")
	}
	if !foundN {
		t.Error("confirm mode help should include 'n' binding")
	}
	if !foundEsc {
		t.Error("confirm mode help should include 'esc' binding")
	}
}

// ---------------------------------------------------------------------------
// Error propagation from orchestrator
// ---------------------------------------------------------------------------

func TestConfirmYes_OrchestratorError_SetsModelError(t *testing.T) {
	m, mock, _ := newGateTestModel(t, model.StatusPlanReview)
	mock.err = errForTest("plan approval failed")

	result, _ := m.Update(keyMsg("a"))
	got := result.(Model)

	result, _ = got.Update(keyMsg("y"))
	got = result.(Model)

	if got.err == nil {
		t.Error("orchestrator error should be propagated to model")
	}
	if got.err.Error() != "plan approval failed" {
		t.Errorf("err = %q, want 'plan approval failed'", got.err.Error())
	}
}
