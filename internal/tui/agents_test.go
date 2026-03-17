package tui

import (
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestVisibleAgents(t *testing.T) {
	taskA := uuid.New()
	taskB := uuid.New()
	subtaskA1 := uuid.New()

	tests := []struct {
		name       string
		agents     []model.Agent
		autoFilter bool
		filterTask *uuid.UUID
		subtaskIDs []uuid.UUID
		showArchived bool
		wantLen    int
	}{
		{
			name: "no filter returns all non-archived agents",
			agents: []model.Agent{
				{ID: uuid.New(), Name: "agent-1", Status: model.AgentWorking, CurrentTaskID: ptr(taskA)},
				{ID: uuid.New(), Name: "agent-2", Status: model.AgentWorking, CurrentTaskID: ptr(taskB)},
			},
			autoFilter: true,
			filterTask: nil,
			wantLen:    2,
		},
		{
			name: "filter by taskID returns matching agents",
			agents: []model.Agent{
				{ID: uuid.New(), Name: "agent-1", Status: model.AgentWorking, CurrentTaskID: ptr(taskA)},
				{ID: uuid.New(), Name: "agent-2", Status: model.AgentWorking, CurrentTaskID: ptr(taskB)},
			},
			autoFilter: true,
			filterTask: &taskA,
			wantLen:    1,
		},
		{
			name: "filter includes subtask agents",
			agents: []model.Agent{
				{ID: uuid.New(), Name: "agent-1", Status: model.AgentWorking, CurrentTaskID: ptr(taskA)},
				{ID: uuid.New(), Name: "agent-sub", Status: model.AgentWorking, CurrentTaskID: ptr(subtaskA1)},
				{ID: uuid.New(), Name: "agent-other", Status: model.AgentWorking, CurrentTaskID: ptr(taskB)},
			},
			autoFilter: true,
			filterTask: &taskA,
			subtaskIDs: []uuid.UUID{subtaskA1},
			wantLen:    2,
		},
		{
			name: "no agents match filter returns empty",
			agents: []model.Agent{
				{ID: uuid.New(), Name: "agent-1", Status: model.AgentWorking, CurrentTaskID: ptr(taskB)},
			},
			autoFilter: true,
			filterTask: &taskA,
			wantLen:    0,
		},
		{
			name:       "empty agents list returns empty",
			agents:     nil,
			autoFilter: true,
			filterTask: nil,
			wantLen:    0,
		},
		{
			name: "dead and idle agents hidden when showArchived is false",
			agents: []model.Agent{
				{ID: uuid.New(), Name: "working", Status: model.AgentWorking, CurrentTaskID: ptr(taskA)},
				{ID: uuid.New(), Name: "dead", Status: model.AgentDead, CurrentTaskID: ptr(taskA)},
				{ID: uuid.New(), Name: "idle", Status: model.AgentIdle},
			},
			autoFilter:   true,
			filterTask:   nil,
			showArchived: false,
			wantLen:      1,
		},
		{
			name: "dead and idle agents shown when showArchived is true",
			agents: []model.Agent{
				{ID: uuid.New(), Name: "working", Status: model.AgentWorking, CurrentTaskID: ptr(taskA)},
				{ID: uuid.New(), Name: "dead", Status: model.AgentDead, CurrentTaskID: ptr(taskA)},
				{ID: uuid.New(), Name: "idle", Status: model.AgentIdle},
			},
			autoFilter:   true,
			filterTask:   nil,
			showArchived: true,
			wantLen:      3,
		},
		{
			name: "autoFilter off skips task filter",
			agents: []model.Agent{
				{ID: uuid.New(), Name: "agent-1", Status: model.AgentWorking, CurrentTaskID: ptr(taskA)},
				{ID: uuid.New(), Name: "agent-2", Status: model.AgentWorking, CurrentTaskID: ptr(taskB)},
			},
			autoFilter: false,
			filterTask: &taskA,
			wantLen:    2,
		},
		{
			name: "agent with nil CurrentTaskID excluded when filter active",
			agents: []model.Agent{
				{ID: uuid.New(), Name: "no-task", Status: model.AgentWorking, CurrentTaskID: nil},
				{ID: uuid.New(), Name: "has-task", Status: model.AgentWorking, CurrentTaskID: ptr(taskA)},
			},
			autoFilter: true,
			filterTask: &taskA,
			wantLen:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := AgentsModel{
				agents:       tt.agents,
				autoFilter:   tt.autoFilter,
				filterTaskID: tt.filterTask,
				subtaskIDs:   tt.subtaskIDs,
				showArchived: tt.showArchived,
			}
			visible := a.visibleAgents()
			if len(visible) != tt.wantLen {
				t.Fatalf("expected %d visible agents, got %d", tt.wantLen, len(visible))
			}
		})
	}
}

