package tui

import (
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// hasBinding reports whether bindings contains an entry with the given key.
func hasBinding(bindings []helpBinding, key string) bool {
	for _, b := range bindings {
		if b.Key == key {
			return true
		}
	}
	return false
}

// bindingDesc returns the description for the first binding matching key,
// or empty string if not found.
func bindingDesc(bindings []helpBinding, key string) string {
	for _, b := range bindings {
		if b.Key == key {
			return b.Desc
		}
	}
	return ""
}

// requireBindings fails if any of the required keys are missing.
func requireBindings(t *testing.T, bindings []helpBinding, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if !hasBinding(bindings, k) {
			t.Errorf("expected binding with key %q to be present", k)
		}
	}
}

// forbidBindings fails if any of the forbidden keys are present.
func forbidBindings(t *testing.T, bindings []helpBinding, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if hasBinding(bindings, k) {
			t.Errorf("expected binding with key %q to be absent", k)
		}
	}
}

// buildTestModel creates a minimal Model for help testing with the given focus,
// task (placed on board + detail), agent, and deleteMode.
func buildTestModel(focus Focus, task *model.Task, agent *model.Agent, deleteMode bool) Model {
	m := Model{
		keys:     defaultKeyMap(),
		focus:    focus,
		create:   NewCreateModel(),
		feedback: NewFeedbackModel(""),
		width:    120,
		height:   40,
		detail: DetailModel{
			agent:      agent,
			deleteMode: deleteMode,
		},
	}
	if task != nil {
		m.board.tasks = []model.Task{*task}
		m.board.cursor = 0
		m.detail.task = task
	}
	m.updatePanelSizes()
	return m
}

func TestContextActions_BoardFocusTaskStatuses(t *testing.T) {
	agentID := uuid.New()
	agent := &model.Agent{
		ID:          agentID,
		TmuxSession: "sess-1",
	}

	tests := []struct {
		name       string
		task       *model.Task
		agent      *model.Agent
		wantKeys   []string
		forbidKeys []string
		wantDescs  map[string]string
	}{
		{
			name: "plan_review status",
			task: &model.Task{
				ID:              uuid.New(),
				Status:          model.StatusPlanReview,
				AssignedAgentID: &agentID,
			},
			agent:    agent,
			wantKeys: []string{"a", "r", "v", "c", "d", "S", "l", "g"},
			wantDescs: map[string]string{
				"a": "approve",
				"r": "reject",
				"v": "review",
				"c": "comment",
				"d": "delete",
			},
		},
		{
			name: "testing_ready status",
			task: &model.Task{
				ID:              uuid.New(),
				Status:          model.StatusTestingReady,
				AssignedAgentID: &agentID,
			},
			agent:    agent,
			wantKeys: []string{"t", "f", "v", "x", "c", "d", "S", "l", "g"},
			wantDescs: map[string]string{
				"t": "pass",
				"f": "fail",
				"v": "review",
				"x": "fix",
			},
		},
		{
			name: "in_progress status",
			task: &model.Task{
				ID:              uuid.New(),
				Status:          model.StatusInProgress,
				AssignedAgentID: &agentID,
			},
			agent:      agent,
			wantKeys:   []string{"p", "x", "d", "S", "l", "g"},
			forbidKeys: []string{"a", "r", "t", "f", "R"},
			wantDescs: map[string]string{
				"p": "pause",
			},
		},
		{
			name: "paused status",
			task: &model.Task{
				ID:     uuid.New(),
				Status: model.StatusPaused,
			},
			agent:      nil,
			wantKeys:   []string{"p", "S"},
			forbidKeys: []string{"a", "r", "t", "f", "d", "R", "x", "l", "g"},
			wantDescs: map[string]string{
				"p": "resume",
			},
		},
		{
			name: "failed status",
			task: &model.Task{
				ID:     uuid.New(),
				Status: model.StatusFailed,
			},
			agent:      nil,
			wantKeys:   []string{"R", "x", "S"},
			forbidKeys: []string{"a", "r", "t", "f", "p", "d", "l", "g"},
			wantDescs: map[string]string{
				"R": "retry",
				"x": "fix",
			},
		},
		{
			name: "backlog status - no task-specific actions besides supervisor",
			task: &model.Task{
				ID:     uuid.New(),
				Status: model.StatusBacklog,
			},
			agent:      nil,
			wantKeys:   []string{"S"},
			forbidKeys: []string{"a", "r", "t", "f", "p", "R", "x", "d", "l", "g"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := buildTestModel(FocusBoard, tt.task, tt.agent, false)
			actions := m.contextActions()

			requireBindings(t, actions, tt.wantKeys...)
			forbidBindings(t, actions, tt.forbidKeys...)

			for key, wantSub := range tt.wantDescs {
				desc := bindingDesc(actions, key)
				if desc == "" {
					t.Errorf("key %q: expected description containing %q, got empty", key, wantSub)
					continue
				}
				if !containsSubstring(desc, wantSub) {
					t.Errorf("key %q: description %q does not contain %q", key, desc, wantSub)
				}
			}
		})
	}
}

