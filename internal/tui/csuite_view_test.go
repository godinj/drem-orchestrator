package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/csuite"
)

// ---------------------------------------------------------------------------
// CsuiteModel View rendering tests
// ---------------------------------------------------------------------------

func TestCsuiteView_NoSnapshot(t *testing.T) {
	c := NewCsuiteModel()
	c.width = 100
	c.height = 40

	view := c.View()
	if !strings.Contains(view, "C-Suite Dashboard") {
		t.Error("view should contain title")
	}
	if !strings.Contains(view, "Loading") {
		t.Error("view should show loading message when no snapshot")
	}
}

func TestCsuiteView_WithAgents(t *testing.T) {
	now := time.Now()
	fiveMinAgo := now.Add(-5 * time.Minute)

	snapshot := csuite.StateSnapshot{
		AgentSummaries: []csuite.AgentSummary{
			{
				CsuiteAgent: csuite.CsuiteAgent{
					ID:              uuid.New(),
					Name:            "kyle",
					Status:          csuite.AgentMonOnline,
					HeartbeatAt:     &now,
					ContextPercent:  42,
					CurrentActivity: "reviewing pipeline",
				},
				UnreadCount: 3,
			},
			{
				CsuiteAgent: csuite.CsuiteAgent{
					ID:              uuid.New(),
					Name:            "mike",
					Status:          csuite.AgentMonStale,
					HeartbeatAt:     &fiveMinAgo,
					ContextPercent:  85,
					CurrentActivity: "waiting for gate",
				},
				UnreadCount: 0,
			},
			{
				CsuiteAgent: csuite.CsuiteAgent{
					ID:              uuid.New(),
					Name:            "rachel",
					Status:          csuite.AgentMonOffline,
					HeartbeatAt:     nil,
					ContextPercent:  0,
					CurrentActivity: "",
				},
				UnreadCount: 1,
			},
		},
		InboxCounts: map[string]int{
			"kyle":   3,
			"rachel": 1,
		},
		Timestamp: now,
	}

	c := CsuiteModel{
		snapshot: &snapshot,
		width:    100,
		height:   40,
	}

	view := c.View()

	// Title should be present.
	if !strings.Contains(view, "C-Suite Dashboard") {
		t.Error("view should contain C-Suite Dashboard title")
	}

	// Agent names should appear.
	if !strings.Contains(view, "kyle") {
		t.Error("view should contain agent name 'kyle'")
	}
	if !strings.Contains(view, "mike") {
		t.Error("view should contain agent name 'mike'")
	}
	if !strings.Contains(view, "rachel") {
		t.Error("view should contain agent name 'rachel'")
	}

	// Status strings should appear.
	if !strings.Contains(view, "online") {
		t.Error("view should contain 'online' status")
	}
	if !strings.Contains(view, "stale") {
		t.Error("view should contain 'stale' status")
	}
	if !strings.Contains(view, "offline") {
		t.Error("view should contain 'offline' status")
	}

	// Activities should appear.
	if !strings.Contains(view, "reviewing pipeline") {
		t.Error("view should contain agent activity")
	}

	// Pipeline summary should be present.
	if !strings.Contains(view, "Pipeline Summary") {
		t.Error("view should contain Pipeline Summary")
	}
}

func TestCsuiteView_EmptyAgentList(t *testing.T) {
	snapshot := csuite.StateSnapshot{
		AgentSummaries: nil,
		InboxCounts:    map[string]int{},
		Timestamp:      time.Now(),
	}
	c := CsuiteModel{
		snapshot: &snapshot,
		width:    100,
		height:   40,
	}

	view := c.View()
	if !strings.Contains(view, "No C-Suite agents") {
		t.Error("view should show empty state message")
	}
}

// ---------------------------------------------------------------------------
// Keybinding tests
// ---------------------------------------------------------------------------

func TestBoardKey_W_SwitchesToCsuite(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	result, _ := m.Update(keyMsg("w"))
	got := result.(Model)
	if got.focus != FocusCsuite {
		t.Errorf("focus = %d, want FocusCsuite (%d)", got.focus, FocusCsuite)
	}
}

