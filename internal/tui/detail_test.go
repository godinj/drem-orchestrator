package tui

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestDeletableItems(t *testing.T) {
	tests := []struct {
		name         string
		task         *model.Task
		comments     []model.TaskComment
		wantLen      int
		wantPlanLen  int // expected number of deleteItemPlanStep items
		wantComments int // expected number of deleteItemComment items
	}{
		{
			name: "task with 2 comments and 3-step plan",
			task: &model.Task{
				ID:     uuid.New(),
				Status: model.StatusPlanReview,
				Plan: model.JSONField{
					"subtasks": []any{
						map[string]any{"title": "Step 1"},
						map[string]any{"title": "Step 2"},
						map[string]any{"title": "Step 3"},
					},
				},
			},
			comments: []model.TaskComment{
				{ID: uuid.New(), Author: "user", Body: "Comment 1"},
				{ID: uuid.New(), Author: "system", Body: "Comment 2"},
			},
			wantLen:      6, // 3 plan + 2 comments + 1 task
			wantPlanLen:  3,
			wantComments: 2,
		},
		{
			name: "task with no comments and no plan",
			task: &model.Task{
				ID:     uuid.New(),
				Status: model.StatusBacklog,
			},
			comments:     nil,
			wantLen:      1, // task only
			wantPlanLen:  0,
			wantComments: 0,
		},
		{
			name: "task with comments only",
			task: &model.Task{
				ID:     uuid.New(),
				Status: model.StatusInProgress,
			},
			comments: []model.TaskComment{
				{ID: uuid.New(), Author: "user", Body: "A comment"},
				{ID: uuid.New(), Author: "user", Body: "Another comment"},
				{ID: uuid.New(), Author: "system", Body: "System note"},
			},
			wantLen:      4, // 3 comments + 1 task
			wantPlanLen:  0,
			wantComments: 3,
		},
		{
			name: "task with plan steps only",
			task: &model.Task{
				ID:     uuid.New(),
				Status: model.StatusPlanReview,
				Plan: model.JSONField{
					"subtasks": []any{
						map[string]any{"title": "Only step"},
					},
				},
			},
			comments:     nil,
			wantLen:      2, // 1 plan + 1 task
			wantPlanLen:  1,
			wantComments: 0,
		},
		{
			name:         "nil task",
			task:         nil,
			comments:     nil,
			wantLen:      0,
			wantPlanLen:  0,
			wantComments: 0,
		},
		{
			name: "plan steps only included for plan_review status",
			task: &model.Task{
				ID:     uuid.New(),
				Status: model.StatusInProgress, // not plan_review
				Plan: model.JSONField{
					"subtasks": []any{
						map[string]any{"title": "Step 1"},
					},
				},
			},
			comments:     nil,
			wantLen:      1, // task only (plan steps excluded for non-plan_review)
			wantPlanLen:  0,
			wantComments: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DetailModel{
				task:     tt.task,
				comments: tt.comments,
			}
			items := d.deletableItems()
			if len(items) != tt.wantLen {
				t.Fatalf("expected %d items, got %d", tt.wantLen, len(items))
			}

			var planCount, commentCount int
			for _, item := range items {
				switch item.kind {
				case deleteItemPlanStep:
					planCount++
				case deleteItemComment:
					commentCount++
				}
			}
			if planCount != tt.wantPlanLen {
				t.Errorf("expected %d plan step items, got %d", tt.wantPlanLen, planCount)
			}
			if commentCount != tt.wantComments {
				t.Errorf("expected %d comment items, got %d", tt.wantComments, commentCount)
			}

			// Verify ordering: plan steps come before comments.
			seenComment := false
			for _, item := range items {
				if item.kind == deleteItemComment {
					seenComment = true
				}
				if item.kind == deleteItemPlanStep && seenComment {
					t.Error("plan step appeared after comment; expected plan steps first")
				}
			}

			// Verify indices are sequential within each kind.
			planIdx, commentIdx := 0, 0
			for _, item := range items {
				switch item.kind {
				case deleteItemPlanStep:
					if item.index != planIdx {
						t.Errorf("plan step index: expected %d, got %d", planIdx, item.index)
					}
					planIdx++
				case deleteItemComment:
					if item.index != commentIdx {
						t.Errorf("comment index: expected %d, got %d", commentIdx, item.index)
					}
					commentIdx++
				}
			}
		})
	}
}

