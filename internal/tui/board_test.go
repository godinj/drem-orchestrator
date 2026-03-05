package tui

import (
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func ptr(id uuid.UUID) *uuid.UUID { return &id }

func TestBuildDisplayList(t *testing.T) {
	parentA := uuid.New()
	parentB := uuid.New()
	childA1 := uuid.New()
	childA2 := uuid.New()
	childB1 := uuid.New()
	orphanParent := uuid.New()
	orphanChild := uuid.New()

	tests := []struct {
		name    string
		tasks   []model.Task
		wantLen int
		// verify checks the built display list.
		verify func(t *testing.T, entries []displayEntry)
	}{
		{
			name:    "empty",
			tasks:   nil,
			wantLen: 0,
		},
		{
			name: "roots only",
			tasks: []model.Task{
				{ID: parentA, Title: "Task A", Status: model.StatusBacklog, Priority: 1},
				{ID: parentB, Title: "Task B", Status: model.StatusInProgress, Priority: 0},
			},
			wantLen: 2,
			verify: func(t *testing.T, entries []displayEntry) {
				// IN_PROGRESS sorts before BACKLOG.
				if entries[0].task.ID != parentB {
					t.Errorf("expected IN_PROGRESS task first, got %s", entries[0].task.Title)
				}
				if entries[0].isChild {
					t.Error("root task should not be marked as child")
				}
			},
		},
		{
			name: "parent with children",
			tasks: []model.Task{
				{ID: parentA, Title: "Parent A", Status: model.StatusInProgress, Priority: 1},
				{ID: childA1, ParentTaskID: ptr(parentA), Title: "Child A1", Status: model.StatusDone, Priority: 0},
				{ID: childA2, ParentTaskID: ptr(parentA), Title: "Child A2", Status: model.StatusBacklog, Priority: 0},
			},
			wantLen: 3,
			verify: func(t *testing.T, entries []displayEntry) {
				// Parent first.
				if entries[0].task.ID != parentA || entries[0].isChild {
					t.Error("first entry should be parent")
				}
				// Children sorted: BACKLOG before DONE by status priority.
				if !entries[1].isChild || entries[1].connector != "├─ " {
					t.Errorf("second entry should be child with mid connector, got connector=%q isChild=%v",
						entries[1].connector, entries[1].isChild)
				}
				if !entries[2].isChild || entries[2].connector != "└─ " {
					t.Errorf("third entry should be child with last connector, got connector=%q",
						entries[2].connector)
				}
			},
		},
		{
			name: "multiple parents interleaved",
			tasks: []model.Task{
				{ID: parentA, Title: "Parent A", Status: model.StatusBacklog, Priority: 0},
				{ID: parentB, Title: "Parent B", Status: model.StatusInProgress, Priority: 0},
				{ID: childA1, ParentTaskID: ptr(parentA), Title: "Child A1", Status: model.StatusBacklog, Priority: 0},
				{ID: childB1, ParentTaskID: ptr(parentB), Title: "Child B1", Status: model.StatusBacklog, Priority: 0},
			},
			wantLen: 4,
			verify: func(t *testing.T, entries []displayEntry) {
				// Parent B (IN_PROGRESS) should sort first.
				if entries[0].task.ID != parentB {
					t.Errorf("expected Parent B first, got %s", entries[0].task.Title)
				}
				if entries[1].task.ID != childB1 || !entries[1].isChild {
					t.Error("expected Child B1 after Parent B")
				}
				if entries[2].task.ID != parentA {
					t.Errorf("expected Parent A third, got %s", entries[2].task.Title)
				}
				if entries[3].task.ID != childA1 || !entries[3].isChild {
					t.Error("expected Child A1 after Parent A")
				}
			},
		},
		{
			name: "orphan subtask appended at end",
			tasks: []model.Task{
				{ID: parentA, Title: "Parent A", Status: model.StatusBacklog, Priority: 0},
				{ID: orphanChild, ParentTaskID: ptr(orphanParent), Title: "Orphan", Status: model.StatusBacklog, Priority: 0},
			},
			wantLen: 2,
			verify: func(t *testing.T, entries []displayEntry) {
				if entries[0].task.ID != parentA {
					t.Error("expected root first")
				}
				if entries[1].task.ID != orphanChild {
					t.Error("expected orphan at end")
				}
				// Orphans are appended as non-child entries.
				if entries[1].isChild {
					t.Error("orphan should not be marked as child")
				}
			},
		},
		{
			name: "single child gets last connector",
			tasks: []model.Task{
				{ID: parentA, Title: "Parent A", Status: model.StatusBacklog, Priority: 0},
				{ID: childA1, ParentTaskID: ptr(parentA), Title: "Only Child", Status: model.StatusBacklog, Priority: 0},
			},
			wantLen: 2,
			verify: func(t *testing.T, entries []displayEntry) {
				if entries[1].connector != "└─ " {
					t.Errorf("single child should get last connector, got %q", entries[1].connector)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := BoardModel{tasks: tt.tasks}
			entries := b.buildDisplayList()
			if len(entries) != tt.wantLen {
				t.Fatalf("expected %d entries, got %d", tt.wantLen, len(entries))
			}
			if tt.verify != nil {
				tt.verify(t, entries)
			}
		})
	}
}

func TestSelected(t *testing.T) {
	parentA := uuid.New()
	childA1 := uuid.New()
	childA2 := uuid.New()
	parentB := uuid.New()

	tasks := []model.Task{
		{ID: parentA, Title: "Parent A", Status: model.StatusInProgress, Priority: 1},
		{ID: childA1, ParentTaskID: ptr(parentA), Title: "Child A1", Status: model.StatusDone, Priority: 0},
		{ID: childA2, ParentTaskID: ptr(parentA), Title: "Child A2", Status: model.StatusBacklog, Priority: 0},
		{ID: parentB, Title: "Parent B", Status: model.StatusBacklog, Priority: 0},
	}

	tests := []struct {
		name     string
		cursor   int
		wantID   uuid.UUID
		wantNil  bool
	}{
		{name: "cursor 0 selects first root", cursor: 0, wantID: parentA},
		{name: "cursor 1 selects first child", cursor: 1},
		{name: "cursor 2 selects second child", cursor: 2},
		{name: "cursor on last parent", cursor: 3, wantID: parentB},
		{name: "cursor beyond end clamps", cursor: 99},
		{name: "empty tasks", cursor: 0, wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := BoardModel{cursor: tt.cursor}
			if tt.name != "empty tasks" {
				b.tasks = tasks
			}

			selected := b.Selected()

			if tt.wantNil {
				if selected != nil {
					t.Fatal("expected nil, got a task")
				}
				return
			}

			if selected == nil {
				t.Fatal("expected a task, got nil")
			}

			if tt.wantID != uuid.Nil && selected.ID != tt.wantID {
				t.Errorf("expected task %v, got %v (%s)", tt.wantID, selected.ID, selected.Title)
			}
		})
	}
}
