package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ---------------------------------------------------------------------------
// BoardModel.Update tests
// ---------------------------------------------------------------------------

func TestBoardUpdate_CursorDown(t *testing.T) {
	b := BoardModel{
		tasks: []model.Task{
			{ID: uuid.New(), Title: "A", Status: model.StatusBacklog},
			{ID: uuid.New(), Title: "B", Status: model.StatusBacklog},
			{ID: uuid.New(), Title: "C", Status: model.StatusBacklog},
		},
		cursor: 0,
		height: 10,
	}

	got, _ := b.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if got.cursor != 1 {
		t.Errorf("cursor = %d, want 1 after j", got.cursor)
	}
}

func TestBoardUpdate_CursorUp(t *testing.T) {
	b := BoardModel{
		tasks: []model.Task{
			{ID: uuid.New(), Title: "A", Status: model.StatusBacklog},
			{ID: uuid.New(), Title: "B", Status: model.StatusBacklog},
		},
		cursor: 1,
		height: 10,
	}

	got, _ := b.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if got.cursor != 0 {
		t.Errorf("cursor = %d, want 0 after k", got.cursor)
	}
}

func TestBoardUpdate_CursorClampAtEnd(t *testing.T) {
	b := BoardModel{
		tasks: []model.Task{
			{ID: uuid.New(), Title: "A", Status: model.StatusBacklog},
			{ID: uuid.New(), Title: "B", Status: model.StatusBacklog},
		},
		cursor: 1,
		height: 10,
	}

	got, _ := b.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if got.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (clamped at end)", got.cursor)
	}
}

func TestBoardUpdate_CursorClampAtStart(t *testing.T) {
	b := BoardModel{
		tasks: []model.Task{
			{ID: uuid.New(), Title: "A", Status: model.StatusBacklog},
		},
		cursor: 0,
		height: 10,
	}

	got, _ := b.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if got.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (clamped at start)", got.cursor)
	}
}

func TestBoardUpdate_TracksSelected(t *testing.T) {
	idA := uuid.New()
	idB := uuid.New()
	b := BoardModel{
		tasks: []model.Task{
			{ID: idA, Title: "A", Status: model.StatusBacklog},
			{ID: idB, Title: "B", Status: model.StatusBacklog},
		},
		cursor: 0,
		height: 10,
	}

	got, _ := b.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if got.selectedID == nil {
		t.Fatal("selectedID should be set after movement")
	}
	if *got.selectedID != idB {
		t.Errorf("selectedID = %v, want %v", *got.selectedID, idB)
	}
}

func TestBoardUpdate_DownArrow(t *testing.T) {
	b := BoardModel{
		tasks: []model.Task{
			{ID: uuid.New(), Title: "A", Status: model.StatusBacklog},
			{ID: uuid.New(), Title: "B", Status: model.StatusBacklog},
		},
		cursor: 0,
		height: 10,
	}

	got, _ := b.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got.cursor != 1 {
		t.Errorf("cursor = %d, want 1 after down arrow", got.cursor)
	}
}

func TestBoardUpdate_UpArrow(t *testing.T) {
	b := BoardModel{
		tasks: []model.Task{
			{ID: uuid.New(), Title: "A", Status: model.StatusBacklog},
			{ID: uuid.New(), Title: "B", Status: model.StatusBacklog},
		},
		cursor: 1,
		height: 10,
	}

	got, _ := b.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got.cursor != 0 {
		t.Errorf("cursor = %d, want 0 after up arrow", got.cursor)
	}
}

// ---------------------------------------------------------------------------
// RelocateCursor tests
// ---------------------------------------------------------------------------

func TestRelocateCursor_EmptyTasks(t *testing.T) {
	b := BoardModel{cursor: 5}
	b.relocateCursor()
	if b.cursor != 0 {
		t.Errorf("cursor = %d, want 0 for empty tasks", b.cursor)
	}
	if b.selectedID != nil {
		t.Error("selectedID should be nil for empty tasks")
	}
}

