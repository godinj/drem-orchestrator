package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
	"github.com/godinj/drem-orchestrator/internal/spawner"
)

// PlannerSpawnConfig carries the per-call planner knobs that dispatchPlan
// propagates into the spawned container. Keeping them in a small struct
// rather than a long argument list makes the Orchestrator call sites more
// readable and leaves room for future fields (thinking budget, context
// window override, etc.) without churning every caller.
type PlannerSpawnConfig struct {
	Model  string
	Effort string
	APIKey string
}

// PlanResult is the public shape dispatchPlan returns. Mirrors MergeResult
// in merge_dispatch.go so downstream handling in processPlanning can branch
// on Success / FailureReason in the same way.
type PlanResult struct {
	Success       bool
	ExitCode      int
	ContainerID   string
	FailureReason string
	// Plan is the parsed plan.json (subtasks + tdd_exceptions + assumptions)
	// when Success is true. Nil otherwise. Stored as a JSONField (map
	// keyed by string) so callers can pass it straight to task.Plan.
	Plan model.JSONField
}

// planDispatchTimeout bounds how long the orchestrator waits for a planner
// container to finish. Opus planning against a non-trivial repo typically
// runs 60-180s; 10 minutes leaves plenty of head-room before SIGTERM.
const planDispatchTimeout = 10 * time.Minute

// plannerPollInterval mirrors mergerPollInterval. Event-driven waiting
// would be ideal but the event channel is consumed by watchDockerEvents.
const plannerPollInterval = 2 * time.Second

// failureReasonForPlanExit maps a planner-container exit code to the typed
// reason string per plans/warm-direct-planner.md §7. Exit 0 combined with
// a missing or malformed plan.json is handled in dispatchPlan directly;
// this only covers non-zero codes.
func failureReasonForPlanExit(code int) string {
	switch code {
	case 0:
		return ""
	case 1:
		return "cli_error"
	case 2:
		return "precondition_failed"
	case 124, 137:
		return "timeout"
	default:
		return "unknown"
	}
}