func TestSelectedDeleteItem(t *testing.T) {
	task := &model.Task{
		ID:     uuid.New(),
		Status: model.StatusPlanReview,
		Plan: model.JSONField{
			"subtasks": []any{
				map[string]any{"title": "Step 1"},
				map[string]any{"title": "Step 2"},
			},
		},
	}
	comments := []model.TaskComment{
		{ID: uuid.New(), Author: "user", Body: "Comment 1"},
	}

	tests := []struct {
		name         string
		task         *model.Task
		comments     []model.TaskComment
		deleteCursor int
		wantNil      bool
		wantKind     deleteItemKind
		wantIndex    int
	}{
		{
			name:         "cursor at 0 returns first plan step",
			task:         task,
			comments:     comments,
			deleteCursor: 0,
			wantKind:     deleteItemPlanStep,
			wantIndex:    0,
		},
		{
			name:         "cursor at last index returns comment",
			task:         task,
			comments:     comments,
			deleteCursor: 2, // 2 plan steps + 1 comment = index 2
			wantKind:     deleteItemComment,
			wantIndex:    0,
		},
		{
			name:         "cursor at -1 returns nil",
			task:         task,
			comments:     comments,
			deleteCursor: -1,
			wantNil:      true,
		},
		{
			name:         "cursor beyond bounds returns nil",
			task:         task,
			comments:     comments,
			deleteCursor: 99,
			wantNil:      true,
		},
		{
			name:         "no deletable items returns nil",
			task:         nil,
			comments:     nil,
			deleteCursor: 0,
			wantNil:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DetailModel{
				task:         tt.task,
				comments:     tt.comments,
				deleteCursor: tt.deleteCursor,
			}
			item := d.selectedDeleteItem()
			if tt.wantNil {
				if item != nil {
					t.Fatalf("expected nil, got %+v", item)
				}
				return
			}
			if item == nil {
				t.Fatal("expected non-nil item, got nil")
			}
			if item.kind != tt.wantKind {
				t.Errorf("expected kind %d, got %d", tt.wantKind, item.kind)
			}
			if item.index != tt.wantIndex {
				t.Errorf("expected index %d, got %d", tt.wantIndex, item.index)
			}
		})
	}
}

func TestIsDeleteTarget(t *testing.T) {
	task := &model.Task{
		ID:     uuid.New(),
		Status: model.StatusPlanReview,
		Plan: model.JSONField{
			"subtasks": []any{
				map[string]any{"title": "Step 1"},
			},
		},
	}
	comments := []model.TaskComment{
		{ID: uuid.New(), Author: "user", Body: "Comment"},
	}

	tests := []struct {
		name         string
		deleteMode   bool
		deleteCursor int
		queryKind    deleteItemKind
		queryIndex   int
		want         bool
	}{
		{
			name:         "matching target in delete mode",
			deleteMode:   true,
			deleteCursor: 0,
			queryKind:    deleteItemPlanStep,
			queryIndex:   0,
			want:         true,
		},
		{
			name:         "non-matching kind",
			deleteMode:   true,
			deleteCursor: 0,
			queryKind:    deleteItemComment,
			queryIndex:   0,
			want:         false,
		},
		{
			name:         "non-matching index",
			deleteMode:   true,
			deleteCursor: 0,
			queryKind:    deleteItemPlanStep,
			queryIndex:   1,
			want:         false,
		},
		{
			name:         "delete mode off returns false",
			deleteMode:   false,
			deleteCursor: 0,
			queryKind:    deleteItemPlanStep,
			queryIndex:   0,
			want:         false,
		},
		{
			name:         "cursor on comment matches comment target",
			deleteMode:   true,
			deleteCursor: 1, // second item = first comment
			queryKind:    deleteItemComment,
			queryIndex:   0,
			want:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DetailModel{
				task:         task,
				comments:     comments,
				deleteMode:   tt.deleteMode,
				deleteCursor: tt.deleteCursor,
			}
			got := d.isDeleteTarget(tt.queryKind, tt.queryIndex)
			if got != tt.want {
				t.Errorf("isDeleteTarget(%d, %d) = %v, want %v",
					tt.queryKind, tt.queryIndex, got, tt.want)
			}
		})
	}
}