func TestIsSubtaskID(t *testing.T) {
	sub1 := uuid.New()
	sub2 := uuid.New()
	unrelated := uuid.New()

	tests := []struct {
		name       string
		subtaskIDs []uuid.UUID
		query      uuid.UUID
		want       bool
	}{
		{
			name:       "matching subtask ID",
			subtaskIDs: []uuid.UUID{sub1, sub2},
			query:      sub1,
			want:       true,
		},
		{
			name:       "unrelated ID",
			subtaskIDs: []uuid.UUID{sub1, sub2},
			query:      unrelated,
			want:       false,
		},
		{
			name:       "empty subtask list",
			subtaskIDs: nil,
			query:      sub1,
			want:       false,
		},
		{
			name:       "second subtask matches",
			subtaskIDs: []uuid.UUID{sub1, sub2},
			query:      sub2,
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := AgentsModel{subtaskIDs: tt.subtaskIDs}
			got := a.isSubtaskID(tt.query)
			if got != tt.want {
				t.Errorf("isSubtaskID(%v) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

func TestSetTaskFilter(t *testing.T) {
	taskID := uuid.New()
	sub1 := uuid.New()
	sub2 := uuid.New()

	t.Run("set filter updates fields and clamps cursor", func(t *testing.T) {
		a := AgentsModel{
			agents: []model.Agent{
				{ID: uuid.New(), Name: "agent-1", Status: model.AgentWorking, CurrentTaskID: ptr(taskID)},
			},
			cursor:     5, // intentionally out of bounds
			autoFilter: true,
		}
		a.setTaskFilter(&taskID, []uuid.UUID{sub1, sub2})

		if a.filterTaskID == nil || *a.filterTaskID != taskID {
			t.Errorf("expected filterTaskID %v, got %v", taskID, a.filterTaskID)
		}
		if len(a.subtaskIDs) != 2 {
			t.Errorf("expected 2 subtaskIDs, got %d", len(a.subtaskIDs))
		}
		// Cursor should be clamped to valid range (1 visible agent = max cursor 0).
		if a.cursor != 0 {
			t.Errorf("expected cursor clamped to 0, got %d", a.cursor)
		}
	})

	t.Run("clear filter with nil", func(t *testing.T) {
		a := AgentsModel{
			filterTaskID: &taskID,
			subtaskIDs:   []uuid.UUID{sub1},
			autoFilter:   true,
		}
		a.setTaskFilter(nil, nil)

		if a.filterTaskID != nil {
			t.Errorf("expected nil filterTaskID, got %v", a.filterTaskID)
		}
		if len(a.subtaskIDs) != 0 {
			t.Errorf("expected 0 subtaskIDs, got %d", len(a.subtaskIDs))
		}
	})
}

func TestClampAgentCursor(t *testing.T) {
	makeAgents := func(n int) []model.Agent {
		t.Helper()
		agents := make([]model.Agent, n)
		for i := range agents {
			agents[i] = model.Agent{
				ID:     uuid.New(),
				Name:   "agent",
				Status: model.AgentWorking,
			}
		}
		return agents
	}

	tests := []struct {
		name       string
		cursor     int
		agentCount int
		wantCursor int
	}{
		{
			name:       "cursor at 0 with 5 agents stays 0",
			cursor:     0,
			agentCount: 5,
			wantCursor: 0,
		},
		{
			name:       "cursor at 4 with 5 agents stays 4",
			cursor:     4,
			agentCount: 5,
			wantCursor: 4,
		},
		{
			name:       "cursor at 5 with 5 agents clamped to 4",
			cursor:     5,
			agentCount: 5,
			wantCursor: 4,
		},
		{
			name:       "cursor at 3 agents reduced to 2 clamped to 1",
			cursor:     3,
			agentCount: 2,
			wantCursor: 1,
		},
		{
			name:       "0 agents cursor clamped to 0",
			cursor:     5,
			agentCount: 0,
			wantCursor: 0,
		},
		{
			name:       "negative cursor clamped to 0",
			cursor:     -1,
			agentCount: 3,
			wantCursor: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := AgentsModel{
				agents: makeAgents(tt.agentCount),
				cursor: tt.cursor,
			}
			a.clampAgentCursor()
			if a.cursor != tt.wantCursor {
				t.Errorf("expected cursor %d, got %d", tt.wantCursor, a.cursor)
			}
		})
	}
}

func TestAgentsModel_Selected(t *testing.T) {
	taskA := uuid.New()
	agents := []model.Agent{
		{ID: uuid.New(), Name: "first", Status: model.AgentWorking, CurrentTaskID: ptr(taskA)},
		{ID: uuid.New(), Name: "second", Status: model.AgentWorking, CurrentTaskID: ptr(taskA)},
		{ID: uuid.New(), Name: "third", Status: model.AgentWorking, CurrentTaskID: ptr(taskA)},
	}

	tests := []struct {
		name     string
		agents   []model.Agent
		cursor   int
		wantName string
		wantNil  bool
	}{
		{
			name:     "cursor 0 returns first agent",
			agents:   agents,
			cursor:   0,
			wantName: "first",
		},
		{
			name:     "cursor 1 returns second agent",
			agents:   agents,
			cursor:   1,
			wantName: "second",
		},
		{
			name:     "cursor at last returns last agent",
			agents:   agents,
			cursor:   2,
			wantName: "third",
		},
		{
			name:    "empty list returns nil",
			agents:  nil,
			cursor:  0,
			wantNil: true,
		},
		{
			name:     "cursor beyond end clamps to last",
			agents:   agents,
			cursor:   99,
			wantName: "third",
		},
		{
			name:    "negative cursor returns nil",
			agents:  agents,
			cursor:  -1,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := AgentsModel{
				agents: tt.agents,
				cursor: tt.cursor,
			}
			selected := a.Selected()
			if tt.wantNil {
				if selected != nil {
					t.Fatalf("expected nil, got agent %q", selected.Name)
				}
				return
			}
			if selected == nil {
				t.Fatal("expected an agent, got nil")
			}
			if selected.Name != tt.wantName {
				t.Errorf("expected agent %q, got %q", tt.wantName, selected.Name)
			}
		})
	}
}
