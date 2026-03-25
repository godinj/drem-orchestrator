package tui

import (
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ---------------------------------------------------------------------------
// Phase 5: Input handlers and navigation tests
// ---------------------------------------------------------------------------

// --- Enter key: drill into detail from board ---

func TestBoardEnter_DrillsIntoDetail(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "Task A", Status: model.StatusInProgress},
	}
	m.board.cursor = 0

	result, cmd := m.Update(keyMsg("enter"))
	got := result.(Model)

	if got.focus != FocusDetail {
		t.Errorf("focus = %d, want FocusDetail (%d)", got.focus, FocusDetail)
	}
	if cmd != nil {
		t.Error("enter on board should produce nil cmd")
	}
}

func TestBoardEnter_EmptyBoard(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	// No tasks

	result, cmd := m.Update(keyMsg("enter"))
	got := result.(Model)

	// Should still change focus even with empty board (detail shows "Select a task")
	if got.focus != FocusDetail {
		t.Errorf("focus = %d, want FocusDetail even with empty board", got.focus)
	}
	if cmd != nil {
		t.Error("enter on empty board should produce nil cmd")
	}
}

// --- Escape key: go back to overview from detail ---

func TestDetailEsc_BackToBoard(t *testing.T) {
	m := newKeyTestModel(t, FocusDetail)
	m.detail.scrollOffset = 5

	result, cmd := m.Update(keyMsg("esc"))
	got := result.(Model)

	if got.focus != FocusBoard {
		t.Errorf("focus = %d, want FocusBoard (%d)", got.focus, FocusBoard)
	}
	if got.detail.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0 (reset on esc)", got.detail.scrollOffset)
	}
	if cmd != nil {
		t.Error("esc from detail should produce nil cmd")
	}
}

func TestDetailEsc_DeleteModeBlocksEsc(t *testing.T) {
	m := newKeyTestModel(t, FocusDetail)
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

	// Delete mode should intercept esc, not change panel
	if got.detail.deleteMode {
		t.Error("esc should exit delete mode")
	}
	if got.focus != FocusDetail {
		t.Errorf("focus = %d, want FocusDetail (delete mode intercepts esc)", got.focus)
	}
}

// --- Escape key: go back to board from agents ---

func TestAgentEsc_BackToBoard(t *testing.T) {
	m := newKeyTestModel(t, FocusAgents)

	result, cmd := m.Update(keyMsg("esc"))
	got := result.(Model)

	if got.focus != FocusBoard {
		t.Errorf("focus = %d, want FocusBoard (%d)", got.focus, FocusBoard)
	}
	if cmd != nil {
		t.Error("esc from agents should produce nil cmd")
	}
}

// --- Escape key: board (home panel) clears error ---

func TestBoardEsc_ClearsError(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	m.err = errForTest("some error")

	result, cmd := m.Update(keyMsg("esc"))
	got := result.(Model)

	if got.err != nil {
		t.Error("esc on board should clear error")
	}
	if got.focus != FocusBoard {
		t.Error("esc on board should stay on board")
	}
	if cmd != nil {
		t.Error("esc on board should produce nil cmd")
	}
}

func TestBoardEsc_NoErrorIsNoop(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)

	result, cmd := m.Update(keyMsg("esc"))
	got := result.(Model)

	if got.focus != FocusBoard {
		t.Error("esc on board with no error should stay on board")
	}
	if cmd != nil {
		t.Error("esc on board should produce nil cmd")
	}
}

// --- Enter key: drill into agent ---

func TestAgentEnter_NoAgentsFallsToDetail(t *testing.T) {
	m := newKeyTestModel(t, FocusAgents)
	// No agents

	result, cmd := m.Update(keyMsg("enter"))
	got := result.(Model)

	if got.focus != FocusDetail {
		t.Errorf("focus = %d, want FocusDetail when no agents", got.focus)
	}
	if cmd != nil {
		t.Error("enter with no agents should produce nil cmd")
	}
}