// dispatchPlan spawns a drem-planner container to run the claude CLI
// against the feature worktree and waits for it to exit. The spawned
// container reads the orchestrator-rendered prompt from --prompt-file,
// writes a plan.json to the worktree root, and exits with the CLI's
// exit code. dispatchPlan then parses plan.json, validates it against
// the rules in plans/warm-direct-planner.md §6, and returns a
// PlanResult. Every outcome — spawn error, CLI non-zero, missing
// plan.json, malformed JSON, validation failure — is surfaced through
// PlanResult.FailureReason so processPlanning can branch without
// swallowing errors.
//
// Fails closed when ANTHROPIC_API_KEY is empty: planner containers
// without credentials crash-loop on claude auth failures and burn
// through the planner-spawn budget for nothing.
func (o *Orchestrator) dispatchPlan(ctx context.Context, task *model.Task, prompt string, cfg PlannerSpawnConfig) (*PlanResult, error) {
	if o.Spawner == nil {
		return nil, fmt.Errorf("dispatchPlan: no Spawner configured")
	}
	if task.WorktreeBranch == "" {
		return nil, fmt.Errorf("dispatchPlan: task %s has no WorktreeBranch", task.ID)
	}
	if cfg.APIKey == "" {
		return nil, errors.New("dispatchPlan: ANTHROPIC_API_KEY is empty; refusing to spawn planner")
	}

	// Resolve the feature worktree directory; the orch writes the prompt
	// file there so the container's bind-mount (/work) picks it up.
	featureName := strings.TrimPrefix(task.WorktreeBranch, "feature/")
	featureDir := o.worktree.FeatureWorktreePath(featureName)
	if featureDir == "" {
		return nil, fmt.Errorf("dispatchPlan: feature worktree path is empty for %q", featureName)
	}

	// Drop the prompt into the feature worktree under a stable,
	// planner-specific name. Keeping it at the worktree root means the
	// container's /work bind-mount sees it without a separate mount.
	promptPath := filepath.Join(featureDir, ".drem-planner-prompt.txt")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return nil, fmt.Errorf("dispatchPlan: write prompt file: %w", err)
	}

	workerID := fmt.Sprintf("planner-%s-%s", task.ID.String()[:shortIDLen], uuid.New().String()[:shortIDLen])
	bareRepo := ""
	if o.worktree != nil {
		bareRepo = o.worktree.BareRepo()
	}

	argv := buildPlannerArgv(task, promptPath, cfg)

	params := spawner.SpawnWorkerParams{
		Project:   o.projectID.String(),
		AgentType: "planner",
		WorkerID:  workerID,
		Branch:    task.WorktreeBranch,
		Env: map[string]string{
			"ANTHROPIC_API_KEY": cfg.APIKey,
			"DREM_TASK_ID":      task.ID.String(),
		},
		Labels: map[string]string{
			"drem.task_id":  task.ID.String(),
			"drem.role":     "planner",
			"drem.language": o.resolveProjectLanguage(),
		},
		BareRepoMount: bareRepo,
		// Planner only reads from /bare (git clone). Leaving this false
		// keeps the hardened read-only default — unlike merger which
		// must push back.
		BareRepoReadWrite: false,
		Cmd:               argv,
	}

	dispatchCtx, cancel := context.WithTimeout(ctx, planDispatchTimeout)
	defer cancel()

	res, err := o.Spawner.SpawnWorker(dispatchCtx, params)
	if err != nil {
		return nil, fmt.Errorf("dispatchPlan: spawn planner: %w", err)
	}
	o.recordSpawnEvent(task, "planner", res.ContainerID, params.Image)

	finalState, err := o.awaitPlannerExit(dispatchCtx, res.ContainerID)
	if err != nil {
		return nil, fmt.Errorf("dispatchPlan: await planner exit: %w", err)
	}

	result := &PlanResult{
		ContainerID: res.ContainerID,
		ExitCode:    finalState.ExitCode,
	}

	// Non-zero exit always maps to a typed failure reason. Don't even
	// look at plan.json — the container signalled failure and we trust
	// the signal over whatever happens to be on disk.
	if finalState.ExitCode != 0 {
		result.FailureReason = failureReasonForPlanExit(finalState.ExitCode)
		return result, nil
	}

	// Exit 0 — read and validate plan.json. The planner entrypoint
	// already guards against exit 0 with no plan.json, but double-check
	// on the orch side because a bind-mount or rename race could still
	// leave it missing when the orch reads it back.
	planPath := filepath.Join(featureDir, "plan.json")
	data, err := os.ReadFile(planPath)
	if err != nil {
		result.FailureReason = "missing_plan_file"
		return result, nil
	}
	var parsed model.JSONField
	if jerr := json.Unmarshal(data, &parsed); jerr != nil {
		result.FailureReason = "plan_parse_error"
		return result, nil
	}
	if verr := validatePlanJSON(parsed); verr != nil {
		result.FailureReason = "plan_validation_failed"
		return result, nil
	}

	result.Success = true
	result.Plan = parsed
	return result, nil
}

// buildPlannerArgv composes the argv slice passed as container.Spec.Cmd
// to the drem-planner image. The entrypoint's flag parser (see
// deploy/docker/context/planner-entrypoint.sh) requires --task-id,
// --branch, --prompt-file, and --model; --effort is optional.
func buildPlannerArgv(task *model.Task, promptPath string, cfg PlannerSpawnConfig) []string {
	argv := []string{
		"--task-id", task.ID.String(),
		"--branch", task.WorktreeBranch,
		"--prompt-file", promptPath,
		"--model", cfg.Model,
	}
	if cfg.Effort != "" {
		argv = append(argv, "--effort", cfg.Effort)
	}
	return argv
}

// awaitPlannerExit polls Inspect until the planner container transitions
// to exited or dead. Same shape as awaitMergerExit.
func (o *Orchestrator) awaitPlannerExit(ctx context.Context, containerID string) (spawner.InspectWorkerResult, error) {
	ticker := time.NewTicker(plannerPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return spawner.InspectWorkerResult{}, ctx.Err()
		case <-ticker.C:
			res, err := o.Spawner.InspectWorker(ctx, spawner.InspectWorkerParams{ContainerID: containerID})
			if err != nil {
				o.logger.Debug("await planner exit: inspect error",
					"container_id", containerID, "error", err)
				continue
			}
			if res.Status == string(spawnerStatusExited) || res.Status == "dead" {
				return res, nil
			}
		}
	}
}

