package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ---------------------------------------------------------------------------
// Model.Update message handling tests (non-key messages)
// ---------------------------------------------------------------------------

func TestModelUpdate_WindowSizeMsg(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	msg := tea.WindowSizeMsg{Width: 200, Height: 60}

	result, cmd := m.Update(msg)
	got := result.(Model)

	if got.width != 200 {
		t.Errorf("width = %d, want 200", got.width)
	}
	if got.height != 60 {
		t.Errorf("height = %d, want 60", got.height)
	}
	if cmd != nil {
		t.Error("WindowSizeMsg should return nil cmd")
	}
}

func TestModelUpdate_AgentsLoadedMsg(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	agents := []model.Agent{
		{ID: uuid.New(), Name: "agent-1", Status: model.AgentWorking},
		{ID: uuid.New(), Name: "agent-2", Status: model.AgentIdle},
	}

	result, cmd := m.Update(agentsLoadedMsg{agents: agents})
	got := result.(Model)

	if len(got.agents.agents) != 2 {
		t.Errorf("agents count = %d, want 2", len(got.agents.agents))
	}
	if cmd != nil {
		t.Error("agentsLoadedMsg should return nil cmd")
	}
}

func TestModelUpdate_LogCapturedMsg_StaleSelection(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskA := uuid.New()
	taskB := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskA, Title: "A", Status: model.StatusInProgress},
	}
	m.board.cursor = 0

	// Send log for a different task than what's selected
	result, cmd := m.Update(logCapturedMsg{forTaskID: taskB, text: "some log"})
	got := result.(Model)

	// Log should be discarded because selection moved
	if got.detail.logText != "" {
		t.Errorf("logText should be empty for stale log, got %q", got.detail.logText)
	}
	if cmd != nil {
		t.Error("stale log should return nil cmd")
	}
}

func TestModelUpdate_LogCapturedMsg_MatchesSelection(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "A", Status: model.StatusInProgress},
	}
	m.board.cursor = 0

	result, _ := m.Update(logCapturedMsg{forTaskID: taskID, text: "log output"})
	got := result.(Model)

	if got.detail.logText != "log output" {
		t.Errorf("logText = %q, want %q", got.detail.logText, "log output")
	}
}

func TestModelUpdate_LogCapturedMsg_Error(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "A", Status: model.StatusInProgress},
	}
	m.board.cursor = 0

	result, _ := m.Update(logCapturedMsg{
		forTaskID: taskID,
		err:       errForTest("log error"),
	})
	got := result.(Model)

	if got.detail.logText == "" {
		t.Error("logText should be set for error case")
	}
}

func TestModelUpdate_OrchLogCapturedMsg_StaleSelection(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskA := uuid.New()
	taskB := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskA, Title: "A", Status: model.StatusInProgress},
	}
	m.board.cursor = 0

	result, _ := m.Update(orchLogCapturedMsg{forTaskID: taskB, text: "orch log"})
	got := result.(Model)

	if got.detail.logText != "" {
		t.Errorf("logText should be empty for stale orch log, got %q", got.detail.logText)
	}
}

func TestModelUpdate_OrchLogCapturedMsg_MatchesSelection(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "A", Status: model.StatusInProgress},
	}
	m.board.cursor = 0

	result, _ := m.Update(orchLogCapturedMsg{forTaskID: taskID, text: "orch log output"})
	got := result.(Model)

	if got.detail.logText != "orch log output" {
		t.Errorf("logText = %q, want %q", got.detail.logText, "orch log output")
	}
}

func TestModelUpdate_OrchLogCapturedMsg_Error(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "A", Status: model.StatusInProgress},
	}
	m.board.cursor = 0

	result, _ := m.Update(orchLogCapturedMsg{
		forTaskID: taskID,
		err:       errForTest("orch log error"),
	})
	got := result.(Model)

	if got.detail.logText == "" {
		t.Error("logText should be set for error case")
	}
}

