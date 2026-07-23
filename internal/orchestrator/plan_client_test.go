package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// planClientRig wires the minimal Orchestrator shape the dispatch helpers
// reach for. healthz + /plan handlers are supplied by the caller so each
// test drives the transitions it needs.
func planClientRig(t *testing.T, healthz, plan http.HandlerFunc) (*Orchestrator, *httptest.Server, model.Task, model.Project) {
	t.Helper()
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	projectID := uuid.New()
	project := model.Project{
		ID:            projectID,
		Name:          "plan-client-test",
		BareRepoPath:  "/tmp/fake-bare",
		DefaultBranch: "main",
	}
	require.NoError(t, db.Create(&project).Error)

	mux := http.NewServeMux()
	if healthz != nil {
		mux.HandleFunc("GET /healthz", healthz)
	} else {
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		})
	}
	if plan != nil {
		mux.HandleFunc("POST /plan", plan)
	}
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	featureDir := t.TempDir()
	wt := &FakeWorktreeManager{
		BarePath:              "/tmp/fake-bare",
		Default:               "main",
		OnFeatureWorktreePath: func(name string) string { return featureDir },
	}

	// Runner with Claude as planner provider so shouldDispatchPlanHTTP
	// returns true.
	runner := agent.NewRunner(db, nil, nil, "/bin/false", "", 1, func(at model.AgentType) model.AgentCLIConfig {
		if at == model.AgentPlanner {
			return model.AgentCLIConfig{Provider: model.ProviderClaude, Model: "claude-opus-4-6", Effort: "high"}
		}
		return model.AgentCLIConfig{Effort: "medium"}
	})

	o := &Orchestrator{
		db:             db,
		projectID:      projectID,
		events:         make(chan Event, 32),
		worktree:       wt,
		runner:         runner,
		logger:         slog.Default().With("component", "plan_client_test"),
		GitrefRegistry: gitref.NewRegistry(db),
	}
	o.SetPlannerContainerEndpoint(ts.URL+"/plan", "shhh-token")

	task := model.Task{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Title:          "plan client test",
		Description:    "test",
		Status:         model.StatusPlanning,
		WorktreeBranch: "feature/client-test",
	}
	require.NoError(t, db.Create(&task).Error)
	return o, ts, task, project
}

// validPlanResponse returns a JSON body the planner-side handler can
// echo — mirrors planContainerResponse with a minimal valid plan.
func validPlanResponse(taskID string) map[string]any {
	return map[string]any{
		"task_id": taskID,
		"plan": map[string]any{
			"subtasks": []any{
				map[string]any{
					"title":       "test add",
					"description": "write test",
					"agent_type":  "coder",
					"phase":       "test",
					"tests_for":   []any{float64(1)},
					"files":       []any{"foo_test.go"},
				},
				map[string]any{
					"title":       "implement add",
					"description": "implement",
					"agent_type":  "coder",
					"phase":       "implementation",
					"files":       []any{"foo.go"},
				},
			},
		},
		"tokens_in":   1234,
		"tokens_out":  456,
		"duration_ms": 7890,
	}
}

// TestDispatchPlanHTTP_HappyPath: healthz 200 + /plan 200 → Success=true
// with the returned plan and token counts.
func TestDispatchPlanHTTP_HappyPath(t *testing.T) {
	var gotBody []byte
	var gotAuth string
	plan := func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(validPlanResponse("ignored"))
	}
	o, _, task, project := planClientRig(t, nil, plan)

	res, err := o.dispatchPlanHTTP(context.Background(), &task, &project, "prompt body")
	require.NoError(t, err)
	require.NotNil(t, res)
	require.True(t, res.Success, "healthy planner with 200 plan must succeed")
	require.NotNil(t, res.Plan)
	require.Contains(t, res.Plan, "subtasks")
	require.Equal(t, 1234, res.TokensIn)
	require.Equal(t, 456, res.TokensOut)
	require.Equal(t, 7890, res.DurationMS)

	assert.Equal(t, "Bearer shhh-token", gotAuth, "bearer token must be forwarded")
	var decoded planContainerRequest
	require.NoError(t, json.Unmarshal(gotBody, &decoded))
	assert.Equal(t, task.ID.String(), decoded.TaskID)
	assert.Equal(t, "high", decoded.Effort, "[agents.planner].effort must be forwarded")
}

