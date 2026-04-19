package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// fakeAttachProbe lets tests drive AttachProbe behaviour deterministically.
type fakeAttachProbe struct {
	result AttachStatus
	err    error
	calls  []string
}

func (f *fakeAttachProbe) Inspect(_ context.Context, containerID string) (AttachStatus, error) {
	f.calls = append(f.calls, containerID)
	return f.result, f.err
}

// newAttachTestAgent seeds the database with a project and an agent whose
// Config carries a container_id/container_image pair (matching what
// Manager.Spawn writes). It returns the DB and the agent row for assertions.
func newAttachTestAgent(t *testing.T, containerID, image string) (*Manager, uuid.UUID) {
	t.Helper()
	db := testutil.NewTestDB(t)
	project := model.Project{ID: uuid.New(), Name: "attach-demo", BareRepoPath: "/tmp/x"}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: project.ID,
		AgentType: model.AgentCoder,
		Name:      "coder-attach",
		Status:    model.AgentWorking,
		Config: model.JSONField{
			"container_id":    containerID,
			"container_image": image,
		},
	}
	if err := db.Create(&ag).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	m := NewManager(db, &fakeSpawner{}, &ImageResolver{Language: "go"}, "")
	return m, agentID
}

func TestManager_Attach_ReturnsHandleWhenContainerAlive(t *testing.T) {
	m, agentID := newAttachTestAgent(t, "ctr-alive", "worker-go:latest")
	probe := &fakeAttachProbe{result: AttachStatus{Alive: true, Status: "running"}}
	m.SetAttachProbe(probe)

	h, err := m.Attach(context.Background(), agentID.String())
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if h.ContainerID != "ctr-alive" {
		t.Errorf("Handle.ContainerID = %q, want ctr-alive", h.ContainerID)
	}
	if h.Image != "worker-go:latest" {
		t.Errorf("Handle.Image = %q, want worker-go:latest", h.Image)
	}
	if got, want := len(probe.calls), 1; got != want {
		t.Errorf("probe call count = %d, want %d", got, want)
	}
}

func TestManager_Attach_ErrorsWhenContainerNotLive(t *testing.T) {
	m, agentID := newAttachTestAgent(t, "ctr-dead", "worker-go:latest")
	probe := &fakeAttachProbe{result: AttachStatus{Alive: false, Status: "exited"}}
	m.SetAttachProbe(probe)

	if _, err := m.Attach(context.Background(), agentID.String()); err == nil {
		t.Fatal("Attach should error for dead container")
	}
}

func TestManager_Attach_EmptyAgentID_ReturnsError(t *testing.T) {
	m := NewManager(nil, &fakeSpawner{}, nil, "")
	if _, err := m.Attach(context.Background(), ""); err == nil {
		t.Fatal("Attach with empty agent id should error")
	}
}

func TestManager_Attach_UnknownAgentID_ReturnsError(t *testing.T) {
	m, _ := newAttachTestAgent(t, "ctr-x", "img:latest")
	if _, err := m.Attach(context.Background(), uuid.New().String()); err == nil {
		t.Fatal("Attach with unknown agent id should error")
	}
}

func TestManager_Attach_NoContainerIDRecorded_ReturnsError(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := model.Project{ID: uuid.New(), Name: "demo", BareRepoPath: "/tmp/x"}
	_ = db.Create(&project)
	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: project.ID,
		AgentType: model.AgentCoder,
		Name:      "coder-no-ctr",
		Status:    model.AgentWorking,
		Config:    model.JSONField{},
	}
	_ = db.Create(&ag)
	m := NewManager(db, &fakeSpawner{}, nil, "")

	if _, err := m.Attach(context.Background(), agentID.String()); err == nil {
		t.Fatal("Attach should error when agent has no container_id recorded")
	}
}

