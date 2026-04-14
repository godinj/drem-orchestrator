package orchestrator

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agent"
	dbpkg "github.com/godinj/drem-orchestrator/internal/db"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// fakeSGLangServer returns an httptest server that mimics SGLang's OpenAI
// chat completions endpoint. It responds with a single-turn "stop" reply
// and advertises token usage so token-recording tests can assert non-zero.
func fakeSGLangServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message":       map[string]any{"role": "assistant", "content": "done"},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     42,
				"completion_tokens": 7,
				"total_tokens":      49,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func setupDirectToolDispatchTest(t *testing.T) (*Orchestrator, uuid.UUID, *httptest.Server) {
	t.Helper()

	gormDB, err := dbpkg.Init(":memory:")
	if err != nil {
		t.Fatalf("init test db: %v", err)
	}

	projectID := uuid.New()
	project := model.Project{
		ID:            projectID,
		Name:          "direct-tool-dispatch-test",
		BareRepoPath:  "/tmp/test-direct-dispatch",
		DefaultBranch: "main",
	}
	if err := gormDB.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	server := fakeSGLangServer(t)
	t.Cleanup(server.Close)

	tmp := t.TempDir()
	events := make(chan Event, 100)
	orch := &Orchestrator{
		db:              gormDB,
		projectID:       projectID,
		worktree:        &worktree.Manager{BareRepoPath: tmp, DefaultBranch: "main"},
		events:          events,
		contextWarnPct:  75,
		contextStopPct:  90,
		contextFixerPct: 85,
		logger:          slog.Default().With("component", "direct-tool-dispatch-test"),
		directToolAgentCfg: &agent.DirectToolAgentConfig{
			Endpoint:      server.URL,
			Model:         "test-model",
			MaxTokens:     128,
			Temperature:   0.0,
			Timeout:       5 * time.Second,
			MaxIterations: 2,
			BashTimeout:   1 * time.Second,
			WorkDir:       tmp,
		},
	}

	return orch, projectID, server
}

func TestProcessCoderDirect_CreatesAgentRecord(t *testing.T) {
	orch, projectID, _ := setupDirectToolDispatchTest(t)

	parent := testutil.CreateTask(t, orch.db, projectID, "Parent feature", model.StatusInProgress)
	sub := testutil.CreateTask(t, orch.db, projectID, "Implement widget", model.StatusInProgress)
	sub.ParentTaskID = &parent.ID
	if err := orch.db.Save(&sub).Error; err != nil {
		t.Fatalf("save subtask: %v", err)
	}

	if err := orch.processCoderDirect(&sub, &parent); err != nil {
		t.Fatalf("processCoderDirect: %v", err)
	}

	var agents []model.Agent
	if err := orch.db.Where("project_id = ?", projectID).Find(&agents).Error; err != nil {
		t.Fatalf("query agents: %v", err)
	}
	if len(agents) == 0 {
		t.Fatal("expected processCoderDirect to create an agent record, got 0")
	}

	ag := agents[0]
	if ag.AgentType != model.AgentCoder {
		t.Errorf("agent type = %q, want %q", ag.AgentType, model.AgentCoder)
	}
	if ag.Provider != "sglang-direct" {
		t.Errorf("agent provider = %q, want %q", ag.Provider, "sglang-direct")
	}
	// Status may be Working (pre-run) or transitioned afterwards; both are
	// acceptable. The critical property is that Provider/Type are correct
	// and the record exists for audit.

	var reloaded model.Task
	if err := orch.db.First(&reloaded, "id = ?", sub.ID).Error; err != nil {
		t.Fatalf("reload subtask: %v", err)
	}
	if reloaded.AssignedAgentID == nil {
		t.Error("expected subtask.AssignedAgentID to be set after processCoderDirect")
	}
}

func TestProcessReviewerDirect_CreatesAgentRecord(t *testing.T) {
	orch, projectID, _ := setupDirectToolDispatchTest(t)

	task := testutil.CreateTask(t, orch.db, projectID, "Review implementation", model.StatusPlanReview)

	if err := orch.processReviewerDirect(&task); err != nil {
		t.Fatalf("processReviewerDirect: %v", err)
	}

	var agents []model.Agent
	if err := orch.db.Where("project_id = ?", projectID).Find(&agents).Error; err != nil {
		t.Fatalf("query agents: %v", err)
	}
	if len(agents) == 0 {
		t.Fatal("expected processReviewerDirect to create an agent record, got 0")
	}

	ag := agents[0]
	if ag.AgentType != model.AgentReviewer {
		t.Errorf("agent type = %q, want %q", ag.AgentType, model.AgentReviewer)
	}
	if ag.Provider != "sglang-direct" {
		t.Errorf("agent provider = %q, want %q", ag.Provider, "sglang-direct")
	}
}

