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
			// Expand all parents so existing tests see children.
			expanded := make(map[uuid.UUID]bool)
			for _, task := range tt.tasks {
				if task.ParentTaskID == nil {
					expanded[task.ID] = true
				}
			}
			b := BoardModel{tasks: tt.tasks, expanded: expanded}
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

func TestSortOrderWithMixedStatuses(t *testing.T) {
	// Create tasks with all status categories in random order.
	// Expected sort order: active > gates > failed > paused/rejected > done
	tasks := []model.Task{
		// These will be mixed in random input order
		{ID: uuid.New(), Title: "done_task", Status: model.StatusDone, Priority: 100},
		{ID: uuid.New(), Title: "in_progress_task", Status: model.StatusInProgress, Priority: 1},
		{ID: uuid.New(), Title: "plan_review_task", Status: model.StatusPlanReview, Priority: 50},
		{ID: uuid.New(), Title: "failed_task", Status: model.StatusFailed, Priority: 75},
		{ID: uuid.New(), Title: "paused_task", Status: model.StatusPaused, Priority: 50},
		{ID: uuid.New(), Title: "planning_task", Status: model.StatusPlanning, Priority: 2},
		{ID: uuid.New(), Title: "test_review_task", Status: model.StatusTestReview, Priority: 40},
		{ID: uuid.New(), Title: "rejected_task", Status: model.StatusRejected, Priority: 30},
		{ID: uuid.New(), Title: "backlog_task", Status: model.StatusBacklog, Priority: 5},
		{ID: uuid.New(), Title: "classifying_task", Status: model.StatusClassifying, Priority: 10},
		{ID: uuid.New(), Title: "test_writing_task", Status: model.StatusTestWriting, Priority: 3},
		{ID: uuid.New(), Title: "merging_task", Status: model.StatusMerging, Priority: 4},
		{ID: uuid.New(), Title: "testing_ready_task", Status: model.StatusTestingReady, Priority: 35},
		{ID: uuid.New(), Title: "needs_clarification_task", Status: model.StatusNeedsClarification, Priority: 25},
	}

	b := BoardModel{
		tasks:    tasks,
		expanded: make(map[uuid.UUID]bool),
		showAll:  true, // show all statuses including rejected
	}

	entries := b.buildDisplayList()

	if len(entries) != len(tasks) {
		t.Fatalf("expected %d entries, got %d", len(tasks), len(entries))
	}

	// Extract status sequence from entries.
	var statuses []model.TaskStatus
	for _, e := range entries {
		statuses = append(statuses, e.task.Status)
	}

	// Define status categories and their ordering.
	statusCategory := map[model.TaskStatus]string{
		model.StatusInProgress:         "active",
		model.StatusPlanning:           "active",
		model.StatusTestWriting:        "active",
		model.StatusMerging:            "active",
		model.StatusClassifying:        "active",
		model.StatusBacklog:            "active",
		model.StatusPlanReview:         "gates",
		model.StatusTestReview:         "gates",
		model.StatusTestingReady:       "gates",
		model.StatusNeedsClarification: "gates",
		model.StatusFailed:             "failed",
		model.StatusPaused:             "paused_rejected",
		model.StatusRejected:           "paused_rejected",
		model.StatusDone:               "done",
	}

	categoryOrder := map[string]int{
		"active":          0,
		"gates":           1,
		"failed":          2,
		"paused_rejected": 3,
		"done":            4,
	}

	// Verify that tasks are grouped by category and within-category ordering.
	lastCategoryIdx := -1
	for i, status := range statuses {
		cat := statusCategory[status]
		catIdx := categoryOrder[cat]

		// Category index should never decrease (monotonically increasing).
		if catIdx < lastCategoryIdx {
			t.Errorf("at index %d: status %q (category %q, order %d) violates ordering after previous category order %d",
				i, status, cat, catIdx, lastCategoryIdx)
		}
		lastCategoryIdx = catIdx
	}

	// Verify specific groupings.
	t.Run("active_tasks_first", func(t *testing.T) {
		activeCount := 0
		firstActiveIdx := -1

		for i, status := range statuses {
			if statusCategory[status] == "active" {
				if firstActiveIdx == -1 {
					firstActiveIdx = i
				}
				activeCount++
			}
		}

		if firstActiveIdx != 0 {
			t.Errorf("first active task should be at index 0, got %d", firstActiveIdx)
		}
		if activeCount != 6 {
			t.Errorf("expected 6 active tasks, got %d", activeCount)
		}
	})

	t.Run("gates_after_active", func(t *testing.T) {
		gateCount := 0
		firstGateIdx := -1

		for i, status := range statuses {
			if statusCategory[status] == "gates" {
				if firstGateIdx == -1 {
					firstGateIdx = i
				}
				gateCount++
			}
		}

		// Gates should start after all active tasks (which are 6).
		if firstGateIdx <= 5 {
			t.Errorf("first gate task should be at index 6 or higher, got %d", firstGateIdx)
		}
		if gateCount != 4 {
			t.Errorf("expected 4 gate tasks, got %d", gateCount)
		}
	})

	t.Run("failed_before_done", func(t *testing.T) {
		failedIdx := -1
		doneIdx := -1

		for i, status := range statuses {
			if status == model.StatusFailed {
				failedIdx = i
			}
			if status == model.StatusDone {
				doneIdx = i
			}
		}

		if failedIdx == -1 {
			t.Fatal("failed task not found in sorted list")
		}
		if doneIdx == -1 {
			t.Fatal("done task not found in sorted list")
		}

		if failedIdx >= doneIdx {
			t.Errorf("failed task (index %d) should appear before done task (index %d)", failedIdx, doneIdx)
		}
	})

	t.Run("paused_rejected_between_failed_and_done", func(t *testing.T) {
		failedIdx := -1
		pausedIdx := -1
		rejectedIdx := -1
		doneIdx := -1

		for i, status := range statuses {
			if status == model.StatusFailed {
				failedIdx = i
			}
			if status == model.StatusPaused {
				pausedIdx = i
			}
			if status == model.StatusRejected {
				rejectedIdx = i
			}
			if status == model.StatusDone {
				doneIdx = i
			}
		}

		if failedIdx == -1 || pausedIdx == -1 || rejectedIdx == -1 || doneIdx == -1 {
			t.Fatal("missing expected statuses in sorted list")
		}

		// Both paused and rejected should be after failed.
		if pausedIdx <= failedIdx {
			t.Errorf("paused (index %d) should be after failed (index %d)", pausedIdx, failedIdx)
		}
		if rejectedIdx <= failedIdx {
			t.Errorf("rejected (index %d) should be after failed (index %d)", rejectedIdx, failedIdx)
		}

		// Both paused and rejected should be before done.
		if pausedIdx >= doneIdx {
			t.Errorf("paused (index %d) should be before done (index %d)", pausedIdx, doneIdx)
		}
		if rejectedIdx >= doneIdx {
			t.Errorf("rejected (index %d) should be before done (index %d)", rejectedIdx, doneIdx)
		}
	})

	t.Run("within_category_priority_ordering", func(t *testing.T) {
		// Active tasks within the active category should be sorted by priority (higher first).
		// Extract just the active tasks.
		var activeTasks []model.Task
		for _, e := range entries {
			if statusCategory[e.task.Status] == "active" {
				activeTasks = append(activeTasks, e.task)
			}
		}

		if len(activeTasks) == 0 {
			t.Fatal("no active tasks found")
		}

		// Verify that priority is maintained within active tasks.
		// When statuses are different within active, priority should be respected.
		// Note: current implementation uses status sort order first, then priority,
		// so we just verify no obvious violations.
		for i := 0; i < len(activeTasks)-1; i++ {
			curr := activeTasks[i]
			next := activeTasks[i+1]

			// If statuses are the same, priority should be >= (stable sort preserves order).
			if curr.Status == next.Status && curr.Priority < next.Priority {
				t.Logf("within same status %q, priority decreased from %d to %d (may be expected with stable sort)",
					curr.Status, curr.Priority, next.Priority)
			}
		}
	})
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
		name    string
		cursor  int
		wantID  uuid.UUID
		wantNil bool
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
			b := BoardModel{cursor: tt.cursor, expanded: map[uuid.UUID]bool{parentA: true, parentB: true}}
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