func TestAgentEnter_NoTmuxSessionFallsToDetail(t *testing.T) {
	m := newKeyTestModel(t, FocusAgents)
	taskA := uuid.New()
	m.agents.agents = []model.Agent{
		{ID: uuid.New(), Name: "headless-agent", Status: model.AgentWorking, CurrentTaskID: &taskA},
	}
	m.agents.cursor = 0

	result, cmd := m.Update(keyMsg("enter"))
	got := result.(Model)

	if got.focus != FocusDetail {
		t.Errorf("focus = %d, want FocusDetail for headless agent", got.focus)
	}
	if cmd != nil {
		t.Error("enter on headless agent should produce nil cmd")
	}
}

// --- Full navigation cycle: board → detail → board ---

func TestNavigationCycle_BoardDetailBoard(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "Task", Status: model.StatusInProgress},
	}
	m.board.cursor = 0

	// Step 1: Enter from board to drill into detail
	result, _ := m.Update(keyMsg("enter"))
	got := result.(Model)
	if got.focus != FocusDetail {
		t.Fatal("step 1: should drill into detail")
	}

	// Step 2: Escape from detail back to board
	result, _ = got.Update(keyMsg("esc"))
	got = result.(Model)
	if got.focus != FocusBoard {
		t.Fatal("step 2: should return to board")
	}
}

// --- Full navigation cycle: board → agents → board ---

func TestNavigationCycle_BoardAgentsBoard(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)

	// Step 1: Tab to agents
	result, _ := m.Update(keyMsg("tab"))
	got := result.(Model)
	if got.focus != FocusAgents {
		t.Fatal("step 1: tab should go to agents")
	}

	// Step 2: Esc back to board
	result, _ = got.Update(keyMsg("esc"))
	got = result.(Model)
	if got.focus != FocusBoard {
		t.Fatal("step 2: esc should return to board")
	}
}

// --- Full navigation cycle: board → agents → detail → board ---

func TestNavigationCycle_BoardAgentsDetailBoard(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)

	// Step 1: Tab to agents
	result, _ := m.Update(keyMsg("tab"))
	got := result.(Model)
	if got.focus != FocusAgents {
		t.Fatal("step 1: should go to agents")
	}

	// Step 2: Enter to drill into detail (no agents, falls through)
	result, _ = got.Update(keyMsg("enter"))
	got = result.(Model)
	if got.focus != FocusDetail {
		t.Fatal("step 2: should drill into detail")
	}

	// Step 3: Esc back to board
	result, _ = got.Update(keyMsg("esc"))
	got = result.(Model)
	if got.focus != FocusBoard {
		t.Fatal("step 3: should return to board")
	}
}

// --- Navigation state: verify section selection updates ---

func TestBoardCursorMovement_UpdatesDetail(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskA := uuid.New()
	taskB := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskA, Title: "Task A", Status: model.StatusInProgress},
		{ID: taskB, Title: "Task B", Status: model.StatusBacklog},
	}
	m.board.cursor = 0

	// Move down to task B
	result, _ := m.Update(keyMsg("j"))
	got := result.(Model)

	if got.board.cursor != 1 {
		t.Errorf("cursor = %d, want 1", got.board.cursor)
	}
	// Detail should now point to task B
	if got.detail.task == nil {
		t.Fatal("detail.task should not be nil after cursor movement")
	}
	if got.detail.task.ID != taskB {
		t.Errorf("detail.task.ID = %v, want %v", got.detail.task.ID, taskB)
	}
}

func TestBoardCursorAtBoundary_NoStateChange(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	taskID := uuid.New()
	m.board.tasks = []model.Task{
		{ID: taskID, Title: "Only Task", Status: model.StatusInProgress},
	}
	m.board.cursor = 0
	m.detail.task = &m.board.tasks[0]

	// Try to move up at top - cursor should stay at 0
	result, cmd := m.Update(keyMsg("k"))
	got := result.(Model)

	if got.board.cursor != 0 {
		t.Errorf("cursor = %d, want 0 at top boundary", got.board.cursor)
	}
	// No refresh should be triggered when cursor doesn't move
	if cmd != nil {
		t.Error("boundary k should not produce a cmd")
	}
}

// --- Scroll behavior: verify scroll position updates ---