func TestModelUpdate_SupervisorSpawnedMsg_Error(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)

	result, _ := m.Update(supervisorSpawnedMsg{err: errForTest("spawn fail")})
	got := result.(Model)

	if got.err == nil {
		t.Error("expected error to be set on model")
	}
}

func TestModelUpdate_ReviewerSpawnedMsg_Error(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)

	result, cmd := m.Update(reviewerSpawnedMsg{err: errForTest("review fail")})
	got := result.(Model)

	if got.err == nil {
		t.Error("expected error to be set on model")
	}
	// reviewerSpawnedMsg always returns refreshData cmd
	if cmd == nil {
		t.Error("reviewerSpawnedMsg should return refresh cmd")
	}
}

func TestModelUpdate_FixerSpawnedMsg_Error(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)

	result, cmd := m.Update(fixerSpawnedMsg{err: errForTest("fixer fail")})
	got := result.(Model)

	if got.err == nil {
		t.Error("expected error to be set on model")
	}
	if cmd == nil {
		t.Error("fixerSpawnedMsg should return refresh cmd")
	}
}

func TestModelUpdate_ReapMsg_Error(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)

	result, cmd := m.Update(reapMsg{err: errForTest("reap fail")})
	got := result.(Model)

	if got.err == nil {
		t.Error("expected error to be set on model")
	}
	if cmd == nil {
		t.Error("reapMsg should return refresh cmd")
	}
}

func TestModelUpdate_ReapMsg_Success(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)

	result, cmd := m.Update(reapMsg{reaped: 3})
	got := result.(Model)

	if got.err == nil {
		t.Error("expected err to be set with reap count")
	}
	if cmd == nil {
		t.Error("reapMsg should return refresh cmd")
	}
}

func TestModelUpdate_DeleteResultMsg_Error(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)

	result, cmd := m.Update(deleteResultMsg{err: errForTest("delete fail")})
	got := result.(Model)

	if got.err == nil {
		t.Error("expected error to be set on model")
	}
	if cmd == nil {
		t.Error("deleteResultMsg should return refresh cmd")
	}
}

func TestModelUpdate_DeleteResultMsg_Success(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)

	result, cmd := m.Update(deleteResultMsg{err: nil})
	got := result.(Model)

	if got.err != nil {
		t.Error("err should be nil on success")
	}
	if cmd == nil {
		t.Error("deleteResultMsg should return refresh cmd")
	}
}

func TestModelUpdate_UnknownMsgPassthrough(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	type unknownMsg struct{}

	result, cmd := m.Update(unknownMsg{})
	got := result.(Model)

	if cmd != nil {
		t.Error("unknown msg should return nil cmd")
	}
	if got.focus != FocusBoard {
		t.Error("unknown msg should not change state")
	}
}

func TestModelUpdate_LogCapturedMsg_NoSelection(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	// No tasks on board

	result, cmd := m.Update(logCapturedMsg{forTaskID: uuid.New(), text: "log"})
	got := result.(Model)

	if got.detail.logText != "" {
		t.Error("logText should be empty when no task selected")
	}
	if cmd != nil {
		t.Error("should return nil cmd when no selection")
	}
}

// ---------------------------------------------------------------------------
// updateDetail tests
// ---------------------------------------------------------------------------

func TestUpdateDetail_ClearsOnSelectionChange(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskA := uuid.New()
	taskB := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskA, Title: "A", Status: model.StatusInProgress},
		{ID: taskB, Title: "B", Status: model.StatusBacklog},
	}
	m.board.cursor = 0

	// Set up some detail data
	m.detail.task = &model.Task{ID: uuid.New()} // different from selected
	m.detail.subtasks = []model.Task{{ID: uuid.New()}}
	m.detail.comments = []model.TaskComment{{ID: uuid.New()}}
	m.detail.scrollOffset = 5

	m.updateDetail()

	if m.detail.scrollOffset != 0 {
		t.Error("scrollOffset should be reset on selection change")
	}
	if m.detail.subtasks != nil {
		t.Error("subtasks should be cleared on selection change")
	}
	if m.detail.comments != nil {
		t.Error("comments should be cleared on selection change")
	}
}

