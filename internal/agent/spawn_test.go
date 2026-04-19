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

// fakeSpawner records every call so tests can assert on the exact
// parameters Manager.Spawn sends across the Spawner interface. The
// canned result is returned verbatim from SpawnWorker so tests can
// pre-populate it and inspect how Manager translates it into a Handle.
type fakeSpawner struct {
	spawnCalls   []WorkerSpawnParams
	destroyCalls []string
	spawnResult  WorkerSpawnResult
	spawnErr     error
	destroyErr   error
}

func (f *fakeSpawner) SpawnWorker(_ context.Context, p WorkerSpawnParams) (WorkerSpawnResult, error) {
	f.spawnCalls = append(f.spawnCalls, p)
	if f.spawnErr != nil {
		return WorkerSpawnResult{}, f.spawnErr
	}
	return f.spawnResult, nil
}

func (f *fakeSpawner) DestroyWorker(_ context.Context, containerID string) error {
	f.destroyCalls = append(f.destroyCalls, containerID)
	return f.destroyErr
}

func TestManager_Spawn_PopulatesParamsAndHandle(t *testing.T) {
	db := testutil.NewTestDB(t)
	// Pre-seed an agent row so Manager has something to update.
	project := model.Project{ID: uuid.New(), Name: "demo", BareRepoPath: "/tmp/x"}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: project.ID,
		AgentType: model.AgentCoder,
		Name:      "coder-x",
		Status:    model.AgentWorking,
	}
	if err := db.Create(&ag).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	fs := &fakeSpawner{
		spawnResult: WorkerSpawnResult{ContainerID: "ctr-abc", Endpoint: "10.1.2.3:8080"},
	}
	m := NewManager(db, fs, &ImageResolver{Language: "go"}, "/tmp/prompts")

	h, err := m.Spawn(context.Background(), SpawnRequest{
		Project:   "demo",
		AgentType: "coder",
		AgentID:   agentID.String(),
		TaskID:    "task-42",
		Branch:    "feature/x",
		Env:       map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatalf("Spawn: unexpected error: %v", err)
	}

	// Handle carries the fake's container ID and the resolved image.
	if h.ContainerID != "ctr-abc" {
		t.Errorf("Handle.ContainerID = %q, want %q", h.ContainerID, "ctr-abc")
	}
	if h.Image != "localhost:5000/drem-worker-go:latest" {
		t.Errorf("Handle.Image = %q, want worker-go:latest", h.Image)
	}
	if h.AgentID != agentID.String() {
		t.Errorf("Handle.AgentID = %q, want %q", h.AgentID, agentID.String())
	}
	if h.StartedAt.IsZero() {
		t.Error("Handle.StartedAt is zero")
	}

	// The spawner saw exactly one call with the expected identifying fields.
	if got := len(fs.spawnCalls); got != 1 {
		t.Fatalf("SpawnWorker calls = %d, want 1", got)
	}
	p := fs.spawnCalls[0]
	if p.Project != "demo" {
		t.Errorf("params.Project = %q, want demo", p.Project)
	}
	if p.AgentType != "coder" {
		t.Errorf("params.AgentType = %q, want coder", p.AgentType)
	}
	if p.WorkerID != agentID.String() {
		t.Errorf("params.WorkerID = %q, want %q", p.WorkerID, agentID.String())
	}
	if p.Branch != "feature/x" {
		t.Errorf("params.Branch = %q, want feature/x", p.Branch)
	}
	if p.Image != "localhost:5000/drem-worker-go:latest" {
		t.Errorf("params.Image = %q, want worker-go:latest", p.Image)
	}
	if p.Labels["drem.language"] != "go" {
		t.Errorf("params.Labels[drem.language] = %q, want go", p.Labels["drem.language"])
	}
	if p.Labels["drem.task_id"] != "task-42" {
		t.Errorf("params.Labels[drem.task_id] = %q, want task-42", p.Labels["drem.task_id"])
	}
	if p.Env["FOO"] != "bar" {
		t.Errorf("params.Env[FOO] = %q, want bar", p.Env["FOO"])
	}

	// DB row got container id + image in Config.
	var out model.Agent
	if err := db.First(&out, "id = ?", agentID).Error; err != nil {
		t.Fatalf("reload agent: %v", err)
	}
	if out.Config["container_id"] != "ctr-abc" {
		t.Errorf("agent.Config[container_id] = %v, want ctr-abc", out.Config["container_id"])
	}
	if out.Config["container_image"] != "localhost:5000/drem-worker-go:latest" {
		t.Errorf("agent.Config[container_image] = %v, want worker-go:latest", out.Config["container_image"])
	}
}