// TestDispatchPlanHTTP_HealthzFailureShortCircuits: when /healthz returns
// 503, dispatch must NOT POST /plan — saves the 5-minute wait on a
// guaranteed 401.
func TestDispatchPlanHTTP_HealthzFailureShortCircuits(t *testing.T) {
	healthz := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	planCalled := 0
	plan := func(w http.ResponseWriter, r *http.Request) {
		planCalled++
		w.WriteHeader(http.StatusOK)
	}
	o, _, task, project := planClientRig(t, healthz, plan)

	res, err := o.dispatchPlanHTTP(context.Background(), &task, &project, "prompt body")
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.Success)
	assert.Equal(t, "planner_unhealthy", res.FailureReason)
	assert.Equal(t, 0, planCalled, "/plan must not be POSTed when /healthz fails")
}

// TestDispatchPlanHTTP_409ValidationFailure: planner's 409 surfaces as
// FailureReason=plan_validation_failed so orch counts it against the
// retry budget but doesn't flag it as infrastructure failure.
func TestDispatchPlanHTTP_409ValidationFailure(t *testing.T) {
	plan := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}
	o, _, task, project := planClientRig(t, nil, plan)

	res, err := o.dispatchPlanHTTP(context.Background(), &task, &project, "prompt")
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Equal(t, "plan_validation_failed", res.FailureReason)
}

// TestDispatchPlanHTTP_502AnthropicUpstream: planner's 502 maps to
// FailureReason=anthropic_upstream.
func TestDispatchPlanHTTP_502AnthropicUpstream(t *testing.T) {
	plan := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}
	o, _, task, project := planClientRig(t, nil, plan)

	res, err := o.dispatchPlanHTTP(context.Background(), &task, &project, "prompt")
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Equal(t, "anthropic_upstream", res.FailureReason)
}

// TestDispatchPlanHTTP_504Timeout: planner's 504 maps to FailureReason=timeout.
func TestDispatchPlanHTTP_504Timeout(t *testing.T) {
	plan := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
	}
	o, _, task, project := planClientRig(t, nil, plan)

	res, err := o.dispatchPlanHTTP(context.Background(), &task, &project, "prompt")
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Equal(t, "timeout", res.FailureReason)
}

// TestDispatchPlanHTTP_401IsInfrastructureError: planner's 401 is a config
// mismatch (token drift). Return a Go error so the caller surfaces it
// loudly rather than silently counting it against retry budget.
func TestDispatchPlanHTTP_401IsInfrastructureError(t *testing.T) {
	plan := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}
	o, _, task, project := planClientRig(t, nil, plan)

	_, err := o.dispatchPlanHTTP(context.Background(), &task, &project, "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

// TestDispatchPlanHTTP_MalformedResponse: the planner returns 200 with a
// body that doesn't parse. Surfaces as a Go error so the caller can log
// and let orch retry on the next tick.
func TestDispatchPlanHTTP_MalformedResponse(t *testing.T) {
	plan := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}
	o, _, task, project := planClientRig(t, nil, plan)

	_, err := o.dispatchPlanHTTP(context.Background(), &task, &project, "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode response")
}

// TestDispatchPlanHTTP_ClientSideValidationFailure: planner returned 200
// but the plan is malformed (empty subtasks). Orch's defensive
// validation trips → FailureReason=plan_validation_failed, no Plan
// stored.
func TestDispatchPlanHTTP_ClientSideValidationFailure(t *testing.T) {
	plan := func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"task_id": "x",
			"plan":    map[string]any{"subtasks": []any{}},
		})
	}
	o, _, task, project := planClientRig(t, nil, plan)

	res, err := o.dispatchPlanHTTP(context.Background(), &task, &project, "prompt")
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Equal(t, "plan_validation_failed", res.FailureReason)
}