func TestCsuiteKey_Esc_ReturnsToBoard(t *testing.T) {
	m := newKeyTestModel(t, FocusCsuite)
	result, cmd := m.Update(keyMsg("esc"))
	got := result.(Model)
	if got.focus != FocusBoard {
		t.Errorf("focus = %d, want FocusBoard (%d)", got.focus, FocusBoard)
	}
	if cmd != nil {
		t.Error("esc from csuite should produce nil cmd")
	}
}

func TestCsuiteKey_W_ReturnsToBoard(t *testing.T) {
	m := newKeyTestModel(t, FocusCsuite)
	result, cmd := m.Update(keyMsg("w"))
	got := result.(Model)
	if got.focus != FocusBoard {
		t.Errorf("focus = %d, want FocusBoard (%d)", got.focus, FocusBoard)
	}
	if cmd != nil {
		t.Error("w from csuite should produce nil cmd")
	}
}

func TestCsuiteKey_CursorMovement(t *testing.T) {
	now := time.Now()
	snapshot := csuite.StateSnapshot{
		AgentSummaries: []csuite.AgentSummary{
			{CsuiteAgent: csuite.CsuiteAgent{ID: uuid.New(), Name: "agent-1", Status: csuite.AgentMonOnline, HeartbeatAt: &now}},
			{CsuiteAgent: csuite.CsuiteAgent{ID: uuid.New(), Name: "agent-2", Status: csuite.AgentMonOnline, HeartbeatAt: &now}},
			{CsuiteAgent: csuite.CsuiteAgent{ID: uuid.New(), Name: "agent-3", Status: csuite.AgentMonOnline, HeartbeatAt: &now}},
		},
		InboxCounts: map[string]int{},
		Timestamp:   now,
	}

	m := newKeyTestModel(t, FocusCsuite)
	m.csuite.snapshot = &snapshot
	m.csuite.cursor = 0

	// Move down
	result, _ := m.Update(keyMsg("j"))
	got := result.(Model)
	if got.csuite.cursor != 1 {
		t.Errorf("cursor = %d, want 1 after j", got.csuite.cursor)
	}

	// Move down again
	result, _ = got.Update(keyMsg("j"))
	got = result.(Model)
	if got.csuite.cursor != 2 {
		t.Errorf("cursor = %d, want 2 after second j", got.csuite.cursor)
	}

	// Move down at boundary (should stay)
	result, _ = got.Update(keyMsg("j"))
	got = result.(Model)
	if got.csuite.cursor != 2 {
		t.Errorf("cursor = %d, want 2 at bottom boundary", got.csuite.cursor)
	}

	// Move up
	result, _ = got.Update(keyMsg("k"))
	got = result.(Model)
	if got.csuite.cursor != 1 {
		t.Errorf("cursor = %d, want 1 after k", got.csuite.cursor)
	}

	// Move up to top
	result, _ = got.Update(keyMsg("k"))
	got = result.(Model)
	if got.csuite.cursor != 0 {
		t.Errorf("cursor = %d, want 0 after second k", got.csuite.cursor)
	}

	// Move up at boundary
	result, _ = got.Update(keyMsg("k"))
	got = result.(Model)
	if got.csuite.cursor != 0 {
		t.Errorf("cursor = %d, want 0 at top boundary", got.csuite.cursor)
	}
}

func TestCsuiteKey_CursorMovement_NoSnapshot(t *testing.T) {
	m := newKeyTestModel(t, FocusCsuite)
	// No snapshot set

	// Move down should be noop
	result, _ := m.Update(keyMsg("j"))
	got := result.(Model)
	if got.csuite.cursor != 0 {
		t.Errorf("cursor = %d, want 0 with nil snapshot", got.csuite.cursor)
	}
}

// ---------------------------------------------------------------------------
// Navigation cycle tests
// ---------------------------------------------------------------------------