func TestBoardScroll_LargeList(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	tasks := make([]model.Task, 50)
	for i := range tasks {
		tasks[i] = model.Task{
			ID:     uuid.New(),
			Title:  "Task",
			Status: model.StatusBacklog,
		}
	}
	m.board.tasks = tasks
	m.board.cursor = 0
	m.board.height = 5

	// Move cursor down many times
	var result interface{} = m
	for i := 0; i < 10; i++ {
		r, _ := result.(Model).Update(keyMsg("j"))
		result = r
	}
	got := result.(Model)

	if got.board.cursor != 10 {
		t.Errorf("cursor = %d, want 10 after 10 j presses", got.board.cursor)
	}
	// Cursor should be within the visible scroll window
	if got.board.cursor < got.board.scrollOffset {
		t.Errorf("cursor %d is above scroll window starting at %d",
			got.board.cursor, got.board.scrollOffset)
	}
	if got.board.cursor >= got.board.scrollOffset+got.board.height {
		t.Errorf("cursor %d is below scroll window ending at %d",
			got.board.cursor, got.board.scrollOffset+got.board.height)
	}
}

func TestDetailScroll_Increments(t *testing.T) {
	m := newKeyTestModel(t, FocusDetail)
	m.detail.scrollOffset = 0

	// Scroll down 5 times
	var result interface{} = m
	for i := 0; i < 5; i++ {
		r, _ := result.(Model).Update(keyMsg("j"))
		result = r
	}
	got := result.(Model)

	if got.detail.scrollOffset != 5 {
		t.Errorf("scrollOffset = %d, want 5 after 5 j presses", got.detail.scrollOffset)
	}

	// Scroll back up 3 times
	for i := 0; i < 3; i++ {
		r, _ := result.(Model).Update(keyMsg("k"))
		result = r
	}
	got = result.(Model)

	if got.detail.scrollOffset != 2 {
		t.Errorf("scrollOffset = %d, want 2 after scrolling back 3", got.detail.scrollOffset)
	}
}

func TestDetailScroll_FloorAtZero(t *testing.T) {
	m := newKeyTestModel(t, FocusDetail)
	m.detail.scrollOffset = 0

	// Try to scroll up past 0
	result, _ := m.Update(keyMsg("k"))
	got := result.(Model)

	if got.detail.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0 (can't go below zero)", got.detail.scrollOffset)
	}
}

func TestDetailScroll_ResetOnEsc(t *testing.T) {
	m := newKeyTestModel(t, FocusDetail)
	m.detail.scrollOffset = 10

	result, _ := m.Update(keyMsg("esc"))
	got := result.(Model)

	if got.detail.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0 after esc", got.detail.scrollOffset)
	}
	if got.focus != FocusBoard {
		t.Errorf("focus = %d, want FocusBoard", got.focus)
	}
}

// --- Tab cycling: complete panel cycle ---

func TestTabCycling_FullCycle(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)

	// Board → tab → Agents
	result, _ := m.Update(keyMsg("tab"))
	got := result.(Model)
	if got.focus != FocusAgents {
		t.Fatalf("board tab: focus = %d, want FocusAgents", got.focus)
	}

	// Agents → tab → Detail
	result, _ = got.Update(keyMsg("tab"))
	got = result.(Model)
	if got.focus != FocusDetail {
		t.Fatalf("agents tab: focus = %d, want FocusDetail", got.focus)
	}

	// Detail → tab → Board
	result, _ = got.Update(keyMsg("tab"))
	got = result.(Model)
	if got.focus != FocusBoard {
		t.Fatalf("detail tab: focus = %d, want FocusBoard", got.focus)
	}
}

func TestShiftTabCycling_ReverseCycle(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)

	// Board → shift+tab → Detail
	result, _ := m.Update(keyMsg("shift+tab"))
	got := result.(Model)
	if got.focus != FocusDetail {
		t.Fatalf("board shift+tab: focus = %d, want FocusDetail", got.focus)
	}

	// Detail → shift+tab → Agents
	result, _ = got.Update(keyMsg("shift+tab"))
	got = result.(Model)
	if got.focus != FocusAgents {
		t.Fatalf("detail shift+tab: focus = %d, want FocusAgents", got.focus)
	}

	// Agents → shift+tab → Board
	result, _ = got.Update(keyMsg("shift+tab"))
	got = result.(Model)
	if got.focus != FocusBoard {
		t.Fatalf("agents shift+tab: focus = %d, want FocusBoard", got.focus)
	}
}