func TestDeletableItemsIncludesTask(t *testing.T) {
	parentID := uuid.New()

	tests := []struct {
		name     string
		task     *model.Task
		comments []model.TaskComment
		wantTask bool // expect a deleteItemTask entry
		wantLast bool // the task entry should be the last item
	}{
		{
			name: "task with plan and comments includes task as last item",
			task: &model.Task{
				ID:     uuid.New(),
				Status: model.StatusPlanReview,
				Plan: model.JSONField{
					"subtasks": []any{
						map[string]any{"title": "Step 1"},
					},
				},
			},
			comments: []model.TaskComment{
				{ID: uuid.New(), Author: "user", Body: "Comment 1"},
			},
			wantTask: true,
			wantLast: true,
		},
		{
			name: "task with comments only includes task as last item",
			task: &model.Task{
				ID:     uuid.New(),
				Status: model.StatusInProgress,
			},
			comments: []model.TaskComment{
				{ID: uuid.New(), Author: "user", Body: "Comment 1"},
			},
			wantTask: true,
			wantLast: true,
		},
		{
			name: "task with no plan and no comments includes task as only item",
			task: &model.Task{
				ID:     uuid.New(),
				Status: model.StatusBacklog,
			},
			comments: nil,
			wantTask: true,
			wantLast: true,
		},
		{
			name: "subtask with ParentTaskID includes task as deletable item",
			task: &model.Task{
				ID:           uuid.New(),
				ParentTaskID: &parentID,
				Status:       model.StatusInProgress,
			},
			comments: nil,
			wantTask: true,
			wantLast: true,
		},
		{
			name:     "nil task does not include task item",
			task:     nil,
			comments: nil,
			wantTask: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DetailModel{
				task:     tt.task,
				comments: tt.comments,
			}
			items := d.deletableItems()

			// Check that a deleteItemTask entry exists.
			var taskCount int
			var lastItem *deleteItem
			for i := range items {
				if items[i].kind == deleteItemTask {
					taskCount++
				}
				lastItem = &items[i]
			}

			if tt.wantTask && taskCount != 1 {
				t.Fatalf("expected exactly 1 deleteItemTask entry, got %d", taskCount)
			}
			if !tt.wantTask && taskCount != 0 {
				t.Fatalf("expected no deleteItemTask entry, got %d", taskCount)
			}

			// Verify the task entry is the last item.
			if tt.wantLast && lastItem != nil && lastItem.kind != deleteItemTask {
				t.Errorf("expected deleteItemTask as last item, got kind %d", lastItem.kind)
			}
		})
	}
}

func TestDeletableItemsTaskAppearsRegardlessOfStatus(t *testing.T) {
	// Unlike plan steps (which only appear in plan_review), the task deletion
	// item should appear for all task statuses.
	statuses := []model.TaskStatus{
		model.StatusBacklog,
		model.StatusPlanning,
		model.StatusPlanReview,
		model.StatusTestWriting,
		model.StatusTestReview,
		model.StatusInProgress,
		model.StatusTestingReady,
		model.StatusMerging,
		model.StatusPaused,
		model.StatusDone,
		model.StatusFailed,
		model.StatusRejected,
	}

	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			d := DetailModel{
				task: &model.Task{
					ID:     uuid.New(),
					Status: status,
				},
			}
			items := d.deletableItems()

			var hasTask bool
			for _, item := range items {
				if item.kind == deleteItemTask {
					hasTask = true
					break
				}
			}
			if !hasTask {
				t.Errorf("expected deleteItemTask for status %s, but it was not present", status)
			}
		})
	}
}

