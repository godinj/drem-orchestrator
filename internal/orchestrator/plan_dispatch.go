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