// --- Agent cursor navigation persists across panel focus changes ---

func TestAgentCursor_PersistsAcrossFocusChange(t *testing.T) {
	m := newKeyTestModel(t, FocusAgents)
	taskA := uuid.New()
	m.agents.agents = []model.Agent{
		{ID: uuid.New(), Name: "agent-1", Status: model.AgentWorking, CurrentTaskID: &taskA},
		{ID: uuid.New(), Name: "agent-2", Status: model.AgentWorking, CurrentTaskID: &taskA},
		{ID: uuid.New(), Name: "agent-3", Status: model.AgentWorking, CurrentTaskID: &taskA},
	}
	m.agents.cursor = 0

	// Move cursor down to agent-2
	result, _ := m.Update(keyMsg("j"))
	got := result.(Model)
	if got.agents.cursor != 1 {
		t.Fatal("agent cursor should be 1 after j")
	}

	// Switch to board
	result, _ = got.Update(keyMsg("esc"))
	got = result.(Model)
	if got.focus != FocusBoard {
		t.Fatal("should be on board after esc")
	}

	// Agent cursor should be preserved
	if got.agents.cursor != 1 {
		t.Errorf("agent cursor = %d, want 1 (preserved across focus change)", got.agents.cursor)
	}
}

// --- Dispatch by focus: verify enter/esc handled per panel ---

func TestHandleKey_EnterEscDispatch(t *testing.T) {
	tests := []struct {
		name      string
		focus     Focus
		key       string
		wantFocus Focus
	}{
		{
			name:      "board enter to detail",
			focus:     FocusBoard,
			key:       "enter",
			wantFocus: FocusDetail,
		},
		{
			name:      "detail esc to board",
			focus:     FocusDetail,
			key:       "esc",
			wantFocus: FocusBoard,
		},
		{
			name:      "agents esc to board",
			focus:     FocusAgents,
			key:       "esc",
			wantFocus: FocusBoard,
		},
		{
			name:      "agents enter to detail (no agents)",
			focus:     FocusAgents,
			key:       "enter",
			wantFocus: FocusDetail,
		},
		{
			name:      "create esc to board",
			focus:     FocusCreate,
			key:       "esc",
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

// --- Down arrow keys work identically to j ---

func TestBoardDownArrow_EquivalentToJ(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	m.board.tasks = []model.Task{
		{ID: uuid.New(), Title: "A", Status: model.StatusInProgress},
		{ID: uuid.New(), Title: "B", Status: model.StatusBacklog},
	}
	m.board.cursor = 0

	result, _ := m.Update(keyMsg("down"))
	got := result.(Model)
	if got.board.cursor != 1 {
		t.Errorf("cursor = %d, want 1 after down arrow", got.board.cursor)
	}
}

func TestDetailDownArrow_EquivalentToJ(t *testing.T) {
	m := newKeyTestModel(t, FocusDetail)
	m.detail.scrollOffset = 0

	result, _ := m.Update(keyMsg("down"))
	got := result.(Model)
	if got.detail.scrollOffset != 1 {
		t.Errorf("scrollOffset = %d, want 1 after down arrow", got.detail.scrollOffset)
	}
}

func TestAgentDownArrow_EquivalentToJ(t *testing.T) {
	m := newKeyTestModel(t, FocusAgents)
	taskA := uuid.New()
	m.agents.agents = []model.Agent{
		{ID: uuid.New(), Name: "a1", Status: model.AgentWorking, CurrentTaskID: &taskA},
		{ID: uuid.New(), Name: "a2", Status: model.AgentWorking, CurrentTaskID: &taskA},
	}
	m.agents.cursor = 0

	result, _ := m.Update(keyMsg("down"))
	got := result.(Model)
	if got.agents.cursor != 1 {
		t.Errorf("cursor = %d, want 1 after down arrow", got.agents.cursor)
	}
}