func TestContextActions_BoardFocusNoTask(t *testing.T) {
	m := buildTestModel(FocusBoard, nil, nil, false)
	actions := m.contextActions()

	// Global/navigation actions should still be present.
	requireBindings(t, actions, "j/k", "tab", "q", "?")

	// No task-specific actions when no task is selected.
	forbidBindings(t, actions, "a", "r", "t", "f", "p", "R", "x", "d", "c", "S", "l", "g")
}

func TestContextActions_AgentFocus(t *testing.T) {
	tests := []struct {
		name       string
		agent      *model.Agent
		wantKeys   []string
		forbidKeys []string
	}{
		{
			name: "agent with tmux session",
			agent: &model.Agent{
				ID:          uuid.New(),
				TmuxSession: "sess-agent",
			},
			wantKeys:   []string{"g", "A", "F"},
			forbidKeys: []string{"a", "r", "t", "f", "p", "R", "x", "d", "c", "S", "l"},
		},
		{
			name: "agent without tmux session",
			agent: &model.Agent{
				ID:          uuid.New(),
				TmuxSession: "",
			},
			wantKeys:   []string{"A", "F"},
			forbidKeys: []string{"a", "r", "t", "f", "p", "R", "x", "d", "c", "S", "l"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// For agent focus, agent is on the agents panel, not on detail.
			// The contextActions for FocusAgents always includes g if there's an
			// agent — but the implementation hardcodes it in the agents section.
			// We test via the model.
			m := buildTestModel(FocusAgents, nil, nil, false)
			actions := m.contextActions()

			requireBindings(t, actions, tt.wantKeys...)

			// Global actions are always present.
			requireBindings(t, actions, "j/k", "tab", "q", "?")
		})
	}
}

func TestContextActions_DetailFocus(t *testing.T) {
	agentID := uuid.New()
	agent := &model.Agent{
		ID:          agentID,
		TmuxSession: "sess-detail",
	}

	tests := []struct {
		name     string
		task     *model.Task
		agent    *model.Agent
		wantKeys []string
	}{
		{
			name: "plan_review - same as board plus scroll hint",
			task: &model.Task{
				ID:              uuid.New(),
				Status:          model.StatusPlanReview,
				AssignedAgentID: &agentID,
			},
			agent:    agent,
			wantKeys: []string{"a", "r", "v", "c", "d", "S", "l", "g"},
		},
		{
			name: "testing_ready - same as board plus scroll hint",
			task: &model.Task{
				ID:              uuid.New(),
				Status:          model.StatusTestingReady,
				AssignedAgentID: &agentID,
			},
			agent:    agent,
			wantKeys: []string{"t", "f", "v", "x", "c", "d", "S", "l", "g"},
		},
		{
			name: "in_progress - same as board",
			task: &model.Task{
				ID:              uuid.New(),
				Status:          model.StatusInProgress,
				AssignedAgentID: &agentID,
			},
			agent:    agent,
			wantKeys: []string{"p", "x", "d", "S", "l", "g"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := buildTestModel(FocusDetail, tt.task, tt.agent, false)
			actions := m.contextActions()

			// Task-status-specific actions should match board.
			requireBindings(t, actions, tt.wantKeys...)

			// Global actions present.
			requireBindings(t, actions, "j/k", "tab", "q", "?")

			// Detail focus should have a scroll hint.
			found := false
			for _, a := range actions {
				if containsSubstring(a.Desc, "scroll") {
					found = true
					break
				}
			}
			if !found {
				t.Error("detail focus should include a scroll hint in actions")
			}
		})
	}
}