func TestIsDeleteTargetTask(t *testing.T) {
	tests := []struct {
		name         string
		deleteMode   bool
		deleteCursor int
		queryKind    deleteItemKind
		queryIndex   int
		want         bool
	}{
		{
			name:         "cursor on task item matches deleteItemTask",
			deleteMode:   true,
			deleteCursor: 0, // task is only item (no plan steps, no comments)
			queryKind:    deleteItemTask,
			queryIndex:   0,
			want:         true,
		},
		{
			name:         "cursor on task item does not match comment kind",
			deleteMode:   true,
			deleteCursor: 0,
			queryKind:    deleteItemComment,
			queryIndex:   0,
			want:         false,
		},
		{
			name:         "delete mode off returns false for task kind",
			deleteMode:   false,
			deleteCursor: 0,
			queryKind:    deleteItemTask,
			queryIndex:   0,
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DetailModel{
				task: &model.Task{
					ID:     uuid.New(),
					Status: model.StatusBacklog,
				},
				deleteMode:   tt.deleteMode,
				deleteCursor: tt.deleteCursor,
			}
			got := d.isDeleteTarget(tt.queryKind, tt.queryIndex)
			if got != tt.want {
				t.Errorf("isDeleteTarget(%d, %d) = %v, want %v",
					tt.queryKind, tt.queryIndex, got, tt.want)
			}
		})
	}
}