func TestRelocateCursor_TracksByID(t *testing.T) {
	idA := uuid.New()
	idB := uuid.New()
	b := BoardModel{
		tasks: []model.Task{
			{ID: idA, Title: "A", Status: model.StatusBacklog},
			{ID: idB, Title: "B", Status: model.StatusBacklog},
		},
		cursor:     0,
		selectedID: &idB,
	}
	b.relocateCursor()
	if b.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (tracked by ID)", b.cursor)
	}
}

func TestRelocateCursor_ClampsWhenIDGone(t *testing.T) {
	idGone := uuid.New()
	idA := uuid.New()
	b := BoardModel{
		tasks: []model.Task{
			{ID: idA, Title: "A", Status: model.StatusBacklog},
		},
		cursor:     5, // Out of bounds
		selectedID: &idGone,
	}
	b.relocateCursor()
	if b.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (clamped)", b.cursor)
	}
	if b.selectedID == nil || *b.selectedID != idA {
		t.Errorf("selectedID should be updated to %v", idA)
	}
}

// ---------------------------------------------------------------------------
// TrackSelected tests
// ---------------------------------------------------------------------------

func TestTrackSelected(t *testing.T) {
	idA := uuid.New()
	idB := uuid.New()
	b := BoardModel{
		tasks: []model.Task{
			{ID: idA, Title: "A", Status: model.StatusBacklog},
			{ID: idB, Title: "B", Status: model.StatusBacklog},
		},
		cursor: 1,
	}
	b.trackSelected()
	if b.selectedID == nil || *b.selectedID != idB {
		t.Errorf("selectedID = %v, want %v", b.selectedID, idB)
	}
}

func TestTrackSelected_EmptyTasks(t *testing.T) {
	b := BoardModel{cursor: 0}
	b.trackSelected()
	// No crash; selectedID stays nil
	if b.selectedID != nil {
		t.Error("selectedID should stay nil for empty tasks")
	}
}

// ---------------------------------------------------------------------------
// AdjustScroll tests
// ---------------------------------------------------------------------------

func TestAdjustScroll_FitsInView(t *testing.T) {
	b := BoardModel{
		tasks: []model.Task{
			{ID: uuid.New(), Title: "A", Status: model.StatusBacklog},
			{ID: uuid.New(), Title: "B", Status: model.StatusBacklog},
		},
		cursor:       0,
		scrollOffset: 5,
		height:       10,
	}
	b.adjustScroll()
	if b.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0 when all tasks fit", b.scrollOffset)
	}
}

func TestAdjustScroll_CursorAboveView(t *testing.T) {
	tasks := make([]model.Task, 20)
	for i := range tasks {
		tasks[i] = model.Task{ID: uuid.New(), Title: "Task", Status: model.StatusBacklog}
	}
	b := BoardModel{
		tasks:        tasks,
		cursor:       2,
		scrollOffset: 5,
		height:       5,
	}
	b.adjustScroll()
	if b.scrollOffset > b.cursor {
		t.Errorf("scrollOffset = %d should be <= cursor %d", b.scrollOffset, b.cursor)
	}
}

func TestAdjustScroll_CursorBelowView(t *testing.T) {
	tasks := make([]model.Task, 20)
	for i := range tasks {
		tasks[i] = model.Task{ID: uuid.New(), Title: "Task", Status: model.StatusBacklog}
	}
	b := BoardModel{
		tasks:        tasks,
		cursor:       15,
		scrollOffset: 0,
		height:       5,
	}
	b.adjustScroll()
	if b.cursor >= b.scrollOffset+b.height {
		t.Errorf("cursor %d should be within scroll window [%d, %d)",
			b.cursor, b.scrollOffset, b.scrollOffset+b.height)
	}
}

func TestAdjustScroll_ZeroHeight(t *testing.T) {
	b := BoardModel{
		tasks:  []model.Task{{ID: uuid.New(), Title: "A", Status: model.StatusBacklog}},
		cursor: 0,
		height: 0,
	}
	b.adjustScroll()
	if b.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0 for zero height", b.scrollOffset)
	}
}