func TestManager_Attach_NoProbe_ReturnsHandleWithoutInspection(t *testing.T) {
	m, agentID := newAttachTestAgent(t, "ctr-123", "img:latest")
	// Explicitly leave probe unset.
	h, err := m.Attach(context.Background(), agentID.String())
	if err != nil {
		t.Fatalf("Attach without probe: %v", err)
	}
	if h.ContainerID != "ctr-123" {
		t.Errorf("Handle.ContainerID = %q, want ctr-123", h.ContainerID)
	}
}

func TestManager_RespawnIfDashboardExited_LiveHandle_Unchanged(t *testing.T) {
	m, agentID := newAttachTestAgent(t, "ctr-live", "img:latest")
	probe := &fakeAttachProbe{result: AttachStatus{Alive: true, Status: "running"}}
	m.SetAttachProbe(probe)

	h := &Handle{
		ContainerID: "ctr-live",
		AgentID:     agentID.String(),
		StartedAt:   time.Now(),
		Image:       "img:latest",
	}
	got, err := m.RespawnIfDashboardExited(context.Background(), h, SpawnRequest{})
	if err != nil {
		t.Fatalf("RespawnIfDashboardExited with live handle: %v", err)
	}
	if got != h {
		t.Error("expected same handle instance when container is live")
	}
}

func TestManager_RespawnIfDashboardExited_DeadHandle_SpawnsFreshAndReturnsSentinel(t *testing.T) {
	m, agentID := newAttachTestAgent(t, "ctr-dead", "img:latest")
	probe := &fakeAttachProbe{result: AttachStatus{Alive: false, Status: "exited"}}
	m.SetAttachProbe(probe)
	// Replace the fake spawner with one that returns a fresh container ID.
	fs := &fakeSpawner{spawnResult: WorkerSpawnResult{ContainerID: "ctr-fresh"}}
	m.spawner = fs

	old := &Handle{
		ContainerID: "ctr-dead",
		AgentID:     agentID.String(),
		StartedAt:   time.Now().Add(-time.Hour),
		Image:       "img:latest",
	}
	req := SpawnRequest{
		Project:   "attach-demo",
		AgentType: "coder",
		AgentID:   agentID.String(),
	}
	fresh, err := m.RespawnIfDashboardExited(context.Background(), old, req)
	if !errors.Is(err, ErrDashboardRespawned) {
		t.Fatalf("expected ErrDashboardRespawned, got %v", err)
	}
	if fresh == nil {
		t.Fatal("expected fresh handle")
	}
	if fresh.ContainerID != "ctr-fresh" {
		t.Errorf("fresh.ContainerID = %q, want ctr-fresh", fresh.ContainerID)
	}
	if len(fs.spawnCalls) != 1 {
		t.Errorf("SpawnWorker calls = %d, want 1", len(fs.spawnCalls))
	}
	if len(fs.destroyCalls) != 1 {
		t.Errorf("DestroyWorker calls = %d, want 1 (old container tear-down)", len(fs.destroyCalls))
	}
	if fs.destroyCalls[0] != "ctr-dead" {
		t.Errorf("DestroyWorker called with %q, want ctr-dead", fs.destroyCalls[0])
	}
}

func TestManager_RespawnIfDashboardExited_NilHandle_ReturnsError(t *testing.T) {
	m := NewManager(nil, &fakeSpawner{}, nil, "")
	if _, err := m.RespawnIfDashboardExited(context.Background(), nil, SpawnRequest{}); err == nil {
		t.Fatal("expected error for nil handle")
	}
}

func TestManager_RespawnIfDashboardExited_NoProbe_PreservesHandle(t *testing.T) {
	m := NewManager(nil, &fakeSpawner{}, nil, "")
	h := &Handle{ContainerID: "ctr-x", AgentID: "a1"}
	got, err := m.RespawnIfDashboardExited(context.Background(), h, SpawnRequest{})
	if err != nil {
		t.Fatalf("RespawnIfDashboardExited without probe: %v", err)
	}
	if got != h {
		t.Error("expected same handle instance when no probe configured")
	}
}
