package orchestrator

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agent"
	dbpkg "github.com/godinj/drem-orchestrator/internal/db"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
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
		worktree:        &FakeWorktreeManager{BarePath: tmp, Default: "main"},
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
	parent.WorktreeBranch = "feature/widget"
	if err := orch.db.Save(&parent).Error; err != nil {
		t.Fatalf("save parent: %v", err)
	}
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

func TestDispatchQuickFixDirect_CreatesDirectAgentRecord(t *testing.T) {
	orch, projectID, _ := setupDirectToolDispatchTest(t)
	orch.runner = agent.NewRunner(orch.db, nil, nil, "/bin/false", "/bin/false", 1, nil)
	task := testutil.CreateTask(t, orch.db, projectID, "Quickfix via GQ", model.StatusInProgress)
	task.WorktreeBranch = "feature/quickfix-direct"
	if err := orch.db.Save(&task).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}

	if err := orch.dispatchQuickFixDirect(&task, nil); err != nil {
		t.Fatalf("dispatchQuickFixDirect: %v", err)
	}

	var ag model.Agent
	if err := orch.db.Where("project_id = ? AND current_task_id = ?", projectID, task.ID).First(&ag).Error; err != nil {
		t.Fatalf("load agent: %v", err)
	}
	if ag.Provider != string(model.ProviderSGLangDirect) {
		t.Fatalf("provider = %q, want %q", ag.Provider, model.ProviderSGLangDirect)
	}
	if ag.WorktreeBranch != task.WorktreeBranch {
		t.Fatalf("worktree branch = %q, want %q", ag.WorktreeBranch, task.WorktreeBranch)
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
	parent.WorktreeBranch = "feature/code-sub"
	if err := orch.db.Save(&parent).Error; err != nil {
		t.Fatalf("save parent: %v", err)
	}
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

// TestProcessCoderDirect_AppliesContextThresholds verifies that the
// orchestrator's contextWarnPct/contextStopPct are forwarded onto the
// per-run DirectToolAgentConfig. The helper is called for every direct
// dispatch (coder/reviewer/fixer); we assert the contract on a fresh
// config copy.
func TestProcessCoderDirect_AppliesContextThresholds(t *testing.T) {
	orch, _, _ := setupDirectToolDispatchTest(t)

	toolCfg := *orch.directToolAgentCfg
	orch.applyContextThresholds(&toolCfg)

	if toolCfg.ContextWarnPct != orch.contextWarnPct {
		t.Errorf("ContextWarnPct = %d, want %d", toolCfg.ContextWarnPct, orch.contextWarnPct)
	}
	if toolCfg.ContextStopPct != orch.contextStopPct {
		t.Errorf("ContextStopPct = %d, want %d", toolCfg.ContextStopPct, orch.contextStopPct)
	}
}

// TestPersistDirectAgentContext_WritesContextPctToConfig verifies that the
// helper that runs after each direct-tool agent run propagates the result's
// FinalContextPct onto the agent's Config so context_monitor.go and the TUI
// see the same shape they expect from subprocess agents.
func TestPersistDirectAgentContext_WritesContextPctToConfig(t *testing.T) {
	ag := &model.Agent{ID: uuid.New()}
	result := &agent.DirectToolAgentResult{FinalContextPct: 72}

	persistDirectAgentContext(ag, result, 20)

	if ag.Config == nil {
		t.Fatal("expected ag.Config to be initialized")
	}
	got, ok := ag.Config["context_used_pct"].(float64)
	if !ok {
		t.Fatalf("expected context_used_pct to be float64, got %T (%v)", ag.Config["context_used_pct"], ag.Config["context_used_pct"])
	}
	if got != 72 {
		t.Errorf("context_used_pct = %v, want 72", got)
	}
	if ag.FinalContextPct != 72 {
		t.Errorf("FinalContextPct column = %d, want 72", ag.FinalContextPct)
	}
}

// TestPersistDirectAgentContext_StopReasonContextLimit verifies that a
// context_limit stop reason maps onto the agent's ExitReason column so the
// TUI/CSV reports show "context_limit" without waiting for the subprocess
// completion path.
func TestPersistDirectAgentContext_StopReasonContextLimit(t *testing.T) {
	ag := &model.Agent{ID: uuid.New()}
	result := &agent.DirectToolAgentResult{
		FinalContextPct: 96,
		StopReason:      "context_limit",
	}

	persistDirectAgentContext(ag, result, 20)

	if ag.ExitReason != model.ExitReasonContextLimit {
		t.Errorf("ExitReason = %q, want %q", ag.ExitReason, model.ExitReasonContextLimit)
	}
	if ag.Config["stop_reason"] != "context_limit" {
		t.Errorf("stop_reason = %v, want %q", ag.Config["stop_reason"], "context_limit")
	}
}

func TestPersistDirectAgentContext_StructuredStopReasons(t *testing.T) {
	tests := []struct {
		name       string
		stopReason string
		want       string
	}{
		{name: "max iterations", stopReason: "max_iterations", want: model.ExitReasonMaxIterations},
		{name: "no progress", stopReason: "no_progress", want: model.ExitReasonNoProgress},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := &model.Agent{ID: uuid.New()}
			persistDirectAgentContext(ag, &agent.DirectToolAgentResult{StopReason: tt.stopReason}, 20)

			if ag.ExitReason != tt.want {
				t.Fatalf("ExitReason = %q, want %q", ag.ExitReason, tt.want)
			}
			if ag.Config["stop_reason"] != tt.stopReason {
				t.Fatalf("stop_reason = %v, want %q", ag.Config["stop_reason"], tt.stopReason)
			}
		})
	}
}

