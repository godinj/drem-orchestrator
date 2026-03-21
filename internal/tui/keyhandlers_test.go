package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// newKeyTestModel builds a minimal Model for key handler testing.
func newKeyTestModel(t *testing.T, focus Focus) Model {
	t.Helper()
	m := Model{
		keys:     defaultKeyMap(),
		focus:    focus,
		create:   NewCreateModel(),
		feedback: NewFeedbackModel(""),
		board:    NewBoardModel(),
		agents:   NewAgentsModel(),
		detail:   NewDetailModel(),
		width:    120,
		height:   40,
	}
	m.updatePanelSizes()
	return m
}

// keyMsg creates a tea.KeyMsg from a string representation.
func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab", "btab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "ctrl+j":
		return tea.KeyMsg{Type: tea.KeyCtrlJ}
	case "ctrl+k":
		return tea.KeyMsg{Type: tea.KeyCtrlK}
	case "ctrl+l":
		return tea.KeyMsg{Type: tea.KeyCtrlL}
	case "ctrl+h":
		return tea.KeyMsg{Type: tea.KeyCtrlH}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// ---------------------------------------------------------------------------
// handleKey dispatch tests
// ---------------------------------------------------------------------------

func TestHandleKey_CtrlCIgnored(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	result, cmd := m.Update(keyMsg("ctrl+c"))
	got := result.(Model)
	if cmd != nil {
		t.Error("ctrl+c should produce nil cmd")
	}
	if got.focus != FocusBoard {
		t.Error("ctrl+c should not change focus")
	}
}

func TestHandleKey_HelpToggle(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	m.showHelp = false

	result, _ := m.Update(keyMsg("?"))
	got := result.(Model)
	if !got.showHelp {
		t.Error("? should toggle showHelp on")
	}

	// Toggle off
	result, _ = got.Update(keyMsg("?"))
	got = result.(Model)
	if got.showHelp {
		t.Error("? should toggle showHelp off")
	}
}

func TestHandleKey_DismissHelpWithAnyKey(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	m.showHelp = true

	result, cmd := m.Update(keyMsg("j"))
	got := result.(Model)
	if got.showHelp {
		t.Error("any key should dismiss help")
	}
	if cmd != nil {
		t.Error("dismissing help should not produce a cmd")
	}
}

