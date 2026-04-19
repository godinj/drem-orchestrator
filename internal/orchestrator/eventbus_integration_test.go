package orchestrator

import (
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/eventbus"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// testOrchestratorWithBus creates an Orchestrator with event bus wired up.
func testOrchestratorWithBus(t *testing.T, bus *eventbus.Bus) (*Orchestrator, uuid.UUID) {
	t.Helper()
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	events := make(chan Event, 100)
	wt := &FakeWorktreeManager{BarePath: t.TempDir(), Default: "main"}
	o := &Orchestrator{
		db:              db,
		projectID:       projectID,
		worktree:        wt,
		events:          events,
		bus:             bus,
		contextWarnPct:  75,
		contextStopPct:  90,
		contextFixerPct: 85,
		logger:          slog.Default(),
	}
	return o, projectID
}

// ---------------------------------------------------------------------------
// SetEventBus
// ---------------------------------------------------------------------------

func TestSetEventBus_NilIsNoOp(t *testing.T) {
	db := testutil.NewTestDB(t)
	events := make(chan Event, 100)
	wt := &FakeWorktreeManager{BarePath: t.TempDir(), Default: "main"}
	o := &Orchestrator{
		db:        db,
		projectID: uuid.New(),
		worktree:  wt,
		events:    events,
		logger:    slog.Default(),
	}

	// Should not panic with nil bus.
	o.publishTaskTransition("some-id", "backlog", "planning", "test")
	o.publishAgentStatus("some-id", "agent-id", "coder", "working")
}

func TestSetEventBus_SetsField(t *testing.T) {
	bus := testutil.NewTestBus(t)
	db := testutil.NewTestDB(t)
	events := make(chan Event, 100)
	wt := &FakeWorktreeManager{BarePath: t.TempDir(), Default: "main"}
	o := &Orchestrator{
		db:        db,
		projectID: uuid.New(),
		worktree:  wt,
		events:    events,
		logger:    slog.Default(),
	}

	o.SetEventBus(bus)
	if o.bus != bus {
		t.Error("expected bus to be set")
	}
}

// ---------------------------------------------------------------------------
// publishTaskTransition
// ---------------------------------------------------------------------------

func TestPublishTaskTransition_CreatesEventAndDeliveries(t *testing.T) {
	bus := testutil.NewTestBus(t)
	o, _ := testOrchestratorWithBus(t, bus)

	taskID := uuid.New().String()
	o.publishTaskTransition(taskID, "backlog", "planning", "test transition")

	// Verify event was published.
	events, err := bus.Events()
	if err != nil {
		t.Fatalf("bus.Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != "task_status_changed" {
		t.Errorf("expected type task_status_changed, got %q", ev.Type)
	}
	if ev.Source != "orchestrator" {
		t.Errorf("expected source orchestrator, got %q", ev.Source)
	}
	if ev.TaskID != taskID {
		t.Errorf("expected task_id %q, got %q", taskID, ev.TaskID)
	}
	if ev.FromStatus != "backlog" {
		t.Errorf("expected from_status backlog, got %q", ev.FromStatus)
	}
	if ev.ToStatus != "planning" {
		t.Errorf("expected to_status planning, got %q", ev.ToStatus)
	}
	if ev.Details != "test transition" {
		t.Errorf("expected details %q, got %q", "test transition", ev.Details)
	}

	// Verify deliveries for all 5 agents.
	for _, agent := range csuiteAgents {
		unacked, err := bus.UnackedDeliveries(agent)
		if err != nil {
			t.Fatalf("bus.UnackedDeliveries(%q): %v", agent, err)
		}
		if len(unacked) != 1 {
			t.Errorf("expected 1 unacked delivery for %q, got %d", agent, len(unacked))
		}
	}
}

// ---------------------------------------------------------------------------
// publishAgentStatus
// ---------------------------------------------------------------------------

func TestPublishAgentStatus_CreatesEventAndDeliveries(t *testing.T) {
	bus := testutil.NewTestBus(t)
	o, _ := testOrchestratorWithBus(t, bus)

	taskID := uuid.New().String()
	agentID := uuid.New().String()
	o.publishAgentStatus(taskID, agentID, "planner", "working")

	events, err := bus.Events()
	if err != nil {
		t.Fatalf("bus.Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != "agent_status_changed" {
		t.Errorf("expected type agent_status_changed, got %q", ev.Type)
	}
	if ev.ToStatus != "working" {
		t.Errorf("expected to_status working, got %q", ev.ToStatus)
	}

	// Verify deliveries exist.
	for _, agent := range csuiteAgents {
		unacked, err := bus.UnackedDeliveries(agent)
		if err != nil {
			t.Fatalf("bus.UnackedDeliveries(%q): %v", agent, err)
		}
		if len(unacked) != 1 {
			t.Errorf("expected 1 unacked delivery for %q, got %d", agent, len(unacked))
		}
	}
}

// ---------------------------------------------------------------------------
// failTask emits to event bus
// ---------------------------------------------------------------------------

func TestFailTask_EmitsEventBusEvent(t *testing.T) {
	bus := testutil.NewTestBus(t)
	o, projectID := testOrchestratorWithBus(t, bus)

	// Create a task in IN_PROGRESS status.
	task := &model.Task{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "test task",
		Status:    model.StatusInProgress,
	}
	if err := o.db.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	if err := o.failTask(task, "test failure reason"); err != nil {
		t.Fatalf("failTask: %v", err)
	}

	events, err := bus.Events()
	if err != nil {
		t.Fatalf("bus.Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != "task_status_changed" {
		t.Errorf("expected type task_status_changed, got %q", ev.Type)
	}
	if ev.FromStatus != string(model.StatusInProgress) {
		t.Errorf("expected from_status in_progress, got %q", ev.FromStatus)
	}
	if ev.ToStatus != string(model.StatusFailed) {
		t.Errorf("expected to_status failed, got %q", ev.ToStatus)
	}
}

// ---------------------------------------------------------------------------
// PauseTask emits to event bus
// ---------------------------------------------------------------------------

func TestPauseTask_EmitsEventBusEvent(t *testing.T) {
	bus := testutil.NewTestBus(t)
	o, projectID := testOrchestratorWithBus(t, bus)

	task := &model.Task{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "test pause",
		Status:    model.StatusInProgress,
	}
	if err := o.db.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	if err := o.PauseTask(task.ID); err != nil {
		t.Fatalf("PauseTask: %v", err)
	}

	events, err := bus.Events()
	if err != nil {
		t.Fatalf("bus.Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].ToStatus != string(model.StatusPaused) {
		t.Errorf("expected to_status paused, got %q", events[0].ToStatus)
	}
}

// ---------------------------------------------------------------------------
// processBacklog emits to event bus
// ---------------------------------------------------------------------------

func TestProcessBacklog_EmitsEventBusEvent(t *testing.T) {
	bus := testutil.NewTestBus(t)
	o, projectID := testOrchestratorWithBus(t, bus)

	task := &model.Task{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "test backlog",
		Status:    model.StatusBacklog,
		Category:  model.CategoryStandard,
	}
	if err := o.db.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	if err := o.processBacklog(task); err != nil {
		t.Fatalf("processBacklog: %v", err)
	}

	events, err := bus.Events()
	if err != nil {
		t.Fatalf("bus.Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].FromStatus != string(model.StatusBacklog) {
		t.Errorf("expected from_status backlog, got %q", events[0].FromStatus)
	}
	if events[0].ToStatus != string(model.StatusPlanning) {
		t.Errorf("expected to_status planning, got %q", events[0].ToStatus)
	}
}

// ---------------------------------------------------------------------------
// applyClassification emits to event bus
// ---------------------------------------------------------------------------

func TestApplyClassification_EmitsEventBusEvent(t *testing.T) {
	bus := testutil.NewTestBus(t)
	o, projectID := testOrchestratorWithBus(t, bus)

	task := &model.Task{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "test classify",
		Status:    model.StatusClassifying,
		Category:  model.CategoryStandard,
	}
	if err := o.db.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	output := &ClassifierOutput{
		Category:        "standard",
		ComplexityScore: 5,
		Title:           "Updated title",
		Description:     "Updated description",
	}
	if err := o.applyClassification(task, output); err != nil {
		t.Fatalf("applyClassification: %v", err)
	}

	events, err := bus.Events()
	if err != nil {
		t.Fatalf("bus.Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].FromStatus != string(model.StatusClassifying) {
		t.Errorf("expected from_status classifying, got %q", events[0].FromStatus)
	}
	if events[0].ToStatus != string(model.StatusBacklog) {
		t.Errorf("expected to_status backlog, got %q", events[0].ToStatus)
	}
}

// ---------------------------------------------------------------------------
// NilBus does not break existing flow
// ---------------------------------------------------------------------------

func TestNilBus_FailTask_Succeeds(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	events := make(chan Event, 100)
	wt := &FakeWorktreeManager{BarePath: t.TempDir(), Default: "main"}
	o := &Orchestrator{
		db:              db,
		projectID:       projectID,
		worktree:        wt,
		events:          events,
		contextWarnPct:  75,
		contextStopPct:  90,
		contextFixerPct: 85,
		logger:          slog.Default(),
		// bus is intentionally nil
	}

	task := &model.Task{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "nil bus test",
		Status:    model.StatusInProgress,
	}
	if err := o.db.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	if err := o.failTask(task, "test"); err != nil {
		t.Fatalf("failTask should succeed with nil bus: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Multiple transitions produce correct event count
// ---------------------------------------------------------------------------

func TestMultipleTransitions_ProduceMultipleEvents(t *testing.T) {
	bus := testutil.NewTestBus(t)
	o, _ := testOrchestratorWithBus(t, bus)

	taskID := uuid.New().String()

	o.publishTaskTransition(taskID, "backlog", "planning", "")
	o.publishTaskTransition(taskID, "planning", "plan_review", "")
	o.publishAgentStatus(taskID, uuid.New().String(), "planner", "working")

	events, err := bus.Events()
	if err != nil {
		t.Fatalf("bus.Events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	// Each agent should have 3 unacked deliveries.
	for _, agent := range csuiteAgents {
		unacked, err := bus.UnackedDeliveries(agent)
		if err != nil {
			t.Fatalf("bus.UnackedDeliveries(%q): %v", agent, err)
		}
		if len(unacked) != 3 {
			t.Errorf("expected 3 unacked deliveries for %q, got %d", agent, len(unacked))
		}
	}
}

// ---------------------------------------------------------------------------
// csuiteAgents constant correctness
// ---------------------------------------------------------------------------

func TestCsuiteAgents_ContainsExpectedAgents(t *testing.T) {
	expected := map[string]bool{
		"kyle": true, "mike": true, "alex": true, "ross": true, "seth": true,
	}
	if len(csuiteAgents) != 5 {
		t.Fatalf("expected 5 agents, got %d", len(csuiteAgents))
	}
	for _, agent := range csuiteAgents {
		if !expected[agent] {
			t.Errorf("unexpected agent %q", agent)
		}
	}
}