// ---------------------------------------------------------------------------
// shouldDispatchPlanHTTP routing tests
// ---------------------------------------------------------------------------

// TestShouldDispatchPlanHTTP_TrueWhenConfigured: endpoint set + provider
// claude → HTTP path applies.
func TestShouldDispatchPlanHTTP_TrueWhenConfigured(t *testing.T) {
	o, _, _, _ := planClientRig(t, nil, nil)
	require.True(t, o.shouldDispatchPlanHTTP())
}

// TestShouldDispatchPlanHTTP_FalseWhenURLEmpty: no endpoint → legacy
// runner path.
func TestShouldDispatchPlanHTTP_FalseWhenURLEmpty(t *testing.T) {
	o, _, _, _ := planClientRig(t, nil, nil)
	o.SetPlannerContainerEndpoint("", "")
	require.False(t, o.shouldDispatchPlanHTTP())
}

// TestShouldDispatchPlanHTTP_FalseWhenProviderSGLang: operator override
// (provider=sglang-direct) → legacy path, not HTTP. Avoids dispatching
// claude-specific flow against a gemma-configured planner.
func TestShouldDispatchPlanHTTP_FalseWhenProviderSGLang(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	projectID := uuid.New()
	require.NoError(t, db.Create(&model.Project{
		ID: projectID, Name: "sglang", BareRepoPath: "/tmp/fake", DefaultBranch: "main",
	}).Error)
	runner := agent.NewRunner(db, nil, nil, "/bin/false", "", 1, func(at model.AgentType) model.AgentCLIConfig {
		if at == model.AgentPlanner {
			return model.AgentCLIConfig{Provider: model.ProviderSGLangDirect, Model: "gemma4-26b"}
		}
		return model.AgentCLIConfig{}
	})
	o := &Orchestrator{
		db:                    db,
		projectID:             projectID,
		events:                make(chan Event, 16),
		worktree:              &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"},
		runner:                runner,
		logger:                slog.Default(),
		GitrefRegistry:        gitref.NewRegistry(db),
		plannerContainerURL:   "http://drem-planner:8090/plan",
		plannerContainerToken: "t",
	}
	require.False(t, o.shouldDispatchPlanHTTP())
}

// ---------------------------------------------------------------------------
// spawnPlannerHTTP integration: driver + bookkeeping
// ---------------------------------------------------------------------------

// TestSpawnPlannerHTTP_StoresPlanAndIncrementsCounter: happy path
// propagates res.Plan onto task.Plan, clears any stale agent assignment,
// and bumps total_planner_spawns.
func TestSpawnPlannerHTTP_StoresPlanAndIncrementsCounter(t *testing.T) {
	plan := func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(validPlanResponse("x"))
	}
	o, _, task, project := planClientRig(t, nil, plan)

	require.NoError(t, o.spawnPlannerHTTP(&task, &project, "prompt"))

	var updated model.Task
	require.NoError(t, o.db.First(&updated, "id = ?", task.ID).Error)
	require.NotNil(t, updated.Plan)
	require.Contains(t, updated.Plan, "subtasks")
	require.Equal(t, float64(1), updated.Context["total_planner_spawns"])
}

// TestSpawnPlannerHTTP_ValidationFailureDoesNotStorePlan: planner
// returned an invalid plan → task.Plan stays nil but
// total_planner_spawns still increments so retry budget is respected.
func TestSpawnPlannerHTTP_ValidationFailureDoesNotStorePlan(t *testing.T) {
	plan := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict) // validation failure
	}
	o, _, task, project := planClientRig(t, nil, plan)

	require.NoError(t, o.spawnPlannerHTTP(&task, &project, "prompt"))

	var updated model.Task
	require.NoError(t, o.db.First(&updated, "id = ?", task.ID).Error)
	require.Nil(t, updated.Plan, "no plan must be stored when planner 409d")
	require.Equal(t, float64(1), updated.Context["total_planner_spawns"],
		"attempt still counts against retry budget")
}

