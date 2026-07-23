// plan_client.go wires the orchestrator to the warm drem-planner HTTP
// server. Replaces the spawn-on-demand plan_dispatch.go — see
// plans/warm-planner-pivot.md §0 for the design pivot rationale.
//
// Flow: orch's processPlanning → dispatchPlanHTTP → POST /plan →
// planner returns plan inline → validatePlanJSON → persist onto task.
//
// No shared filesystem. No container spawn. Planner is long-lived; orch
// just POSTs and reads the response.

package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
)

// PlanResult is the public shape dispatchPlanHTTP returns. Mirrors the
// retired PlanResult shape from plan_dispatch.go so processPlanning can
// branch on Success/FailureReason identically.
type PlanResult struct {
	Success       bool
	FailureReason string
	// Plan is the parsed plan.json when Success is true; nil otherwise.
	Plan model.JSONField
	// TokensIn / TokensOut carry the planner's reported usage for
	// observability; 0 when absent.
	TokensIn  int
	TokensOut int
	// DurationMS is the planner-reported wall time (not including orch
	// round-trip).
	DurationMS int
}

// plannerDispatchTimeout bounds how long orch waits for a plan to return
// over HTTP. Matches the planner's own default model timeout (5 min) plus
// a small buffer for the HTTP transport.
const plannerDispatchTimeout = 6 * time.Minute

// plannerHealthzTimeout is the per-/healthz probe bound. Short because
// /healthz is meant to return in tens of milliseconds; anything longer is
// effectively unhealthy.
const plannerHealthzTimeout = 3 * time.Second