func TestHandleKey_DispatchByFocus(t *testing.T) {
	tests := []struct {
		name      string
		focus     Focus
		key       string
		wantFocus Focus
	}{
		{
			name:      "board tab to agents",
			focus:     FocusBoard,
			key:       "tab",
			wantFocus: FocusAgents,
		},
		{
			name:      "board shift+tab to detail",
			focus:     FocusBoard,
			key:       "shift+tab",
			wantFocus: FocusDetail,
		},
		{
			name:      "board ctrl+j to detail",
			focus:     FocusBoard,
			key:       "ctrl+j",
			wantFocus: FocusDetail,
		},
		{
			name:      "agents tab to detail",
			focus:     FocusAgents,
			key:       "tab",
			wantFocus: FocusDetail,
		},
		{
			name:      "agents shift+tab to board",
			focus:     FocusAgents,
			key:       "shift+tab",
			wantFocus: FocusBoard,
		},
		{
			name:      "agents ctrl+j to detail",
			focus:     FocusAgents,
			key:       "ctrl+j",
			wantFocus: FocusDetail,
		},
		{
			name:      "detail tab to board",
			focus:     FocusDetail,
			key:       "tab",
			wantFocus: FocusBoard,
		},
		{
			name:      "detail shift+tab to agents",
			focus:     FocusDetail,
			key:       "shift+tab",
			wantFocus: FocusAgents,
		},
		{
			name:      "detail ctrl+k to board",
			focus:     FocusDetail,
			key:       "ctrl+k",
			wantFocus: FocusBoard,
		},
		{
			name:      "detail ctrl+h to board",
			focus:     FocusDetail,
			key:       "ctrl+h",
			wantFocus: FocusBoard,
		},
		{
			name:      "detail ctrl+l to agents",
			focus:     FocusDetail,
			key:       "ctrl+l",
			wantFocus: FocusAgents,
		},
		{
			name:      "board ctrl+l to agents",
			focus:     FocusBoard,
			key:       "ctrl+l",
			wantFocus: FocusAgents,
		},
		{
			name:      "agents ctrl+h to board",
			focus:     FocusAgents,
			key:       "ctrl+h",
			wantFocus: FocusBoard,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newKeyTestModel(t, tt.focus)
			result, _ := m.Update(keyMsg(tt.key))
			got := result.(Model)
			if got.focus != tt.wantFocus {
				t.Errorf("focus = %d, want %d", got.focus, tt.wantFocus)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Board key handler tests
// ---------------------------------------------------------------------------

func TestBoardKeys_CursorMovement(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	parentA := uuid.New()
	parentB := uuid.New()
	m.board.tasks = []model.Task{
		{ID: parentA, Title: "Task A", Status: model.StatusInProgress},
		{ID: parentB, Title: "Task B", Status: model.StatusBacklog},
	}
	m.board.cursor = 0

	// Move down
	result, _ := m.Update(keyMsg("j"))
	got := result.(Model)
	if got.board.cursor != 1 {
		t.Errorf("cursor = %d, want 1 after j", got.board.cursor)
	}

	// Move up
	result, _ = got.Update(keyMsg("k"))
	got = result.(Model)
	if got.board.cursor != 0 {
		t.Errorf("cursor = %d, want 0 after k", got.board.cursor)
	}

	// Don't go below 0
	result, _ = got.Update(keyMsg("k"))
	got = result.(Model)
	if got.board.cursor != 0 {
		t.Errorf("cursor = %d, want 0 at boundary", got.board.cursor)
	}
}

func TestBoardKeys_NewTask(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	result, _ := m.Update(keyMsg("n"))
	got := result.(Model)
	if got.focus != FocusCreate {
		t.Errorf("focus = %d, want FocusCreate (%d)", got.focus, FocusCreate)
	}
}

func TestBoardKeys_SpaceToggle(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	parentID := uuid.New()
	childID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: parentID, Title: "Parent", Status: model.StatusInProgress},
		{ID: childID, ParentTaskID: &parentID, Title: "Child", Status: model.StatusBacklog},
	}
	m.board.cursor = 0

	// Initially children are collapsed by default. Pressing space should expand.
	result, _ := m.Update(keyMsg(" "))
	got := result.(Model)
	if !got.board.expanded[parentID] {
		t.Error("space should expand the parent node")
	}

	// Press space again to collapse
	result, _ = got.Update(keyMsg(" "))
	got = result.(Model)
	if got.board.expanded[parentID] {
		t.Error("space should collapse the parent node")
	}
}

func TestBoardKeys_ArchiveToggle(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	if m.agents.showArchived {
		t.Fatal("showArchived should start false")
	}

	result, _ := m.Update(keyMsg("A"))
	got := result.(Model)
	if !got.agents.showArchived {
		t.Error("A should toggle showArchived on")
	}

	result, _ = got.Update(keyMsg("A"))
	got = result.(Model)
	if got.agents.showArchived {
		t.Error("A should toggle showArchived off")
	}
}

func TestBoardKeys_FilterToggle(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	if !m.agents.autoFilter {
		t.Fatal("autoFilter should start true")
	}

	result, _ := m.Update(keyMsg("F"))
	got := result.(Model)
	if got.agents.autoFilter {
		t.Error("F should toggle autoFilter off")
	}

	result, _ = got.Update(keyMsg("F"))
	got = result.(Model)
	if !got.agents.autoFilter {
		t.Error("F should toggle autoFilter on")
	}
}

func TestBoardKeys_NoSelectedTaskActionsAreNoop(t *testing.T) {
	// With no tasks, action keys should not crash and return nil cmd.
	m := newKeyTestModel(t, FocusBoard)
	keys := []string{"a", "r", "t", "f", "p", "R", "l", "c"}

	for _, k := range keys {
		t.Run("key_"+k, func(t *testing.T) {
			_, cmd := m.Update(keyMsg(k))
			if cmd != nil {
				t.Errorf("key %q with no selected task should produce nil cmd", k)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Agent key handler tests
// ---------------------------------------------------------------------------

func TestAgentKeys_CursorMovement(t *testing.T) {
	m := newKeyTestModel(t, FocusAgents)
	taskA := uuid.New()
	m.agents.agents = []model.Agent{
		{ID: uuid.New(), Name: "agent-1", Status: model.AgentWorking, CurrentTaskID: &taskA},
		{ID: uuid.New(), Name: "agent-2", Status: model.AgentWorking, CurrentTaskID: &taskA},
		{ID: uuid.New(), Name: "agent-3", Status: model.AgentWorking, CurrentTaskID: &taskA},
	}
	m.agents.cursor = 0

	result, _ := m.Update(keyMsg("j"))
	got := result.(Model)
	if got.agents.cursor != 1 {
		t.Errorf("cursor = %d, want 1", got.agents.cursor)
	}

	result, _ = got.Update(keyMsg("k"))
	got = result.(Model)
	if got.agents.cursor != 0 {
		t.Errorf("cursor = %d, want 0", got.agents.cursor)
	}
}

func TestAgentKeys_ArchiveToggle(t *testing.T) {
	m := newKeyTestModel(t, FocusAgents)

	result, _ := m.Update(keyMsg("A"))
	got := result.(Model)
	if !got.agents.showArchived {
		t.Error("A should toggle showArchived on from agent panel")
	}
}

func TestAgentKeys_FilterToggle(t *testing.T) {
	m := newKeyTestModel(t, FocusAgents)

	result, _ := m.Update(keyMsg("F"))
	got := result.(Model)
	if got.agents.autoFilter {
		t.Error("F should toggle autoFilter off from agent panel")
	}
}

// ---------------------------------------------------------------------------
// Detail key handler tests
// ---------------------------------------------------------------------------

func TestDetailKeys_Scroll(t *testing.T) {
	m := newKeyTestModel(t, FocusDetail)
	m.detail.scrollOffset = 0

	result, _ := m.Update(keyMsg("j"))
	got := result.(Model)
	if got.detail.scrollOffset != 1 {
		t.Errorf("scrollOffset = %d, want 1 after j", got.detail.scrollOffset)
	}

	result, _ = got.Update(keyMsg("k"))
	got = result.(Model)
	if got.detail.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0 after k", got.detail.scrollOffset)
	}

	// Don't go below 0
	result, _ = got.Update(keyMsg("k"))
	got = result.(Model)
	if got.detail.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0 at boundary", got.detail.scrollOffset)
	}
}

func TestDetailKeys_PanelNavigation(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		wantFocus Focus
	}{
		{"tab to board", "tab", FocusBoard},
		{"shift+tab to agents", "shift+tab", FocusAgents},
		{"ctrl+k to board", "ctrl+k", FocusBoard},
		{"ctrl+h to board", "ctrl+h", FocusBoard},
		{"ctrl+l to agents", "ctrl+l", FocusAgents},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newKeyTestModel(t, FocusDetail)
			result, _ := m.Update(keyMsg(tt.key))
			got := result.(Model)
			if got.focus != tt.wantFocus {
				t.Errorf("focus = %d, want %d", got.focus, tt.wantFocus)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Create key handler tests
// ---------------------------------------------------------------------------

func TestCreateKeys_EscReturnToBoard(t *testing.T) {
	m := newKeyTestModel(t, FocusCreate)
	result, _ := m.Update(keyMsg("esc"))
	got := result.(Model)
	if got.focus != FocusBoard {
		t.Errorf("focus = %d, want FocusBoard (%d)", got.focus, FocusBoard)
	}
}

func TestCreateKeys_EnterEmptyTitleShowsError(t *testing.T) {
	m := newKeyTestModel(t, FocusCreate)
	// Title is empty by default
	result, _ := m.Update(keyMsg("enter"))
	got := result.(Model)
	if got.create.err == nil {
		t.Error("enter with empty title should set error")
	}
	// Should stay on create focus
	if got.focus != FocusCreate {
		t.Errorf("focus = %d, want FocusCreate", got.focus)
	}
}

// ---------------------------------------------------------------------------
// Feedback key handler tests
// ---------------------------------------------------------------------------

func TestFeedbackKeys_EscReturnToBoard(t *testing.T) {
	m := newKeyTestModel(t, FocusFeedback)
	m.feedbackAction = feedbackAddComment
	m.feedback.Show()

	result, _ := m.Update(keyMsg("esc"))
	got := result.(Model)
	if got.focus != FocusBoard {
		t.Errorf("focus = %d, want FocusBoard", got.focus)
	}
	if got.feedbackAction != feedbackNone {
		t.Errorf("feedbackAction = %d, want feedbackNone", got.feedbackAction)
	}
	if got.feedback.Visible() {
		t.Error("feedback should be hidden after esc")
	}
}

func TestFeedbackKeys_EnterEmptyDismisses(t *testing.T) {
	m := newKeyTestModel(t, FocusFeedback)
	m.feedbackAction = feedbackAddComment
	m.feedback.Show()
	// No selected task, empty feedback
	result, _ := m.Update(keyMsg("enter"))
	got := result.(Model)
	if got.focus != FocusBoard {
		t.Errorf("focus = %d, want FocusBoard", got.focus)
	}
	if got.feedbackAction != feedbackNone {
		t.Errorf("feedbackAction = %d, want feedbackNone", got.feedbackAction)
	}
}

// ---------------------------------------------------------------------------
// Delete mode key handler tests
// ---------------------------------------------------------------------------

func TestDeleteModeKeys_EscCancels(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "Test", Status: model.StatusBacklog},
	}
	m.board.cursor = 0
	m.detail.task = &m.board.tasks[0]
	m.detail.deleteMode = true
	m.detail.deleteCursor = 0

	result, _ := m.Update(keyMsg("esc"))
	got := result.(Model)
	if got.detail.deleteMode {
		t.Error("esc should exit delete mode")
	}
}

func TestDeleteModeKeys_Navigation(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{
			ID:     taskID,
			Title:  "Test",
			Status: model.StatusPlanReview,
			Plan: model.JSONField{
				"subtasks": []any{
					map[string]any{"title": "Step 1"},
					map[string]any{"title": "Step 2"},
				},
			},
		},
	}
	m.board.cursor = 0
	m.detail.task = &m.board.tasks[0]
	m.detail.deleteMode = true
	m.detail.deleteCursor = 0
	m.detail.comments = []model.TaskComment{
		{ID: uuid.New(), Author: "user", Body: "Comment 1"},
	}

	// Move down
	result, _ := m.Update(keyMsg("j"))
	got := result.(Model)
	if got.detail.deleteCursor != 1 {
		t.Errorf("deleteCursor = %d, want 1", got.detail.deleteCursor)
	}

	// Move up
	result, _ = got.Update(keyMsg("k"))
	got = result.(Model)
	if got.detail.deleteCursor != 0 {
		t.Errorf("deleteCursor = %d, want 0", got.detail.deleteCursor)
	}

	// Don't go below 0
	result, _ = got.Update(keyMsg("k"))
	got = result.(Model)
	if got.detail.deleteCursor != 0 {
		t.Errorf("deleteCursor = %d, want 0 at boundary", got.detail.deleteCursor)
	}
}

func TestDeleteModeKeys_InterceptsFromDetail(t *testing.T) {
	m := newKeyTestModel(t, FocusDetail)
	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "Test", Status: model.StatusBacklog},
	}
	m.board.cursor = 0
	m.detail.task = &m.board.tasks[0]
	m.detail.deleteMode = true
	m.detail.deleteCursor = 0

	// Esc should cancel delete mode, not change panel
	result, _ := m.Update(keyMsg("esc"))
	got := result.(Model)
	if got.detail.deleteMode {
		t.Error("esc should exit delete mode from detail")
	}
	// Focus should not change
	if got.focus != FocusDetail {
		t.Errorf("focus = %d, want FocusDetail during delete mode cancel", got.focus)
	}
}