// validatePlanJSON applies the validation rules from
// plans/warm-direct-planner.md §6: subtasks non-empty, every tests_for
// / dependencies index valid, and the TDD pairing rule per
// internal/prompt/prompt_planner.go:63-66 (each implementation subtask
// has exactly one test subtask). Returns nil on success.
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

	// Walk each subtask validating tests_for / dependencies indices.
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

// shouldSpawnPlannerContainer reports whether processPlanning should route
// the planner spawn through the container / spawner path (dispatchPlan)
// rather than the legacy runner.SpawnAgent path. The container path
// applies iff a spawner is configured AND the planner's resolved
// provider is Anthropic's claude — the planner image ships the claude
// CLI, so any other provider (most notably sglang-direct) falls back
// to the legacy runner for rollback safety.
func (o *Orchestrator) shouldSpawnPlannerContainer() bool {
	if o.Spawner == nil {
		return false
	}
	if o.runner == nil {
		return false
	}
	cfg := o.runner.AgentConfig(model.AgentPlanner)
	return cfg.EffectiveProvider() == model.ProviderClaude
}

// spawnPlannerContainer drives the container path for processPlanning.
// It resolves the planner's model / effort from the runner config,
// reads ANTHROPIC_API_KEY from the orch env, invokes dispatchPlan,
// and on success writes the parsed plan onto the task so the next
// processPlanning tick's "plan already exists" branch advances the
// task. Validation and exit-code failures do NOT return a Go error —
// they increment the planner-spawn counter and return nil so the
// tick loop retries until the MaxTotalPlannerSpawns budget is
// exhausted.
//
// Returns a non-nil error only for genuinely fatal conditions:
// missing ANTHROPIC_API_KEY (orch is misconfigured; retrying won't
// help) or a spawner RPC error (infrastructure failure).
func (o *Orchestrator) spawnPlannerContainer(task *model.Task, plannerPrompt string) error {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return errors.New("spawnPlannerContainer: ANTHROPIC_API_KEY unset in orch env; refusing to spawn")
	}

	plannerCfg := o.runner.AgentConfig(model.AgentPlanner)
	cfg := PlannerSpawnConfig{
		Model:  plannerCfg.Model,
		Effort: plannerCfg.Effort,
		APIKey: apiKey,
	}

	// Increment the total-planner-spawns counter BEFORE dispatchPlan so
	// validation / exit-code failures still count against the budget.
	// processPlanning reads this counter on its next tick to enforce
	// MaxTotalPlannerSpawns.
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

	ctx := context.Background()
	res, err := o.dispatchPlan(ctx, task, plannerPrompt, cfg)
	if err != nil {
		// Infrastructure failure (spawner RPC, missing bare repo, etc.).
		// Still persist the incremented counter so retries don't loop
		// forever on misconfiguration.
		_ = o.db.Save(task).Error
		return fmt.Errorf("spawnPlannerContainer: dispatch: %w", err)
	}

	if res.Success {
		task.Plan = res.Plan
		// Clear any stale agent assignment so the top of processPlanning
		// doesn't try to reconcile a nonexistent Agent row on the next
		// tick — the container path doesn't go through runner.SpawnAgent.
		task.AssignedAgentID = nil
	} else {
		// Surface the failure reason for debugging; the tick loop will
		// retry on the next pass, counting against MaxTotalPlannerSpawns.
		o.logger.Warn("planner container did not produce a valid plan",
			"task_id", task.ID, "exit_code", res.ExitCode,
			"failure_reason", res.FailureReason)
		o.emit("planner_container_failed", map[string]any{
			"task_id":        task.ID,
			"exit_code":      res.ExitCode,
			"failure_reason": res.FailureReason,
		})
	}

	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("spawnPlannerContainer: save task: %w", err)
	}
	return nil
}

// plannerPromptFor produces the planner prompt text the container path
// feeds to the claude CLI. Extracted into a helper so spawnPlannerContainer
// can call it without re-deriving the featureDir / comments /
// targetCoder* fields that processPlanning also needs.
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

// validateIndexList enforces that every integer in a subtask's index
// list field (tests_for / dependencies) points at a real subtask. JSON
// decoding of integers into interface{} produces float64; accept both
// just in case a future caller feeds a typed input.
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