func TestContextActions_DetailDeleteMode(t *testing.T) {
	task := &model.Task{
		ID:     uuid.New(),
		Status: model.StatusPlanReview,
	}

	m := buildTestModel(FocusDetail, task, nil, true)
	actions := m.contextActions()

	// Delete mode should have navigation, confirm, and cancel actions.
	requireBindings(t, actions, "j/k")

	// Verify confirm/cancel are present (may use enter/y or esc).
	hasConfirm := hasBinding(actions, "enter/y") || hasBinding(actions, "enter")
	if !hasConfirm {
		t.Error("delete mode should have confirm action (enter or enter/y)")
	}

	hasCancel := hasBinding(actions, "esc")
	if !hasCancel {
		t.Error("delete mode should have cancel action (esc)")
	}

	// Normal task actions should NOT be present in delete mode.
	forbidBindings(t, actions, "a", "r", "t", "f", "p", "R", "S", "n")
}

func TestContextActions_GlobalActionsAlwaysPresent(t *testing.T) {
	tests := []struct {
		name  string
		focus Focus
		task  *model.Task
		agent *model.Agent
	}{
		{
			name:  "board with task",
			focus: FocusBoard,
			task:  &model.Task{ID: uuid.New(), Status: model.StatusBacklog},
		},
		{
			name:  "board without task",
			focus: FocusBoard,
			task:  nil,
		},
		{
			name:  "agents",
			focus: FocusAgents,
			agent: &model.Agent{ID: uuid.New()},
		},
		{
			name:  "detail with task",
			focus: FocusDetail,
			task:  &model.Task{ID: uuid.New(), Status: model.StatusInProgress},
		},
	}

	globalKeys := []string{"j/k", "tab", "q", "?"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := buildTestModel(tt.focus, tt.task, tt.agent, false)
			actions := m.contextActions()
			requireBindings(t, actions, globalKeys...)

			// Verify descriptions for global actions.
			navDesc := bindingDesc(actions, "j/k")
			if !containsSubstring(navDesc, "navigate") {
				t.Errorf("j/k description %q should contain 'navigate'", navDesc)
			}

			tabDesc := bindingDesc(actions, "tab")
			if tabDesc == "" {
				t.Error("tab should have a description")
			}

			qDesc := bindingDesc(actions, "q")
			if !containsSubstring(qDesc, "quit") {
				t.Errorf("q description %q should contain 'quit'", qDesc)
			}

			helpDesc := bindingDesc(actions, "?")
			if !containsSubstring(helpDesc, "help") {
				t.Errorf("? description %q should contain 'help'", helpDesc)
			}
		})
	}
}

func TestContextActions_AgentRelatedOnBoardDetail(t *testing.T) {
	agentID := uuid.New()

	tests := []struct {
		name     string
		focus    Focus
		task     *model.Task
		agent    *model.Agent
		wantLog  bool
		wantJump bool
	}{
		{
			name:  "board - agent assigned with tmux",
			focus: FocusBoard,
			task: &model.Task{
				ID:              uuid.New(),
				Status:          model.StatusInProgress,
				AssignedAgentID: &agentID,
			},
			agent:    &model.Agent{ID: agentID, TmuxSession: "sess"},
			wantLog:  true,
			wantJump: true,
		},
		{
			name:  "board - no agent",
			focus: FocusBoard,
			task: &model.Task{
				ID:     uuid.New(),
				Status: model.StatusInProgress,
			},
			agent:    nil,
			wantLog:  false,
			wantJump: false,
		},
		{
			name:  "detail - agent assigned with tmux",
			focus: FocusDetail,
			task: &model.Task{
				ID:              uuid.New(),
				Status:          model.StatusTestingReady,
				AssignedAgentID: &agentID,
			},
			agent:    &model.Agent{ID: agentID, TmuxSession: "sess"},
			wantLog:  true,
			wantJump: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := buildTestModel(tt.focus, tt.task, tt.agent, false)
			actions := m.contextActions()

			if tt.wantLog {
				if !hasBinding(actions, "l") {
					t.Error("expected log action (l) when agent is assigned")
				}
				desc := bindingDesc(actions, "l")
				if !containsSubstring(desc, "log") {
					t.Errorf("log action description %q should contain 'log'", desc)
				}
			} else {
				if hasBinding(actions, "l") {
					t.Error("should not have log action (l) when no agent")
				}
			}

			if tt.wantJump {
				if !hasBinding(actions, "g") {
					t.Error("expected jump action (g) when agent has tmux session")
				}
			} else {
				if hasBinding(actions, "g") {
					t.Error("should not have jump action (g) when agent has no tmux session")
				}
			}
		})
	}
}