func TestIsDeleteSectionTask(t *testing.T) {
	tests := []struct {
		name         string
		deleteMode   bool
		deleteCursor int
		queryKind    deleteItemKind
		want         bool
	}{
		{
			name:         "cursor on task item matches task section",
			deleteMode:   true,
			deleteCursor: 0, // task is only item
			queryKind:    deleteItemTask,
			want:         true,
		},
		{
			name:         "cursor on task item does not match comment section",
			deleteMode:   true,
			deleteCursor: 0,
			queryKind:    deleteItemComment,
			want:         false,
		},
		{
			name:         "cursor on task item does not match plan section",
			deleteMode:   true,
			deleteCursor: 0,
			queryKind:    deleteItemPlanStep,
			want:         false,
		},
		{
			name:         "delete mode off returns false for task section",
			deleteMode:   false,
			deleteCursor: 0,
			queryKind:    deleteItemTask,
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DetailModel{
				task: &model.Task{
					ID:     uuid.New(),
					Status: model.StatusBacklog,
				},
				deleteMode:   tt.deleteMode,
				deleteCursor: tt.deleteCursor,
			}
			got := d.isDeleteSection(tt.queryKind)
			if got != tt.want {
				t.Errorf("isDeleteSection(%d) = %v, want %v",
					tt.queryKind, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Exit reason display
// ---------------------------------------------------------------------------

func TestDetailView_ShowsExitReason(t *testing.T) {
	// When an agent's Config contains exit_reason, the detail view should
	// render it in the warnings/info section so supervisors can see why
	// an agent stopped.
	agentID := uuid.New()
	taskID := uuid.New()

	task := &model.Task{
		ID:          taskID,
		Title:       "task-with-exit-reason",
		Description: "a task whose agent exited",
		Status:      model.StatusFailed,
		Context: model.JSONField{
			"failure_reason": "agent exited with non-zero code",
		},
	}

	agent := &model.Agent{
		ID:        agentID,
		Name:      "test-agent-exit",
		AgentType: model.AgentCoder,
		Status:    model.AgentDead,
		Config: model.JSONField{
			"exit_reason":    "context_limit",
			"exit_last_tool": "Read",
			"exit_summary":   "Was reading large files when context limit hit",
		},
	}

	d := DetailModel{
		task:   task,
		agent:  agent,
		width:  120,
		height: 50,
	}

	output := d.View()

	// Verify exit reason is rendered in the output.
	if !strings.Contains(output, "context_limit") {
		t.Errorf("expected detail view to contain exit_reason 'context_limit', got:\n%s", output)
	}

	// Verify exit last tool is rendered.
	if !strings.Contains(output, "Read") {
		t.Errorf("expected detail view to contain exit_last_tool 'Read', got:\n%s", output)
	}

	// Verify exit summary is rendered.
	if !strings.Contains(output, "Was reading large files when context limit hit") {
		t.Errorf("expected detail view to contain exit_summary, got:\n%s", output)
	}
}

func TestDetailView_NoExitReason(t *testing.T) {
	// When an agent's Config does NOT contain exit_reason, the detail view
	// should not render any exit info section (backwards compatible).
	agentID := uuid.New()
	taskID := uuid.New()

	task := &model.Task{
		ID:          taskID,
		Title:       "task-no-exit-reason",
		Description: "a normal task",
		Status:      model.StatusInProgress,
	}

	agent := &model.Agent{
		ID:        agentID,
		Name:      "test-agent-normal",
		AgentType: model.AgentCoder,
		Status:    model.AgentWorking,
		Config:    model.JSONField{"pid": float64(12345)},
	}

	d := DetailModel{
		task:   task,
		agent:  agent,
		width:  120,
		height: 50,
	}

	output := d.View()

	// Should not contain any exit reason labels.
	if strings.Contains(output, "Exit reason") || strings.Contains(output, "exit_reason") {
		t.Errorf("expected no exit reason in detail view without exit info, got:\n%s", output)
	}
}

func TestDetailView_ShowsExitReason_SuccessAgent(t *testing.T) {
	// Exit info should also render for agents that exited successfully,
	// e.g. to show what tools they last used and work summary.
	agentID := uuid.New()
	taskID := uuid.New()

	task := &model.Task{
		ID:          taskID,
		Title:       "task-success-exit",
		Description: "completed successfully",
		Status:      model.StatusDone,
	}

	agent := &model.Agent{
		ID:        agentID,
		Name:      "test-agent-success",
		AgentType: model.AgentCoder,
		Status:    model.AgentIdle,
		Config: model.JSONField{
			"exit_reason":    "success",
			"exit_last_tool": "Write",
			"exit_summary":   "Created 2 files, modified 1",
		},
	}

	d := DetailModel{
		task:   task,
		agent:  agent,
		width:  120,
		height: 50,
	}

	output := d.View()

	if !strings.Contains(output, "success") {
		t.Errorf("expected detail view to show exit_reason 'success', got:\n%s", output)
	}
	if !strings.Contains(output, "Created 2 files, modified 1") {
		t.Errorf("expected detail view to show exit_summary, got:\n%s", output)
	}
}

func TestIsDeleteSection(t *testing.T) {
	task := &model.Task{
		ID:     uuid.New(),
		Status: model.StatusPlanReview,
		Plan: model.JSONField{
			"subtasks": []any{
				map[string]any{"title": "Step 1"},
			},
		},
	}
	comments := []model.TaskComment{
		{ID: uuid.New(), Author: "user", Body: "Comment"},
	}

	tests := []struct {
		name         string
		deleteMode   bool
		deleteCursor int
		queryKind    deleteItemKind
		want         bool
	}{
		{
			name:         "cursor on plan step matches plan section",
			deleteMode:   true,
			deleteCursor: 0,
			queryKind:    deleteItemPlanStep,
			want:         true,
		},
		{
			name:         "cursor on plan step does not match comment section",
			deleteMode:   true,
			deleteCursor: 0,
			queryKind:    deleteItemComment,
			want:         false,
		},
		{
			name:         "cursor on comment matches comment section",
			deleteMode:   true,
			deleteCursor: 1, // first comment
			queryKind:    deleteItemComment,
			want:         true,
		},
		{
			name:         "cursor on comment does not match plan section",
			deleteMode:   true,
			deleteCursor: 1,
			queryKind:    deleteItemPlanStep,
			want:         false,
		},
		{
			name:         "delete mode off returns false",
			deleteMode:   false,
			deleteCursor: 0,
			queryKind:    deleteItemPlanStep,
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DetailModel{
				task:         task,
				comments:     comments,
				deleteMode:   tt.deleteMode,
				deleteCursor: tt.deleteCursor,
			}
			got := d.isDeleteSection(tt.queryKind)
			if got != tt.want {
				t.Errorf("isDeleteSection(%d) = %v, want %v",
					tt.queryKind, got, tt.want)
			}
		})
	}
}