// ---------------------------------------------------------------------------
// NewBoardModel tests
// ---------------------------------------------------------------------------

func TestNewBoardModel(t *testing.T) {
	b := NewBoardModel()
	if b.cursor != 0 {
		t.Errorf("cursor = %d, want 0", b.cursor)
	}
	if len(b.tasks) != 0 {
		t.Errorf("tasks length = %d, want 0", len(b.tasks))
	}
}

// ---------------------------------------------------------------------------
// Collapsed/expanded state in buildDisplayList
// ---------------------------------------------------------------------------

func TestBuildDisplayList_CollapsedByDefault(t *testing.T) {
	parentID := uuid.New()
	childID := uuid.New()
	b := BoardModel{
		tasks: []model.Task{
			{ID: parentID, Title: "Parent", Status: model.StatusBacklog},
			{ID: childID, ParentTaskID: &parentID, Title: "Child", Status: model.StatusBacklog},
		},
		// expanded is nil = all collapsed
	}
	entries := b.buildDisplayList()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (collapsed parent), got %d", len(entries))
	}
	if !entries[0].collapsed {
		t.Error("parent should be collapsed by default")
	}
	if entries[0].childCount != 1 {
		t.Errorf("childCount = %d, want 1", entries[0].childCount)
	}
}

func TestBuildDisplayList_ExpandedShowsChildren(t *testing.T) {
	parentID := uuid.New()
	childID := uuid.New()
	b := BoardModel{
		tasks: []model.Task{
			{ID: parentID, Title: "Parent", Status: model.StatusBacklog},
			{ID: childID, ParentTaskID: &parentID, Title: "Child", Status: model.StatusBacklog},
		},
		expanded: map[uuid.UUID]bool{parentID: true},
	}
	entries := b.buildDisplayList()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (expanded parent + child), got %d", len(entries))
	}
	if entries[0].collapsed {
		t.Error("parent should not be collapsed when expanded")
	}
	if !entries[1].isChild {
		t.Error("second entry should be a child")
	}
}

// ---------------------------------------------------------------------------
// taskAnnotation tests
// ---------------------------------------------------------------------------

func TestTaskAnnotation_EmptyWork(t *testing.T) {
	task := model.Task{
		Status:  model.StatusDone,
		Context: model.JSONField{"empty_work": true},
	}
	result := taskAnnotation(task)
	if result == "" {
		t.Error("expected non-empty annotation for empty_work")
	}
}

func TestTaskAnnotation_EmptyFeature(t *testing.T) {
	task := model.Task{
		Status:  model.StatusDone,
		Context: model.JSONField{"empty_feature": true},
	}
	result := taskAnnotation(task)
	if result == "" {
		t.Error("expected non-empty annotation for empty_feature")
	}
}

func TestTaskAnnotation_RetryCount(t *testing.T) {
	task := model.Task{
		Status: model.StatusDone,
		Context: model.JSONField{
			"empty_work":  true,
			"retry_count": float64(2),
		},
	}
	result := taskAnnotation(task)
	if result == "" {
		t.Error("expected non-empty annotation for retry count")
	}
}

func TestTaskAnnotation_PendingDependencies(t *testing.T) {
	task := model.Task{
		Status:        model.StatusBacklog,
		ParentTaskID:  nil, // root task
		DependencyIDs: model.JSONArray{"dep-1", "dep-2"},
	}
	result := taskAnnotation(task)
	if result == "" {
		t.Error("expected non-empty annotation for pending dependencies")
	}
}

func TestTaskAnnotation_NilContext(t *testing.T) {
	task := model.Task{
		Status: model.StatusDone,
	}
	result := taskAnnotation(task)
	if result != "" {
		t.Errorf("expected empty annotation for nil context, got %q", result)
	}
}

func TestTaskAnnotation_NoSpecialFlags(t *testing.T) {
	task := model.Task{
		Status:  model.StatusDone,
		Context: model.JSONField{"some_key": "some_value"},
	}
	result := taskAnnotation(task)
	if result != "" {
		t.Errorf("expected empty annotation for no special flags, got %q", result)
	}
}