func TestNavigationCycle_BoardCsuiteBoard(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)

	// Step 1: w to csuite
	result, _ := m.Update(keyMsg("w"))
	got := result.(Model)
	if got.focus != FocusCsuite {
		t.Fatal("step 1: w should go to csuite")
	}

	// Step 2: esc back to board
	result, _ = got.Update(keyMsg("esc"))
	got = result.(Model)
	if got.focus != FocusBoard {
		t.Fatal("step 2: esc should return to board")
	}
}

func TestNavigationCycle_BoardCsuite_WToggle(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)

	// Step 1: w to csuite
	result, _ := m.Update(keyMsg("w"))
	got := result.(Model)
	if got.focus != FocusCsuite {
		t.Fatal("step 1: w should go to csuite")
	}

	// Step 2: w again back to board (toggle)
	result, _ = got.Update(keyMsg("w"))
	got = result.(Model)
	if got.focus != FocusBoard {
		t.Fatal("step 2: w from csuite should return to board")
	}
}

// ---------------------------------------------------------------------------
// Help / context actions tests
// ---------------------------------------------------------------------------

func TestContextActions_CsuiteFocus(t *testing.T) {
	m := buildTestModel(FocusCsuite, nil, nil, false)
	actions := m.contextActions()

	// Should have csuite-specific bindings.
	requireBindings(t, actions, "j/k", "w/esc", "?")

	// Should NOT have task-specific bindings.
	forbidBindings(t, actions, "a", "r", "t", "f", "p", "R", "n", "b")
}

func TestContextActions_BoardHasCsuiteBinding(t *testing.T) {
	m := buildTestModel(FocusBoard, nil, nil, false)
	actions := m.contextActions()

	// Board should include the 'w' binding for csuite dashboard.
	requireBindings(t, actions, "w")

	desc := bindingDesc(actions, "w")
	if !containsSubstring(desc, "C-Suite") && !containsSubstring(desc, "csuite") &&
		!containsSubstring(desc, "dashboard") {
		t.Errorf("w description %q should reference C-Suite or dashboard", desc)
	}
}

func TestHelpOverlay_CsuiteTitle(t *testing.T) {
	m := buildTestModel(FocusCsuite, nil, nil, false)
	m.showHelp = true
	view := m.renderHelpOverlay()
	if !strings.Contains(view, "C-Suite") {
		t.Error("help overlay should contain 'C-Suite' in title when focused")
	}
}

// ---------------------------------------------------------------------------
// csuiteSnapshotMsg handler test
// ---------------------------------------------------------------------------

func TestUpdate_CsuiteSnapshotMsg(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	now := time.Now()
	snap := csuite.StateSnapshot{
		AgentSummaries: []csuite.AgentSummary{
			{CsuiteAgent: csuite.CsuiteAgent{ID: uuid.New(), Name: "kyle", Status: csuite.AgentMonOnline, HeartbeatAt: &now}},
		},
		InboxCounts: map[string]int{"kyle": 2},
		Timestamp:   now,
	}

	result, cmd := m.Update(csuiteSnapshotMsg{snapshot: snap})
	got := result.(Model)

	if got.csuite.snapshot == nil {
		t.Fatal("snapshot should be set after csuiteSnapshotMsg")
	}
	if got.csuite.snapshot.AgentSummaries[0].Name != "kyle" {
		t.Error("snapshot should contain agent kyle")
	}
	if cmd != nil {
		t.Error("csuiteSnapshotMsg should produce nil cmd")
	}
}

// ---------------------------------------------------------------------------
// agentMonStatusBadge tests
// ---------------------------------------------------------------------------