func TestSpawnPlannerHTTP_RejectsInventedDirectoryTree(t *testing.T) {
	plan := func(w http.ResponseWriter, r *http.Request) {
		response := validPlanResponse("x")
		response["plan"] = map[string]any{"subtasks": []any{
			map[string]any{
				"title": "write test", "description": "test", "agent_type": "coder", "phase": "test",
				"tests_for": []any{float64(1)}, "files": []any{"invented/pkg/widget_test.go"},
			},
			map[string]any{
				"title": "implement widget", "description": "implementation", "agent_type": "coder", "phase": "implementation",
				"files": []any{"invented/pkg/widget.go"},
			},
		}}
		_ = json.NewEncoder(w).Encode(response)
	}
	o, _, task, project := planClientRig(t, nil, plan)

	require.NoError(t, o.spawnPlannerHTTP(&task, &project, "prompt"))

	var updated model.Task
	require.NoError(t, o.db.First(&updated, "id = ?", task.ID).Error)
	require.Nil(t, updated.Plan)
	validation, ok := updated.Context["plan_validation"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, false, validation["valid"])
}

func TestSpawnPlannerHTTP_DiscardsResultAfterConcurrentCancellation(t *testing.T) {
	var orch *Orchestrator
	var taskID uuid.UUID
	plan := func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, orch.db.Exec(
			"UPDATE tasks SET status = ?, state_version = state_version + 1 WHERE id = ?",
			model.StatusCancelled, taskID,
		).Error)
		_ = json.NewEncoder(w).Encode(validPlanResponse("x"))
	}
	o, _, task, project := planClientRig(t, nil, plan)
	orch, taskID = o, task.ID

	require.NoError(t, o.spawnPlannerHTTP(&task, &project, "prompt"))

	var updated model.Task
	require.NoError(t, o.db.First(&updated, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusCancelled, updated.Status)
	require.Nil(t, updated.Plan, "stale planner response must not resurrect a cancelled task")
}

// TestSpawnPlannerHTTP_HealthzFailDoesNotStorePlan: /healthz fails so
// dispatch short-circuits. Counter still increments so retry budget is
// respected; task stays in PLANNING.
func TestSpawnPlannerHTTP_HealthzFailDoesNotStorePlan(t *testing.T) {
	healthz := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	o, _, task, project := planClientRig(t, healthz, nil)

	require.NoError(t, o.spawnPlannerHTTP(&task, &project, "prompt"))

	var updated model.Task
	require.NoError(t, o.db.First(&updated, "id = ?", task.ID).Error)
	require.Nil(t, updated.Plan)
	require.Equal(t, model.StatusPlanning, updated.Status)
	require.Equal(t, float64(1), updated.Context["total_planner_spawns"])
}

func TestProcessPlanningWarmHTTPDoesNotConsumeLegacyAgentCapacity(t *testing.T) {
	plan := func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(validPlanResponse("x"))
	}
	o, _, task, _ := planClientRig(t, nil, plan)
	o.runner = agent.NewRunner(o.db, nil, nil, "/bin/false", "", 0, func(at model.AgentType) model.AgentCLIConfig {
		if at == model.AgentPlanner {
			return model.AgentCLIConfig{Provider: model.ProviderCodex, Model: "gpt-5.4-mini", Effort: "high"}
		}
		return model.AgentCLIConfig{}
	})

	require.NoError(t, o.processPlanning(&task))

	var updated model.Task
	require.Eventually(t, func() bool {
		if err := o.db.First(&updated, "id = ?", task.ID).Error; err != nil {
			return false
		}
		return updated.Plan != nil
	}, time.Second, 10*time.Millisecond)
	require.NotNil(t, updated.Plan, "warm planner must run even when legacy agent capacity is zero")
}