func TestContextActions_SupervisorAlwaysOnBoardDetail(t *testing.T) {
	task := &model.Task{
		ID:     uuid.New(),
		Status: model.StatusBacklog,
	}

	tests := []struct {
		name  string
		focus Focus
		want  bool
	}{
		{"board focus", FocusBoard, true},
		{"detail focus", FocusDetail, true},
		{"agent focus", FocusAgents, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var taskArg *model.Task
			if tt.focus != FocusAgents {
				taskArg = task
			}
			m := buildTestModel(tt.focus, taskArg, nil, false)
			actions := m.contextActions()

			if tt.want {
				if !hasBinding(actions, "S") {
					t.Error("expected supervisor action (S) on board/detail focus")
				}
				desc := bindingDesc(actions, "S")
				if !containsSubstring(desc, "supervisor") {
					t.Errorf("S description %q should contain 'supervisor'", desc)
				}
			} else {
				if hasBinding(actions, "S") {
					t.Error("should not have supervisor action (S) on agent focus")
				}
			}
		})
	}
}

func TestHelpOverlay_NoQuitBinding(t *testing.T) {
	focusStates := []struct {
		name  string
		focus Focus
	}{
		{"FocusBoard", FocusBoard},
		{"FocusAgents", FocusAgents},
		{"FocusDetail", FocusDetail},
		{"FocusCreate", FocusCreate},
		{"FocusFeedback", FocusFeedback},
	}

	task := &model.Task{
		ID:     uuid.New(),
		Status: model.StatusBacklog,
	}

	for _, fs := range focusStates {
		t.Run(fs.name, func(t *testing.T) {
			var taskArg *model.Task
			// Provide a task for focus states that use it.
			if fs.focus == FocusBoard || fs.focus == FocusDetail {
				taskArg = task
			}
			m := buildTestModel(fs.focus, taskArg, nil, false)
			actions := m.contextActions()

			for _, b := range actions {
				if b.Key == "q" && containsSubstring(b.Desc, "quit") {
					t.Errorf("binding q with quit description should not exist, got Key=%q Desc=%q", b.Key, b.Desc)
				}
				if b.Key == "ctrl+c" && containsSubstring(b.Desc, "quit") {
					t.Errorf("binding ctrl+c with quit description should not exist, got Key=%q Desc=%q", b.Key, b.Desc)
				}
			}
		})
	}
}

func TestHelpOverlay_ShowsTmuxExitGuidance(t *testing.T) {
	m := buildTestModel(FocusBoard, nil, nil, false)
	actions := m.contextActions()

	found := false
	for _, b := range actions {
		if containsSubstring(b.Key, "tmux") || containsSubstring(b.Desc, "tmux") {
			found = true
			break
		}
	}
	if !found {
		t.Error("contextActions should include tmux exit guidance (a binding mentioning 'tmux' in Key or Desc)")
	}
}

func TestHelpBar_NoQuitReference(t *testing.T) {
	m := buildTestModel(FocusBoard, nil, nil, false)
	bar := m.renderHelpBar()

	if containsSubstring(bar, "q quit") {
		t.Errorf("help bar should not contain 'q quit', got %q", bar)
	}
	if !containsSubstring(bar, "? help") {
		t.Errorf("help bar should contain '? help', got %q", bar)
	}
}

// containsSubstring reports whether s contains substr (case-insensitive).
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) &&
		containsFold(s, substr)
}

// containsFold is a simple case-insensitive substring check.
func containsFold(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if equalFold(s[i:i+len(substr)], substr) {
			return true
		}
	}
	return false
}

// equalFold reports whether s and t are equal under Unicode case-folding
// (ASCII subset only for simplicity).
func equalFold(s, t string) bool {
	if len(s) != len(t) {
		return false
	}
	for i := 0; i < len(s); i++ {
		a, b := s[i], t[i]
		if a >= 'A' && a <= 'Z' {
			a += 'a' - 'A'
		}
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}