func TestUpdateDetail_SameTask(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskID := uuid.New()
	task := model.Task{ID: taskID, Title: "A", Status: model.StatusInProgress}
	m.board.tasks = []model.Task{task}
	m.board.cursor = 0

	// Detail already points to same task
	m.detail.task = &task
	m.detail.scrollOffset = 3

	m.updateDetail()

	// Scroll offset should be preserved when same task
	if m.detail.scrollOffset != 3 {
		t.Errorf("scrollOffset = %d, want 3 (preserved for same task)", m.detail.scrollOffset)
	}
}

func TestUpdateDetail_NoTasks(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	m.updateDetail()

	if m.detail.task != nil {
		t.Error("detail.task should be nil when no tasks")
	}
}

func TestUpdateDetail_SetsAgentFilter(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskID := uuid.New()
	subtaskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "Parent", Status: model.StatusInProgress},
	}
	m.board.cursor = 0
	m.detail.subtasks = []model.Task{
		{ID: subtaskID, ParentTaskID: &taskID, Title: "Sub", Status: model.StatusBacklog},
	}
	m.detail.task = &m.board.tasks[0]

	m.updateDetail()

	if m.agents.filterTaskID == nil || *m.agents.filterTaskID != taskID {
		t.Error("agent filter task ID should be set to selected task")
	}
}

// ---------------------------------------------------------------------------
// toggleBoardCollapse tests
// ---------------------------------------------------------------------------

func TestToggleBoardCollapse_ParentNode(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	parentID := uuid.New()
	childID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: parentID, Title: "Parent", Status: model.StatusInProgress},
		{ID: childID, ParentTaskID: &parentID, Title: "Child", Status: model.StatusBacklog},
	}
	m.board.cursor = 0 // on the parent

	// First toggle expands
	m.toggleBoardCollapse()
	if !m.board.expanded[parentID] {
		t.Error("first toggle should expand parent")
	}

	// Second toggle collapses
	m.toggleBoardCollapse()
	if m.board.expanded[parentID] {
		t.Error("second toggle should collapse parent")
	}
}

func TestToggleBoardCollapse_ChildNode(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	parentID := uuid.New()
	childID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: parentID, Title: "Parent", Status: model.StatusInProgress},
		{ID: childID, ParentTaskID: &parentID, Title: "Child", Status: model.StatusBacklog},
	}
	m.board.expanded = map[uuid.UUID]bool{parentID: true}
	m.board.cursor = 1 // on the child

	// Toggle from child should collapse parent
	m.toggleBoardCollapse()
	if m.board.expanded[parentID] {
		t.Error("toggling from child should collapse parent")
	}
	// Cursor should move to parent
	if m.board.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (moved to parent after collapse)", m.board.cursor)
	}
}

func TestToggleBoardCollapse_OutOfBounds(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	m.board.cursor = 99 // out of bounds

	// Should not panic
	m.toggleBoardCollapse()
}

func TestToggleBoardCollapse_LeafNode(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	leafID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: leafID, Title: "Leaf", Status: model.StatusBacklog},
	}
	m.board.cursor = 0

	// Toggle on a leaf (no children, not a child) should be a no-op
	m.toggleBoardCollapse()
	if m.board.expanded != nil && m.board.expanded[leafID] {
		t.Error("toggling a leaf should not mark it as expanded")
	}
}

// ---------------------------------------------------------------------------
// updatePanelSizes tests
// ---------------------------------------------------------------------------