func TestAgentMonStatusBadge(t *testing.T) {
	tests := []struct {
		status csuite.AgentMonStatus
		want   string
	}{
		{csuite.AgentMonOnline, "online"},
		{csuite.AgentMonStale, "stale"},
		{csuite.AgentMonOffline, "offline"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			badge := agentMonStatusBadge(tt.status)
			if !strings.Contains(badge, tt.want) {
				t.Errorf("badge %q should contain %q", badge, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// heartbeatAge tests
// ---------------------------------------------------------------------------

func TestHeartbeatAge(t *testing.T) {
	tests := []struct {
		name    string
		time    *time.Time
		wantSub string
	}{
		{"nil", nil, "never"},
		{"just now", timePtr(time.Now().Add(-10 * time.Second)), "s ago"},
		{"minutes ago", timePtr(time.Now().Add(-5 * time.Minute)), "m ago"},
		{"hours ago", timePtr(time.Now().Add(-3 * time.Hour)), "h ago"},
		{"days ago", timePtr(time.Now().Add(-48 * time.Hour)), "d ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := heartbeatAge(tt.time)
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("heartbeatAge = %q, want substring %q", got, tt.wantSub)
			}
		})
	}
}

func timePtr(t time.Time) *time.Time { return &t }

// ---------------------------------------------------------------------------
// Scroll adjustment tests
// ---------------------------------------------------------------------------

func TestCsuiteAdjustScroll_NilSnapshot(t *testing.T) {
	c := NewCsuiteModel()
	c.scrollOffset = 5
	c.adjustScroll()
	if c.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0 with nil snapshot", c.scrollOffset)
	}
}

func TestCsuiteAdjustScroll_ClampsToRange(t *testing.T) {
	now := time.Now()
	agents := make([]csuite.AgentSummary, 50)
	for i := range agents {
		agents[i] = csuite.AgentSummary{
			CsuiteAgent: csuite.CsuiteAgent{
				ID:          uuid.New(),
				Name:        "agent",
				Status:      csuite.AgentMonOnline,
				HeartbeatAt: &now,
			},
		}
	}
	snapshot := csuite.StateSnapshot{
		AgentSummaries: agents,
		InboxCounts:    map[string]int{},
		Timestamp:      now,
	}

	c := CsuiteModel{
		snapshot: &snapshot,
		width:    100,
		height:   20,
		cursor:   49,
	}
	c.adjustScroll()

	// Cursor should be visible within the scroll window.
	listHeight := c.listHeight() - 2
	if c.cursor < c.scrollOffset || c.cursor >= c.scrollOffset+listHeight {
		t.Errorf("cursor %d should be within scroll window [%d, %d)",
			c.cursor, c.scrollOffset, c.scrollOffset+listHeight)
	}
}

// ---------------------------------------------------------------------------
// Pipeline summary rendering tests
// ---------------------------------------------------------------------------

func TestCsuiteView_PipelineSummary_Counts(t *testing.T) {
	now := time.Now()
	snapshot := csuite.StateSnapshot{
		AgentSummaries: []csuite.AgentSummary{
			{CsuiteAgent: csuite.CsuiteAgent{ID: uuid.New(), Name: "a1", Status: csuite.AgentMonOnline, HeartbeatAt: &now}},
			{CsuiteAgent: csuite.CsuiteAgent{ID: uuid.New(), Name: "a2", Status: csuite.AgentMonStale, HeartbeatAt: &now}},
			{CsuiteAgent: csuite.CsuiteAgent{ID: uuid.New(), Name: "a3", Status: csuite.AgentMonOffline}},
		},
		InboxCounts: map[string]int{"a1": 5, "a2": 2},
		Timestamp:   now,
	}

	c := CsuiteModel{
		snapshot: &snapshot,
		width:    100,
		height:   40,
	}

	view := c.View()

	// Should show agent count.
	if !strings.Contains(view, "Agents: 3") {
		t.Error("pipeline summary should show agent count")
	}
	// Should show online count.
	if !strings.Contains(view, "Online: 1") {
		t.Error("pipeline summary should show online count")
	}
	// Should show stale count.
	if !strings.Contains(view, "Stale: 1") {
		t.Error("pipeline summary should show stale count")
	}
	// Should show offline count.
	if !strings.Contains(view, "Offline: 1") {
		t.Error("pipeline summary should show offline count")
	}
	// Should show total unread count (5 + 2 = 7).
	if !strings.Contains(view, "Unread: 7") {
		t.Error("pipeline summary should show total unread count")
	}
}