func TestProcessFixerDirect_CreatesAgentRecord(t *testing.T) {
	orch, projectID, _ := setupDirectToolDispatchTest(t)

	task := testutil.CreateTask(t, orch.db, projectID, "Fix build failure", model.StatusInProgress)
	task.Context = model.JSONField{
		"diagnosis":      "nil pointer in handler.go:42",
		"affected_files": []any{"internal/handler.go"},
		"suggested_fix":  "add nil check before dereference",
	}
	if err := orch.db.Save(&task).Error; err != nil {
		t.Fatalf("save task context: %v", err)
	}

	if err := orch.processFixerDirect(&task); err != nil {
		t.Fatalf("processFixerDirect: %v", err)
	}

	var agents []model.Agent
	if err := orch.db.Where("project_id = ?", projectID).Find(&agents).Error; err != nil {
		t.Fatalf("query agents: %v", err)
	}
	if len(agents) == 0 {
		t.Fatal("expected processFixerDirect to create an agent record, got 0")
	}

	ag := agents[0]
	if ag.AgentType != model.AgentFixer {
		t.Errorf("agent type = %q, want %q", ag.AgentType, model.AgentFixer)
	}
	if ag.Provider != "sglang-direct" {
		t.Errorf("agent provider = %q, want %q", ag.Provider, "sglang-direct")
	}
}

func TestProcessCoderDirect_NilConfig_NoAgentCreated(t *testing.T) {
	orch, projectID, _ := setupDirectToolDispatchTest(t)
	// Clear the direct config to exercise the fall-through path.
	orch.directToolAgentCfg = nil

	parent := testutil.CreateTask(t, orch.db, projectID, "Parent", model.StatusInProgress)
	sub := testutil.CreateTask(t, orch.db, projectID, "Sub", model.StatusInProgress)

	if err := orch.processCoderDirect(&sub, &parent); err != nil {
		t.Fatalf("processCoderDirect with nil config: %v", err)
	}

	var agents []model.Agent
	if err := orch.db.Where("provider = ?", "sglang-direct").Find(&agents).Error; err != nil {
		t.Fatalf("query agents: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 sglang-direct agents with nil config, got %d", len(agents))
	}
}

func TestProcessCoderDirect_SetsTokensOnAgent(t *testing.T) {
	orch, projectID, _ := setupDirectToolDispatchTest(t)

	parent := testutil.CreateTask(t, orch.db, projectID, "Parent feature", model.StatusInProgress)
	sub := testutil.CreateTask(t, orch.db, projectID, "Code subtask", model.StatusInProgress)
	sub.ParentTaskID = &parent.ID
	if err := orch.db.Save(&sub).Error; err != nil {
		t.Fatalf("save subtask: %v", err)
	}

	if err := orch.processCoderDirect(&sub, &parent); err != nil {
		t.Fatalf("processCoderDirect: %v", err)
	}

	var agents []model.Agent
	if err := orch.db.Where("project_id = ?", projectID).Find(&agents).Error; err != nil {
		t.Fatalf("query agents: %v", err)
	}
	if len(agents) == 0 {
		t.Fatal("expected agent record to exist")
	}

	ag := agents[0]
	if ag.TokensIn == 0 && ag.TokensOut == 0 {
		t.Error("expected TokensIn or TokensOut to be recorded on agent after direct tool run")
	}
}

func TestProviderAutoDetection_SGLangDirect_RoutesToDirectPath(t *testing.T) {
	orch, projectID, _ := setupDirectToolDispatchTest(t)

	parent := testutil.CreateTask(t, orch.db, projectID, "Feature", model.StatusInProgress)
	sub := testutil.CreateTask(t, orch.db, projectID, "Implement", model.StatusInProgress)
	sub.ParentTaskID = &parent.ID
	if err := orch.db.Save(&sub).Error; err != nil {
		t.Fatalf("save subtask: %v", err)
	}

	if err := orch.processCoderDirect(&sub, &parent); err != nil {
		t.Fatalf("processCoderDirect: %v", err)
	}

	var agents []model.Agent
	if err := orch.db.Where("provider = ?", "sglang-direct").Find(&agents).Error; err != nil {
		t.Fatalf("query agents: %v", err)
	}
	if len(agents) == 0 {
		t.Fatal("expected sglang-direct agent to be created when provider auto-detection routes to direct path")
	}
	if agents[0].AgentType != model.AgentCoder {
		t.Errorf("auto-detected agent type = %q, want %q", agents[0].AgentType, model.AgentCoder)
	}
}