func TestUpdatePanelSizes_MinimumDimensions(t *testing.T) {
	m := Model{
		width:    20,
		height:   12,
		create:   NewCreateModel(),
		feedback: NewFeedbackModel(""),
	}
	m.updatePanelSizes()

	// Board width should be positive
	if m.board.width <= 0 {
		t.Errorf("board.width = %d, want > 0", m.board.width)
	}
	if m.board.height <= 0 {
		t.Errorf("board.height = %d, want > 0", m.board.height)
	}
	if m.detail.width <= 0 {
		t.Errorf("detail.width = %d, want > 0", m.detail.width)
	}
	if m.detail.height <= 0 {
		t.Errorf("detail.height = %d, want > 0", m.detail.height)
	}
}

func TestUpdatePanelSizes_SmallWindow(t *testing.T) {
	m := Model{
		width:    5,
		height:   5,
		create:   NewCreateModel(),
		feedback: NewFeedbackModel(""),
	}
	m.updatePanelSizes()

	// Should handle small windows without panicking
	if m.board.width < 0 {
		t.Errorf("board.width = %d, should not be negative", m.board.width)
	}
}

// ---------------------------------------------------------------------------
// DataRefreshedMsg tests
// ---------------------------------------------------------------------------

func TestModelUpdate_DataRefreshedMsg_UpdatesTasksAndAgents(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskID := uuid.New()
	task := model.Task{ID: taskID, Title: "Original", Status: model.StatusInProgress}
	m.board.tasks = []model.Task{task}
	m.board.cursor = 0
	m.board.selectedID = &taskID
	m.detail.task = &task // detail already points to same task

	subtask := model.Task{ID: uuid.New(), ParentTaskID: &taskID, Title: "Sub", Status: model.StatusBacklog}
	agent := model.Agent{ID: uuid.New(), Name: "agent-1", Status: model.AgentWorking}
	comment := model.TaskComment{ID: uuid.New(), Author: "user", Body: "Hello"}

	result, _ := m.Update(dataRefreshedMsg{
		tasks:     []model.Task{{ID: taskID, Title: "Updated", Status: model.StatusInProgress}},
		agents:    []model.Agent{agent},
		forTaskID: &taskID,
		subtasks:  []model.Task{subtask},
		agent:     &agent,
		comments:  []model.TaskComment{comment},
	})
	got := result.(Model)

	if len(got.board.tasks) != 1 || got.board.tasks[0].Title != "Updated" {
		t.Error("tasks should be updated from refresh")
	}
	if len(got.agents.agents) != 1 {
		t.Error("agents should be updated from refresh")
	}
	// Detail data is set when forTaskID matches the current selection
	// AND then updateDetail preserves it because detail.task.ID matches.
	if len(got.detail.subtasks) != 1 {
		t.Error("subtasks should be set from refresh when task matches")
	}
	if got.detail.agent == nil {
		t.Error("detail agent should be set from refresh when task matches")
	}
}

func TestModelUpdate_DataRefreshedMsg_StaleForTaskID(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskA := uuid.New()
	taskB := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskA, Title: "A", Status: model.StatusInProgress},
	}
	m.board.cursor = 0

	agent := model.Agent{ID: uuid.New(), Name: "agent-1", Status: model.AgentWorking}

	result, _ := m.Update(dataRefreshedMsg{
		tasks:     []model.Task{{ID: taskA, Title: "A", Status: model.StatusInProgress}},
		agents:    []model.Agent{agent},
		forTaskID: &taskB, // stale - doesn't match current selection
		subtasks:  []model.Task{{ID: uuid.New(), Title: "Sub"}},
		agent:     &agent,
	})
	got := result.(Model)

	// Detail data should NOT be updated because forTaskID doesn't match selection
	if got.detail.agent != nil {
		t.Error("detail agent should not be set when forTaskID doesn't match selection")
	}
}

// ---------------------------------------------------------------------------
// errForTest helper
// ---------------------------------------------------------------------------

type testError string

func (e testError) Error() string { return string(e) }

func errForTest(msg string) error { return testError(msg) }