func TestProcessPlanningWarmHTTPDoesNotBlockLifecycleTickOrDuplicate(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	requestCount := make(chan struct{}, 2)
	plan := func(w http.ResponseWriter, r *http.Request) {
		requestCount <- struct{}{}
		select {
		case <-requestStarted:
		default:
			close(requestStarted)
		}
		<-releaseRequest
		_ = json.NewEncoder(w).Encode(validPlanResponse("x"))
	}
	o, _, task, _ := planClientRig(t, nil, plan)

	started := time.Now()
	require.NoError(t, o.processPlanning(&task))
	require.Less(t, time.Since(started), 100*time.Millisecond,
		"warm planner latency must not block the lifecycle tick")
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("planner request did not start")
	}

	// A second tick while the first request is live must reuse the in-memory
	// claim instead of sending a duplicate plan request.
	require.NoError(t, o.processPlanning(&task))
	close(releaseRequest)
	require.Eventually(t, func() bool {
		var updated model.Task
		return o.db.First(&updated, "id = ?", task.ID).Error == nil && updated.Plan != nil
	}, time.Second, 10*time.Millisecond)
	require.Len(t, requestCount, 1)
}

// ---------------------------------------------------------------------------
// validatePlanJSON — regression coverage for the logic migrated from
// the deleted plan_dispatch.go.
// ---------------------------------------------------------------------------

// TestValidatePlanJSON_Cases walks every bullet of plans/warm-planner-
// pivot.md §6 so a regression trips the suite instead of silently
// passing a bad plan to the next stage.
func TestValidatePlanJSON_Cases(t *testing.T) {
	cases := []struct {
		name    string
		plan    model.JSONField
		wantErr string
	}{
		{"missing subtasks", model.JSONField{}, "missing subtasks"},
		{"subtasks not array", model.JSONField{"subtasks": "nope"}, "not an array"},
		{"empty subtasks", model.JSONField{"subtasks": []any{}}, "empty"},
		{
			name: "tests_for out of range",
			plan: model.JSONField{
				"subtasks": []any{
					map[string]any{"title": "a", "tests_for": []any{float64(99)}},
					map[string]any{"title": "b"},
				},
			},
			wantErr: "out of range",
		},
		{
			name: "dependencies non-integer",
			plan: model.JSONField{
				"subtasks": []any{
					map[string]any{"title": "a"},
					map[string]any{"title": "b", "dependencies": []any{"not a num"}},
				},
			},
			wantErr: "non-integer",
		},
		{
			name: "subtask not an object",
			plan: model.JSONField{
				"subtasks": []any{"just a string"},
			},
			wantErr: "not an object",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePlanJSON(tc.plan)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestValidatePlanJSON_HappyPath: a minimally-valid plan returns nil.
func TestValidatePlanJSON_HappyPath(t *testing.T) {
	plan := model.JSONField{
		"subtasks": []any{
			map[string]any{"title": "test", "phase": "test", "tests_for": []any{float64(1)}},
			map[string]any{"title": "impl", "phase": "implementation"},
		},
	}
	require.NoError(t, validatePlanJSON(plan))
}

// TestDerivePlannerHealthURL ensures the /plan → /healthz derivation
// trims cleanly for every URL shape the orchestrator might encounter.
func TestDerivePlannerHealthURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"http://drem-planner:8090/plan", "http://drem-planner:8090/healthz"},
		{"http://planner.example/plan", "http://planner.example/healthz"},
		{"http://bare/", "http://bare/healthz"},
		{"http://bare", "http://bare/healthz"},
	}
	for _, tc := range cases {
		got, err := derivePlannerHealthURL(tc.in)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got)
	}
	_, err := derivePlannerHealthURL("")
	require.Error(t, err)
}