func TestManager_Spawn_UsesOverrideImage(t *testing.T) {
	db := testutil.NewTestDB(t)
	fs := &fakeSpawner{spawnResult: WorkerSpawnResult{ContainerID: "c1"}}
	m := NewManager(db, fs, &ImageResolver{
		Language: "go",
		Overrides: map[string]string{
			"coder": "localhost:5000/drem-worker-go:v9.9",
		},
	}, "")

	h, err := m.Spawn(context.Background(), SpawnRequest{
		Project:   "demo",
		AgentType: "coder",
		AgentID:   "agent-1",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if h.Image != "localhost:5000/drem-worker-go:v9.9" {
		t.Errorf("Handle.Image = %q, want override", h.Image)
	}
	if fs.spawnCalls[0].Image != "localhost:5000/drem-worker-go:v9.9" {
		t.Errorf("params.Image = %q, want override", fs.spawnCalls[0].Image)
	}
}

func TestManager_Spawn_UnmappableType_Errors(t *testing.T) {
	db := testutil.NewTestDB(t)
	fs := &fakeSpawner{}
	m := NewManager(db, fs, &ImageResolver{Language: "go"}, "")

	_, err := m.Spawn(context.Background(), SpawnRequest{
		Project:   "demo",
		AgentType: "unknown-type",
		AgentID:   "agent-1",
	})
	if err == nil {
		t.Fatal("Spawn with unmappable agent type: expected error, got nil")
	}
	if len(fs.spawnCalls) != 0 {
		t.Errorf("SpawnWorker should not be called on unmappable type (got %d calls)", len(fs.spawnCalls))
	}
}

func TestManager_Spawn_MissingRequiredFields(t *testing.T) {
	db := testutil.NewTestDB(t)
	fs := &fakeSpawner{}
	m := NewManager(db, fs, &ImageResolver{Language: "go"}, "")

	cases := []struct {
		name string
		req  SpawnRequest
	}{
		{"no project", SpawnRequest{AgentType: "merger", AgentID: "x"}},
		{"no agent type", SpawnRequest{Project: "p", AgentID: "x"}},
		{"no agent id", SpawnRequest{Project: "p", AgentType: "merger"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := m.Spawn(context.Background(), tc.req); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	if len(fs.spawnCalls) != 0 {
		t.Errorf("SpawnWorker should not be called on validation failures (got %d)", len(fs.spawnCalls))
	}
}

func TestManager_Spawn_ForwardsSpawnerError(t *testing.T) {
	db := testutil.NewTestDB(t)
	fs := &fakeSpawner{spawnErr: errors.New("boom")}
	m := NewManager(db, fs, &ImageResolver{Language: "go"}, "")

	_, err := m.Spawn(context.Background(), SpawnRequest{
		Project:   "demo",
		AgentType: "merger",
		AgentID:   "a1",
	})
	if err == nil {
		t.Fatal("expected error when spawner fails")
	}
}

func TestManager_Teardown_CallsDestroyAndMarksDead(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := model.Project{ID: uuid.New(), Name: "demo", BareRepoPath: "/tmp/x"}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: project.ID,
		AgentType: model.AgentCoder,
		Name:      "coder-x",
		Status:    model.AgentWorking,
	}
	if err := db.Create(&ag).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	fs := &fakeSpawner{}
	m := NewManager(db, fs, &ImageResolver{Language: "go"}, "")

	h := &Handle{
		ContainerID: "ctr-abc",
		AgentID:     agentID.String(),
		StartedAt:   time.Now(),
		Image:       "localhost:5000/drem-worker-go:latest",
	}
	if err := m.Teardown(context.Background(), h); err != nil {
		t.Fatalf("Teardown: %v", err)
	}

	if got := len(fs.destroyCalls); got != 1 {
		t.Fatalf("DestroyWorker calls = %d, want 1", got)
	}
	if fs.destroyCalls[0] != "ctr-abc" {
		t.Errorf("DestroyWorker containerID = %q, want ctr-abc", fs.destroyCalls[0])
	}

	var out model.Agent
	if err := db.First(&out, "id = ?", agentID).Error; err != nil {
		t.Fatalf("reload agent: %v", err)
	}
	if out.Status != model.AgentDead {
		t.Errorf("agent status after teardown = %q, want dead", out.Status)
	}
}

func TestManager_Teardown_NilHandle_ReturnsError(t *testing.T) {
	m := NewManager(nil, &fakeSpawner{}, nil, "")
	if err := m.Teardown(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil handle")
	}
}

func TestManager_Teardown_EmptyContainerID_ReturnsError(t *testing.T) {
	m := NewManager(nil, &fakeSpawner{}, nil, "")
	if err := m.Teardown(context.Background(), &Handle{AgentID: "a1"}); err == nil {
		t.Fatal("expected error for empty container id")
	}
}

func TestManager_PromptDir_DefaultLayout(t *testing.T) {
	m := NewManager(nil, &fakeSpawner{}, nil, "/tmp/projects")
	got := m.PromptDir("demo")
	want := "/tmp/projects/demo/prompts"
	if got != want {
		t.Errorf("PromptDir = %q, want %q", got, want)
	}
}