// dispatchPlanHTTP POSTs the given task + context to the warm drem-planner
// HTTP endpoint and returns the resulting PlanResult. Gates on /healthz
// before POSTing so missing credentials surface as a fail-fast
// FailureReason rather than a wasted 5-minute wait.
func (o *Orchestrator) dispatchPlanHTTP(ctx context.Context, task *model.Task, project *model.Project, plannerPrompt string) (*PlanResult, error) {
	if o.plannerContainerURL == "" {
		return nil, errors.New("dispatchPlanHTTP: plannerContainerURL not configured")
	}

	// Pre-flight: /healthz must return 200 before we POST. Costs ~10ms
	// and catches the "operator hasn't run codex login" case without
	// eating a full model timeout.
	if err := o.probePlannerHealthz(ctx); err != nil {
		o.logger.Warn("planner /healthz failed; aborting dispatch",
			"task_id", task.ID, "error", err)
		return &PlanResult{
			Success:       false,
			FailureReason: "planner_unhealthy",
		}, nil
	}

	req := planContainerRequest{
		TaskID:       task.ID.String(),
		Task:         taskToJSON(task),
		Project:      projectToJSON(project),
		WorktreePath: o.worktreePathFor(task),
		Effort:       o.plannerEffort(),
		TargetCoder:  o.targetCoderFor(),
		Comments:     o.commentsForTask(task),
		Prompt:       plannerPrompt,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("dispatchPlanHTTP: marshal request: %w", err)
	}

	dispatchCtx, cancel := context.WithTimeout(ctx, plannerDispatchTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(dispatchCtx, http.MethodPost, o.plannerContainerURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("dispatchPlanHTTP: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.plannerContainerToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.plannerContainerToken)
	}

	client := &http.Client{Timeout: plannerDispatchTimeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("dispatchPlanHTTP: POST %s: %w", o.plannerContainerURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("dispatchPlanHTTP: read response: %w", err)
	}

	// Map HTTP status to FailureReason buckets per plans/warm-planner-pivot.md §5.
	switch resp.StatusCode {
	case http.StatusOK:
		// Success path: parse + validate the returned plan.
	case http.StatusConflict:
		return &PlanResult{Success: false, FailureReason: "plan_validation_failed"}, nil
	case http.StatusBadGateway:
		return &PlanResult{Success: false, FailureReason: "anthropic_upstream"}, nil
	case http.StatusGatewayTimeout:
		return &PlanResult{Success: false, FailureReason: "timeout"}, nil
	case http.StatusServiceUnavailable:
		return &PlanResult{Success: false, FailureReason: "planner_unhealthy"}, nil
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("dispatchPlanHTTP: planner rejected bearer token (401); check DREM_WARM_AGENT_TOKEN_FILE alignment")
	default:
		return &PlanResult{
			Success:       false,
			FailureReason: fmt.Sprintf("planner_http_%d", resp.StatusCode),
		}, nil
	}

	var parsed planContainerResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("dispatchPlanHTTP: decode response: %w (body=%s)", err, truncateBody(respBody, 500))
	}

	// validatePlan is already enforced server-side (planner returns 409 on
	// malformed shape), but re-validate here so a misbehaving planner
	// doesn't poison the task's Plan field.
	if err := validatePlanJSON(parsed.Plan); err != nil {
		return &PlanResult{
			Success:       false,
			FailureReason: "plan_validation_failed",
		}, nil
	}

	return &PlanResult{
		Success:    true,
		Plan:       parsed.Plan,
		TokensIn:   parsed.TokensIn,
		TokensOut:  parsed.TokensOut,
		DurationMS: parsed.DurationMS,
	}, nil
}

// probePlannerHealthz issues a GET to the planner's /healthz endpoint and
// returns nil iff the response is 200. Derives the healthz URL from the
// configured /plan URL by swapping the path — both live on the same host.
func (o *Orchestrator) probePlannerHealthz(ctx context.Context) error {
	healthURL, err := derivePlannerHealthURL(o.plannerContainerURL)
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, plannerHealthzTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, healthURL, nil)
	if err != nil {
		return fmt.Errorf("build healthz request: %w", err)
	}
	client := &http.Client{Timeout: plannerHealthzTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("healthz GET: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %d", resp.StatusCode)
	}
	return nil
}

// derivePlannerHealthURL trims a trailing /plan off the dispatch URL and
// appends /healthz. The planner's /healthz and /plan routes sit on the
// same mux.
func derivePlannerHealthURL(planURL string) (string, error) {
	if planURL == "" {
		return "", errors.New("planner URL is empty")
	}
	if strings.HasSuffix(planURL, "/plan") {
		return strings.TrimSuffix(planURL, "/plan") + "/healthz", nil
	}
	// Fallback: append /healthz verbatim.
	return strings.TrimRight(planURL, "/") + "/healthz", nil
}

// ---------------------------------------------------------------------------
// JSON shapes — mirror cmd/drem-planner/server.go
// ---------------------------------------------------------------------------

// planContainerRequest is the orch-side view of the planner's POST /plan
// body. Kept local so the orchestrator doesn't import cmd/drem-planner.
type planContainerRequest struct {
	TaskID       string                   `json:"task_id"`
	Task         map[string]any           `json:"task"`
	Project      map[string]any           `json:"project"`
	WorktreePath string                   `json:"worktree_path"`
	Comments     []any                    `json:"comments,omitempty"`
	TargetCoder  planContainerTargetCoder `json:"target_coder,omitempty"`
	Effort       string                   `json:"effort,omitempty"`
	// Prompt is a best-effort pre-rendered planner prompt. The planner
	// server composes its own prompt from the other fields; Prompt is
	// retained for forward-compatibility with agents that want to inject
	// additional context. The current planner server ignores it.
	Prompt string `json:"prompt,omitempty"`
}

type planContainerTargetCoder struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// planContainerResponse is the orch-side view of the planner's 200 body.
type planContainerResponse struct {
	TaskID     string          `json:"task_id"`
	Plan       model.JSONField `json:"plan"`
	TokensIn   int             `json:"tokens_in"`
	TokensOut  int             `json:"tokens_out"`
	DurationMS int             `json:"duration_ms"`
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// shouldDispatchPlanHTTP reports whether processPlanning should route the
// plan request through the warm drem-planner HTTP endpoint rather than the
// legacy runner.SpawnAgent path. True iff a planner URL is configured AND
// the planner's resolved provider is a CLI-backed planner handled by the
// warm service.
func (o *Orchestrator) shouldDispatchPlanHTTP() bool {
	if o.plannerContainerURL == "" {
		return false
	}
	if o.runner == nil {
		return false
	}
	cfg := o.runner.AgentConfig(model.AgentPlanner)
	switch cfg.EffectiveProvider() {
	case model.ProviderClaude, model.ProviderCodex:
		return true
	default:
		return false
	}
}

// spawnPlannerHTTPAsync starts one warm planner request for task and returns
// immediately. Planner latency can be several minutes; doing this work inline
// used to stall the single lifecycle tick, preventing unrelated tasks from
// classifying, dispatching, or completing while Codex planned one task.
//
// The in-memory claim suppresses duplicate requests on subsequent ticks. It is
// intentionally process-local: after a restart the old HTTP request no longer
// exists, so the new process may safely claim and retry the still-planning task.
// spawnPlannerHTTP retains the durable state-version claim and conditional
// result write, so cancellation/review actions cannot be overwritten by a late
// response.
func (o *Orchestrator) spawnPlannerHTTPAsync(task *model.Task, project *model.Project, plannerPrompt string) {
	key := task.ID.String()
	if _, loaded := o.plannerHTTPInflight.LoadOrStore(key, struct{}{}); loaded {
		return
	}

	// Work on copies because the caller's task/project usually point into a
	// per-tick query slice whose elements are reused after this method returns.
	taskCopy := *task
	projectCopy := *project
	go func() {
		defer o.plannerHTTPInflight.Delete(key)
		if err := o.spawnPlannerHTTP(&taskCopy, &projectCopy, plannerPrompt); err != nil {
			o.logger.Error("planner http: async dispatch", "task_id", taskCopy.ID, "error", err)
			return
		}
		o.logger.Info("planner http: dispatch completed", "task_id", taskCopy.ID)
	}()

	o.logger.Info("planner http: dispatch started", "task_id", task.ID)
}

// spawnPlannerHTTP drives the HTTP path. Called by processPlanning when
// shouldDispatchPlanHTTP is true. On success writes the returned plan onto
// task.Plan and clears any stale agent assignment so the "plan already
// exists" branch at the top of processPlanning advances the task on the
// next tick. Validation / upstream failures do NOT return a Go error —
// they increment the planner-spawn counter and log the reason; the tick
// loop retries until MaxTotalPlannerSpawns is exhausted.
//
// Returns a non-nil error only for genuinely fatal conditions: a mis-
// configured URL, an HTTP transport failure, or an auth mismatch (401).
func (o *Orchestrator) spawnPlannerHTTP(task *model.Task, project *model.Project, plannerPrompt string) error {
	dispatchVersion := task.StateVersion
	// Increment the total-planner-spawns counter BEFORE dispatch so
	// validation / upstream failures still count against the budget.
	totalSpawns := 0
	if task.Context != nil {
		if v, ok := task.Context["total_planner_spawns"].(float64); ok {
			totalSpawns = int(v)
		}
	}
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["total_planner_spawns"] = float64(totalSpawns + 1)
	claimed := o.db.Model(&model.Task{}).
		Where("id = ? AND status = ? AND state_version = ?", task.ID, model.StatusPlanning, dispatchVersion).
		Update("context", task.Context)
	if claimed.Error != nil {
		return fmt.Errorf("spawnPlannerHTTP: persist dispatch claim: %w", claimed.Error)
	}
	if claimed.RowsAffected == 0 {
		o.logger.Info("planner http: dispatch skipped after task changed", "task_id", task.ID, "state_version", dispatchVersion)
		return nil
	}

	ctx := context.Background()
	res, err := o.dispatchPlanHTTP(ctx, task, project, plannerPrompt)
	if err != nil {
		return fmt.Errorf("spawnPlannerHTTP: %w", err)
	}
	var current model.Task
	if err := o.db.Select("id", "status", "state_version").First(&current, "id = ?", task.ID).Error; err != nil {
		return fmt.Errorf("spawnPlannerHTTP: reload dispatch state: %w", err)
	}
	if current.Status != model.StatusPlanning || current.StateVersion != dispatchVersion {
		o.logger.Info("planner http: result discarded after task changed",
			"task_id", task.ID, "dispatch_version", dispatchVersion,
			"current_version", current.StateVersion, "current_status", current.Status)
		return nil
	}

	if res.Success {
		validation, err := o.validateWarmPlan(res.Plan, task)
		if err != nil {
			return fmt.Errorf("spawnPlannerHTTP: validate returned plan: %w", err)
		}
		if !validation.Valid {
			if task.Context == nil {
				task.Context = make(model.JSONField)
			}
			task.Context["plan_validation"] = map[string]any{
				"valid": false, "warnings": validation.Warnings, "errors": validation.Errors,
			}
			res.Success = false
			res.FailureReason = "plan_validation_failed: " + strings.Join(validation.Errors, "; ")
		}
	}

	if res.Success {
		task.Plan = res.Plan
		task.AssignedAgentID = nil
		o.emit("planner_http_success", map[string]any{
			"task_id":     task.ID,
			"tokens_in":   res.TokensIn,
			"tokens_out":  res.TokensOut,
			"duration_ms": res.DurationMS,
		})
		o.logger.Info("planner http: plan received",
			"task_id", task.ID,
			"tokens_in", res.TokensIn,
			"tokens_out", res.TokensOut,
			"duration_ms", res.DurationMS,
		)
	} else {
		failureClass := normalizeFailureClass(res.FailureReason, res.FailureReason)
		budget := consumeRetryBudget(task, retryEdgeForTask(*task, string(model.AgentPlanner)), failureClass, res.FailureReason, time.Now())
		o.logger.Warn("planner http: dispatch did not produce a valid plan",
			"task_id", task.ID,
			"failure_reason", res.FailureReason,
		)
		o.emit("planner_http_failed", map[string]any{
			"task_id":        task.ID,
			"failure_reason": res.FailureReason,
		})
		if budget.Exhausted {
			return o.failTaskWithFailureEvidence(task,
				fmt.Sprintf("planner retry budget exhausted for %s after %d failure(s)", failureClass, budget.Attempts),
				failureClass, res.FailureReason, budget.LastAt, budget)
		}
	}

	updates := map[string]any{"context": task.Context}
	if res.Success {
		updates["plan"] = task.Plan
		updates["assigned_agent_id"] = nil
	}
	saved := o.db.Model(&model.Task{}).
		Where("id = ? AND status = ? AND state_version = ?", task.ID, model.StatusPlanning, dispatchVersion).
		Updates(updates)
	if saved.Error != nil {
		return fmt.Errorf("spawnPlannerHTTP: save task: %w", saved.Error)
	}
	if saved.RowsAffected == 0 {
		o.logger.Info("planner http: result discarded during conditional save", "task_id", task.ID, "state_version", dispatchVersion)
	}
	return nil
}

// validateWarmPlan applies the same structural and repository-path checks to
// inline HTTP planner results that agent-worktree planner results receive.
// This prevents the low-latency warm path from bypassing deterministic gates.
func (o *Orchestrator) validateWarmPlan(plan model.JSONField, task *model.Task) (PlanValidationResult, error) {
	parsed, err := parsePlan(plan)
	if err != nil {
		return PlanValidationResult{}, err
	}
	result := ValidatePlan(parsed.Subtasks, parsed.TDDExceptions)
	fileResult := ValidatePlanFileExistence(parsed.Subtasks, o.worktreePathFor(task))
	result.Warnings = append(result.Warnings, fileResult.Warnings...)
	result.Errors = append(result.Errors, fileResult.Errors...)
	result.Valid = len(result.Errors) == 0
	return result, nil
}

// plannerPromptFor produces repository-aware context for the warm planner.
// It includes the repo map, local instructions, constraints, and build/test
// commands that are unavailable inside the global planner container.
func (o *Orchestrator) plannerPromptFor(task *model.Task, project *model.Project) string {
	featureName := strings.TrimPrefix(task.WorktreeBranch, "feature/")
	featureDir := o.worktree.FeatureWorktreePath(featureName)
	comments, _ := o.GetComments(task.ID)
	var targetProvider, targetModel string
	if o.runner != nil {
		coderCfg := o.runner.AgentConfig(model.AgentCoder)
		targetProvider = string(coderCfg.EffectiveProvider())
		targetModel = coderCfg.Model
	}
	return prompt.Generate(prompt.Opts{
		Task:                task,
		Project:             project,
		AgentType:           model.AgentPlanner,
		WorktreePath:        featureDir,
		Comments:            comments,
		TargetCoderProvider: targetProvider,
		TargetCoderModel:    targetModel,
	})
}

// worktreePathFor returns the absolute feature-worktree path for the task,
// or empty when the task has no branch yet. processPlanning creates the
// branch before calling us so this should always be non-empty.
func (o *Orchestrator) worktreePathFor(task *model.Task) string {
	if task.WorktreeBranch == "" || o.worktree == nil {
		return ""
	}
	featureName := strings.TrimPrefix(task.WorktreeBranch, "feature/")
	return o.worktree.FeatureWorktreePath(featureName)
}

// plannerEffort returns the [agents.planner].effort TOML knob, falling
// back to "high" when unset.
func (o *Orchestrator) plannerEffort() string {
	if o.runner == nil {
		return "high"
	}
	cfg := o.runner.AgentConfig(model.AgentPlanner)
	if cfg.Effort == "" {
		return "high"
	}
	return cfg.Effort
}

// targetCoderFor resolves the downstream coder's provider + model so the
// planner can adjust plan granularity (sglang-direct needs more detail
// than claude).
func (o *Orchestrator) targetCoderFor() planContainerTargetCoder {
	if o.runner == nil {
		return planContainerTargetCoder{}
	}
	cfg := o.runner.AgentConfig(model.AgentCoder)
	return planContainerTargetCoder{
		Provider: string(cfg.EffectiveProvider()),
		Model:    cfg.Model,
	}
}

// taskToJSON marshals a Task through JSON so map[string]any consumers
// (the planner request body) get the same shape the DB-facing code sees.
// Keeps the planner decoupled from the model.Task struct evolution.
func taskToJSON(t *model.Task) map[string]any {
	if t == nil {
		return nil
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// projectToJSON is the Project-side mirror of taskToJSON.
func projectToJSON(p *model.Project) map[string]any {
	if p == nil {
		return nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// commentsForTask returns the comments attached to the task as a plain
// []any for inclusion in the planner request body. Shape-preserved via
// JSON round-trip.
func (o *Orchestrator) commentsForTask(task *model.Task) []any {
	if o == nil || task == nil {
		return nil
	}
	comments, err := o.GetComments(task.ID)
	if err != nil || len(comments) == 0 {
		return nil
	}
	raw, err := json.Marshal(comments)
	if err != nil {
		return nil
	}
	var out []any
	_ = json.Unmarshal(raw, &out)
	return out
}

// truncateBody bounds a response body in error messages.
func truncateBody(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...<truncated>"
}

// ---------------------------------------------------------------------------
// Plan validation — migrated from the deleted plan_dispatch.go
// ---------------------------------------------------------------------------

// validatePlanJSON applies the validation rules from
// plans/warm-planner-pivot.md §6: subtasks non-empty, every tests_for /
// dependencies index valid. Duplicated into the orch side so a
// misbehaving planner can't poison the task's Plan field with an invalid
// shape.
func validatePlanJSON(parsed model.JSONField) error {
	rawSubtasks, ok := parsed["subtasks"]
	if !ok {
		return errors.New("plan.json missing subtasks")
	}
	subtasksList, ok := rawSubtasks.([]any)
	if !ok {
		return errors.New("plan.json subtasks is not an array")
	}
	if len(subtasksList) == 0 {
		return errors.New("plan.json subtasks is empty")
	}
	n := len(subtasksList)
	for i, raw := range subtasksList {
		m, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("subtask %d is not an object", i)
		}
		if err := validateIndexList(m, "tests_for", n, i); err != nil {
			return err
		}
		if err := validateIndexList(m, "dependencies", n, i); err != nil {
			return err
		}
	}
	return nil
}

// validateIndexList is the per-field helper migrated verbatim from
// plan_dispatch.go.
func validateIndexList(m map[string]any, field string, n, subtaskIdx int) error {
	raw, ok := m[field]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("subtask %d field %q is not an array", subtaskIdx, field)
	}
	for _, v := range list {
		var idx int
		switch val := v.(type) {
		case float64:
			idx = int(val)
		case int:
			idx = val
		default:
			return fmt.Errorf("subtask %d field %q has non-integer element", subtaskIdx, field)
		}
		if idx < 0 || idx >= n {
			return fmt.Errorf("subtask %d field %q index %d out of range [0,%d)", subtaskIdx, field, idx, n)
		}
	}
	return nil
}