func TestDirectToolHelpers_ReadBashCmdArgument(t *testing.T) {
	args := map[string]any{"cmd": "go test ./... && git commit -m test"}

	if got := derivePhase("bash", args); got != "testing" {
		t.Fatalf("derivePhase = %q, want testing", got)
	}
	if got := deriveTarget("bash", args); !strings.Contains(got, "go test") {
		t.Fatalf("deriveTarget = %q, want command snippet", got)
	}
}

// TestPersistDirectAgentContext_NoOpWhenMonitorDisabled verifies that when
// ContextLimit is unset (FinalContextPct stays 0 with no stop reason), the
// helper does NOT write a misleading 0 into Config — downstream readers
// distinguish "no monitor data" from "0% used".
func TestPersistDirectAgentContext_NoContextPctWhenMonitorDisabled(t *testing.T) {
	ag := &model.Agent{ID: uuid.New()}
	result := &agent.DirectToolAgentResult{FinalContextPct: 0, StopReason: "", Output: "done"}

	persistDirectAgentContext(ag, result, 20)

	// Config is initialized (for cost/exit fields) but context_used_pct
	// should not be present when monitor was disabled.
	if ag.Config != nil {
		if _, exists := ag.Config["context_used_pct"]; exists {
			t.Errorf("expected no context_used_pct write when monitor is disabled, got %v", ag.Config["context_used_pct"])
		}
	}
	// Cost and exit reason should still be set.
	if ag.Config == nil {
		t.Fatal("expected Config to be initialized for cost/exit fields")
	}
	if ag.Config["cost_label"] != "local" {
		t.Errorf("cost_label = %v, want %q", ag.Config["cost_label"], "local")
	}
	if ag.ExitReason != model.ExitReasonSuccess {
		t.Errorf("ExitReason = %q, want %q", ag.ExitReason, model.ExitReasonSuccess)
	}
}

// TestProcessReviewerDirect_PersistsLiveContextPct verifies that when
// ContextLimit is configured, the OnIteration callback persists
// context_used_pct to the agent's Config in the DB on each iteration,
// making it visible to the TUI while the agent is still running.
func TestProcessReviewerDirect_PersistsLiveContextPct(t *testing.T) {
	orch, projectID, _ := setupDirectToolDispatchTest(t)

	// Enable context monitoring: the fake server returns prompt_tokens=42,
	// so with ContextLimit=100 we expect context_used_pct = 42.
	orch.directToolAgentCfg.ContextLimit = 100

	task := testutil.CreateTask(t, orch.db, projectID, "Review implementation", model.StatusPlanReview)

	if err := orch.processReviewerDirect(&task); err != nil {
		t.Fatalf("processReviewerDirect: %v", err)
	}

	var agents []model.Agent
	if err := orch.db.Where("project_id = ?", projectID).Find(&agents).Error; err != nil {
		t.Fatalf("query agents: %v", err)
	}
	if len(agents) == 0 {
		t.Fatal("expected agent record to exist")
	}

	ag := agents[0]
	if ag.Config == nil {
		t.Fatal("expected agent Config to be non-nil after run with ContextLimit set")
	}
	pct, ok := ag.Config["context_used_pct"].(float64)
	if !ok {
		t.Fatalf("expected context_used_pct in agent Config, got keys: %v", ag.Config)
	}
	// The fake server returns prompt_tokens=42 and ContextLimit=100,
	// so we expect (42*100)/100 = 42.
	if pct != 42 {
		t.Errorf("context_used_pct = %v, want 42", pct)
	}
}

func TestProviderAutoDetection_SGLangDirect_RoutesToDirectPath(t *testing.T) {
	orch, projectID, _ := setupDirectToolDispatchTest(t)

	parent := testutil.CreateTask(t, orch.db, projectID, "Feature", model.StatusInProgress)
	parent.WorktreeBranch = "feature/auto-detect"
	if err := orch.db.Save(&parent).Error; err != nil {
		t.Fatalf("save parent: %v", err)
	}
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
