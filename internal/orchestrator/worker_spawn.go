package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/workeridentity"
)

// WorkerSpawner is the narrow surface the orchestrator consumes from the
// spawner package. Defined at the consumption site (per architecture rule)
// so tests can provide a fake without importing the spawner RPC wiring.
// This mirrors spawner.Client but as an interface for injectability.
type WorkerSpawner interface {
	SpawnWorker(ctx context.Context, p spawner.SpawnWorkerParams) (spawner.SpawnWorkerResult, error)
	DestroyWorker(ctx context.Context, p spawner.DestroyWorkerParams) error
	ListWorkers(ctx context.Context, p spawner.ListWorkersParams) (spawner.ListWorkersResult, error)
	InspectWorker(ctx context.Context, p spawner.InspectWorkerParams) (spawner.InspectWorkerResult, error)
}

// spawnWorkerContext captures the minimal data required by spawnCoder et al.
// It is populated from the task + project and returned so the orchestrator
// can record the resulting container ID on the right DB row. project is
// the human-readable name (maps to drem.project + DREM_PROJECT env);
// projectID is the stable UUID (maps to drem.project_id). See
// plans/dual-label-worker-spawn.md.
type spawnWorkerContext struct {
	project             string
	projectID           string
	agentType           string
	workerID            string
	branch              string
	image               string
	language            string
	bareRepo            string
	credsMount          string
	codexAuth           string
	promptMount         string
	promptHash          string
	promptAssetVersions string
	provider            string
	modelID             string
	effort              string
	envVars             map[string]string
	extraLabel          map[string]string
}

// spawnCoder dispatches a coder worker for a task via the configured
// WorkerSpawner. It builds SpawnWorkerParams from the task, records the
// resulting container ID on the assigned Agent row, registers the branch
// in the gitref Registry (user stories 10 and 11), and emits an events-table
// audit row (user story 49: every spawn produces a "what happened when"
// record). Returns an error without mutating task state if the spawner
// rejects the request.
func (o *Orchestrator) spawnCoder(ctx context.Context, task *model.Task) error {
	return o.spawnTypedWorker(ctx, task, string(model.AgentCoder))
}

// spawnReviewer dispatches a reviewer worker. Mirrors spawnCoder.
func (o *Orchestrator) spawnReviewer(ctx context.Context, task *model.Task) error {
	return o.spawnTypedWorker(ctx, task, string(model.AgentReviewer))
}

// spawnFixer dispatches a fixer worker. Mirrors spawnCoder.
func (o *Orchestrator) spawnFixer(ctx context.Context, task *model.Task) error {
	return o.spawnTypedWorker(ctx, task, string(model.AgentFixer))
}

// spawnSupervisor dispatches a supervisor worker. Used for supervisor
// sessions that run inside a container rather than an interactive tmux shell.
func (o *Orchestrator) spawnSupervisor(ctx context.Context, task *model.Task) error {
	return o.spawnTypedWorker(ctx, task, "supervisor")
}

// spawnTypedWorker is the common back-end for spawnCoder, spawnReviewer,
// spawnFixer, and spawnSupervisor. It requires o.Spawner to be configured
// — callers must verify this upstream (nil check) before calling.
func (o *Orchestrator) spawnTypedWorker(ctx context.Context, task *model.Task, agentType string) error {
	if o.Spawner == nil {
		return fmt.Errorf("spawn %s: orchestrator has no WorkerSpawner configured", agentType)
	}

	swc, err := o.buildSpawnContext(task, agentType)
	if err != nil {
		// Prompt render/write failures are distinguishable from other
		// spawn-context errors by the "render prompt" / "prompt
		// delivery required" prefix; classify them so audit queries
		// can isolate prompt-pipeline problems without string parsing.
		// See plans/worker-prompt-delivery.md §7.
		msg := err.Error()
		if strings.Contains(msg, "render prompt") || strings.Contains(msg, "prompt delivery required") {
			o.recordSpawnFailureEventWithReason(task, agentType, spawnPolicyReasonPromptMissing, err)
		} else {
			o.recordSpawnFailureEvent(task, agentType, err)
		}
		return fmt.Errorf("spawn %s worker: %w", agentType, err)
	}
	// Branch pre-provision: the worker's entrypoint runs
	// `git clone --branch <DREM_BRANCH> /bare /home/drem/work`, which
	// requires refs/heads/<branch> to exist in the bare repo. The
	// pre-container runner.SpawnAgent path created branches as a side
	// effect of `git worktree add -b`; the container path has no such
	// side effect, so we create the branch here. See
	// plans/orch-container-subtask-branch-provisioning.md.
	if branchErr := o.ensureWorkerBranch(ctx, task, swc); branchErr != nil {
		reason := ""
		if errors.Is(branchErr, errSubtaskParentBranchMissing) {
			reason = spawnPolicyReasonBranchMissing
		}
		o.recordSpawnFailureEventWithReason(task, agentType, reason, branchErr)
		return fmt.Errorf("spawn %s worker: %w", agentType, branchErr)
	}
	// Policy check: reject an ANTHROPIC_API_KEY smuggled into the env
	// map (currently impossible via buildSpawnContext, but the boundary
	// check stops a future envVars extension from regressing the
	// subscription-only policy). See plans/worker-subscription-auth.md
	// §6 commit 5.
	if policyErr := rejectAPIKeyInEnv(swc.envVars); policyErr != nil {
		o.recordSpawnFailureEventWithReason(task, agentType, spawnPolicyReasonAPIKey, policyErr)
		return fmt.Errorf("spawn %s worker: %w", agentType, policyErr)
	}
	params := spawner.SpawnWorkerParams{
		Project:       swc.project,
		ProjectID:     swc.projectID,
		AgentType:     swc.agentType,
		WorkerID:      swc.workerID,
		Branch:        swc.branch,
		Image:         swc.image,
		Env:           swc.envVars,
		Labels:        swc.extraLabel,
		BareRepoMount: swc.bareRepo,
		// The worker's drem-watchdog commits and pushes the agent's
		// in-flight work to the feature branch in the bare repo on each
		// tick (PRD §Lifecycle and recovery, user stories 17/18). The
		// push target is /bare mounted inside the worker; without the
		// read-write flag the spawner defaults to a read-only mount and
		// `git push origin` fails with "remote unpack failed: unable to
		// create temporary object directory". Merger already sets this
		// explicitly in merge_dispatch.go; worker spawns need the same
		// so the watchdog can function.
		BareRepoReadWrite: true,
		CredsMount:        swc.credsMount,
		CodexAuthMount:    swc.codexAuth,
		PromptMount:       swc.promptMount,
	}

	store := workeridentity.NewStore(o.db)
	reservation, reserveErr := store.ReserveSpawn(ctx, workeridentity.SpawnRecord{
		Task:                    task,
		ProjectID:               o.projectID,
		AgentType:               agentType,
		WorkerID:                swc.workerID,
		Image:                   params.Image,
		Branch:                  params.Branch,
		Provider:                swc.provider,
		ModelID:                 swc.modelID,
		Effort:                  swc.effort,
		PromptAssetVersionsJSON: swc.promptAssetVersions,
		RenderedPromptHash:      swc.promptHash,
		RenderedPromptPath:      swc.promptMount,
	})
	if reserveErr != nil {
		reason := "worker_reservation_failed"
		if errors.Is(reserveErr, workeridentity.ErrTaskAlreadyClaimed) {
			reason = "worker_already_active"
		}
		o.recordSpawnFailureEventWithReason(task, agentType, reason, reserveErr)
		return fmt.Errorf("spawn %s worker: reserve identity: %w", agentType, reserveErr)
	}
	if params.Env != nil {
		params.Env["DREM_AGENT_ID"] = reservation.AgentID.String()
	}

	res, spawnErr := o.Spawner.SpawnWorker(ctx, params)
	if spawnErr != nil {
		if abortErr := store.AbortReservation(ctx, reservation, "spawn_failed"); abortErr != nil {
			o.logger.Error("spawn worker: abort reservation after spawn failure", "task_id", task.ID, "error", abortErr)
		}
		o.recordSpawnFailureEvent(task, agentType, spawnErr)
		return fmt.Errorf("spawn %s worker: %w", agentType, spawnErr)
	}

	// Finalize container ID and model identity on the agent row so the audit
	// trail in user story 49 has a single join from task → agent → container.
	//
	// params.Branch (not task.WorktreeBranch) is the canonical branch
	// the worker actually clones — buildSpawnContext already derives it
	// from the task and fills in the synthetic "feature/<taskID>" when
	// the task row was missing one. Passing it here keeps the agent
	// row's WorktreeBranch in lockstep with the container's
	// DREM_BRANCH env var and drem.branch label, so the reconciler's
	// commit-check finds the right ref regardless of which branch
	// source the spawner used.
	handle, finalizeErr := store.FinalizeSpawn(ctx, reservation, res.ContainerID)
	if finalizeErr != nil {
		destroyErr := o.Spawner.DestroyWorker(ctx, spawner.DestroyWorkerParams{ContainerID: res.ContainerID})
		if abortErr := store.AbortReservation(ctx, reservation, "finalize_failed"); abortErr != nil {
			o.logger.Error("spawn worker: abort reservation after finalize failure", "task_id", task.ID, "error", abortErr)
		}
		o.recordSpawnFailureEventWithReason(task, agentType, "identity_finalize_failed", finalizeErr)
		if destroyErr != nil {
			return fmt.Errorf("spawn %s worker: finalize identity: %w; destroy %s failed: %v", agentType, finalizeErr, res.ContainerID, destroyErr)
		}
		return fmt.Errorf("spawn %s worker: finalize identity: %w", agentType, finalizeErr)
	}

	// Register the branch in gitref so downstream reconciliation and cleanup
	// find the right ownership metadata.
	if o.GitrefRegistry != nil && swc.branch != "" && swc.bareRepo != "" {
		ref := &gitref.BranchRef{
			BareRepoPath: swc.bareRepo,
			Project:      swc.project,
			TaskID:       task.ID.String(),
			AgentType:    agentType,
			Branch:       swc.branch,
			Status:       gitref.StatusActive,
		}
		if regErr := o.GitrefRegistry.Register(ctx, ref); regErr != nil {
			// Already-claimed is informational; a different agent may own the
			// branch on a parallel spawn attempt. Log and continue — the
			// container has already been spawned.
			o.logger.Warn("spawn worker: register gitref",
				"task_id", task.ID, "branch", swc.branch, "error", regErr)
		}
	}

	o.recordSpawnEventWithWorkerID(task, agentType, res.ContainerID, params.Image, swc.workerID, handle.AttemptID)
	return nil
}

// buildSpawnContext derives the SpawnWorkerParams-shaped bundle from a task.
// Kept in one place so every spawn path uses consistent label/env/branch
// derivation. Returns an error when an agent type requires a
// subscription-auth creds mount but no host path is available — in that
// case spawning must fail-closed rather than produce an unauth'd worker.
func (o *Orchestrator) buildSpawnContext(task *model.Task, agentType string) (spawnWorkerContext, error) {
	// projectName is the human-readable label; projectID is the stable UUID.
	// Both are emitted as container labels (drem.project / drem.project_id)
	// so agentmon's name-based filter and every internal orch UUID filter
	// both match. See plans/dual-label-worker-spawn.md. projectName may be
	// empty in legacy test rigs that construct the Orchestrator directly;
	// the spawner validates Project non-empty so those tests will fail loudly.
	projectName := o.projectName
	projectID := o.projectID.String()
	branch := task.WorktreeBranch
	if branch == "" {
		branch = "feature/" + taskFeatureName(task)
	}
	workerID := fmt.Sprintf("%s-%s-%s", agentType, task.ID.String()[:shortIDLen], uuid.New().String()[:shortIDLen])

	env := map[string]string{
		"DREM_TASK_ID":   task.ID.String(),
		"DREM_PROJECT":   projectName,
		"DREM_AGENT":     agentType,
		"DREM_BRANCH":    branch,
		"DREM_WORKER_ID": workerID,
		// worker-entrypoint.sh's "Environment contract" (see
		// deploy/docker/context/worker-entrypoint.sh) requires
		// DREM_AGENT_ID for watchdog heartbeats (--agent-id). Before
		// this wiring the entrypoint died at startup with
		// "required env var DREM_AGENT_ID is unset", preventing any
		// container-mode worker from reaching the claude exec.
		// Using workerID gives the watchdog a stable per-spawn
		// identity that also matches orch's per-spawn accounting.
		"DREM_AGENT_ID": workerID,
	}

	cliConfig := model.AgentCLIConfig{}
	if o.runner != nil {
		if at, ok := workerAgentType(agentType); ok {
			cliConfig = o.runner.AgentConfig(at)
		}
	}
	provider := cliConfig.EffectiveProvider()
	switch provider {
	case model.ProviderOpenCode:
		env["DREM_AGENT_HARNESS"] = "opencode"
	case model.ProviderCodex:
		env["DREM_AGENT_HARNESS"] = "codex"
	case model.ProviderSGLangDirect:
		env["DREM_AGENT_HARNESS"] = "sglang-direct"
		if o.directToolAgentCfg != nil {
			env["DREM_DIRECT_ENDPOINT"] = o.directToolAgentCfg.Endpoint
			if o.directToolAgentCfg.MaxTokens > 0 {
				env["DREM_DIRECT_MAX_TOKENS"] = fmt.Sprintf("%d", o.directToolAgentCfg.MaxTokens)
			}
			if o.directToolAgentCfg.MaxIterations > 0 {
				env["DREM_DIRECT_MAX_ITERATIONS"] = fmt.Sprintf("%d", o.directToolAgentCfg.MaxIterations)
			}
			if o.directToolAgentCfg.Temperature >= 0 {
				env["DREM_DIRECT_TEMPERATURE"] = fmt.Sprintf("%g", o.directToolAgentCfg.Temperature)
			}
			if o.directToolAgentCfg.Timeout > 0 {
				env["DREM_DIRECT_TIMEOUT"] = o.directToolAgentCfg.Timeout.String()
			}
			if o.directToolAgentCfg.BashTimeout > 0 {
				env["DREM_DIRECT_BASH_TIMEOUT"] = o.directToolAgentCfg.BashTimeout.String()
			}
			if o.directToolAgentCfg.ContextLimit > 0 {
				env["DREM_DIRECT_CONTEXT_LIMIT"] = fmt.Sprintf("%d", o.directToolAgentCfg.ContextLimit)
			}
			if o.contextWarnPct > 0 {
				env["DREM_DIRECT_CONTEXT_WARN_PCT"] = fmt.Sprintf("%d", o.contextWarnPct)
			}
			if o.contextStopPct > 0 {
				env["DREM_DIRECT_CONTEXT_STOP_PCT"] = fmt.Sprintf("%d", o.contextStopPct)
			}
		}
	default:
		env["DREM_AGENT_HARNESS"] = "claude"
	}
	if cliConfig.Model != "" {
		env["DREM_MODEL"] = cliConfig.Model
	}
	if cliConfig.Effort != "" {
		env["DREM_EFFORT"] = cliConfig.Effort
	}

	labels := map[string]string{
		"drem.task_id": task.ID.String(),
	}
	if lang := o.resolveProjectLanguage(); lang != "" {
		labels["drem.language"] = lang
	}

	bareRepo := ""
	if o.worktree != nil {
		bareRepo = o.worktree.BareRepo()
	}

	// CredsMount is populated only for claude-backed roles. Fail-closed:
	// if the role requires it but no host path is available, return an
	// error so the caller emits a worker_spawn_failed event.
	credsMount := ""
	if provider == model.ProviderClaude && credsMountRequired(agentType) {
		credsMount = resolveWorkerCredsPath()
		if credsMount == "" {
			return spawnWorkerContext{}, fmt.Errorf(
				"subscription-auth required for agent_type=%q but %s is unset and $HOME is unresolvable",
				agentType, workerCredsPathEnv)
		}
	}
	codexAuth := ""
	if provider == model.ProviderCodex {
		codexAuth = resolveWorkerCodexAuthPath()
		if codexAuth == "" {
			return spawnWorkerContext{}, fmt.Errorf(
				"codex auth required for agent_type=%q but %s is unset and $HOME is unresolvable",
				agentType, workerCodexAuthPathEnv)
		}
	}

	// PromptMount is populated only for claude-backed roles (same
	// membership as credsMountRequired today; the tables are kept
	// separate so a future role can opt in/out of each independently).
	// The rendered prompt is written atomically to host; the spawner
	// bind-mounts the file read-only into the worker at
	// /home/drem/.drem/prompt.md and sets DREM_PROMPT_PATH there.
	// See plans/worker-prompt-delivery.md §§2, 4.
	promptMount := ""
	promptHash := ""
	promptAssetVersions := ""
	if promptRequired(agentType) {
		// resolveWorkerPromptRoot's fallback builds
		// $HOME/.drem/projects/<project>/prompts so the name is the
		// right handle (matches the per-project compose's host layout);
		// production sets DREM_PROMPT_ROOT_HOST explicitly so the arg
		// is only used for the local-dev fallback path.
		promptRoot := resolveWorkerPromptRoot(projectName)
		if promptRoot == "" {
			return spawnWorkerContext{}, fmt.Errorf(
				"prompt delivery required for agent_type=%q but %s is unset and $HOME is unresolvable",
				agentType, workerPromptRootEnv)
		}
		// Load the project row so prompt.Generate has a populated
		// Opts.Project (name, description, bare repo path all flow
		// into the rendered markdown). A failure to load is
		// non-fatal — we render with a nil project; prompt.Generate
		// guards every Project read.
		var proj *model.Project
		var row model.Project
		if err := o.db.First(&row, "id = ?", o.projectID).Error; err == nil {
			proj = &row
		}
		promptInfo, err := o.renderAndWritePrompt(task, proj, agentType, promptRoot)
		if err != nil {
			return spawnWorkerContext{}, fmt.Errorf(
				"render prompt for agent_type=%q task_id=%s: %w",
				agentType, task.ID, err)
		}
		promptMount = promptInfo.Path
		promptHash = promptInfo.Hash
		promptAssetVersions = promptInfo.AssetVersions
	}

	return spawnWorkerContext{
		project:             projectName,
		projectID:           projectID,
		agentType:           agentType,
		workerID:            workerID,
		branch:              branch,
		bareRepo:            bareRepo,
		credsMount:          credsMount,
		codexAuth:           codexAuth,
		promptMount:         promptMount,
		promptHash:          promptHash,
		promptAssetVersions: promptAssetVersions,
		provider:            string(provider),
		modelID:             cliConfig.Model,
		effort:              cliConfig.Effort,
		envVars:             env,
		extraLabel:          labels,
	}, nil
}

func workerAgentType(agentType string) (model.AgentType, bool) {
	switch agentType {
	case string(model.AgentCoder):
		return model.AgentCoder, true
	case string(model.AgentReviewer):
		return model.AgentReviewer, true
	case string(model.AgentFixer):
		return model.AgentFixer, true
	case string(model.AgentResearcher):
		return model.AgentResearcher, true
	case string(model.AgentPlanner):
		return model.AgentPlanner, true
	case string(model.AgentClassifier):
		return model.AgentClassifier, true
	case string(model.AgentPrep):
		return model.AgentPrep, true
	}
	return "", false
}

// resolveProjectLanguage returns the project language from the Project row,
// defaulting to "go" when the column is empty. The spawner requires this
// label for coder workers to pick the right image (coder-<lang>).
func (o *Orchestrator) resolveProjectLanguage() string {
	var proj model.Project
	if err := o.db.First(&proj, "id = ?", o.projectID).Error; err != nil {
		return "go"
	}
	if proj.Language == "" {
		return "go"
	}
	return proj.Language
}

// recordSpawnEvent writes a TaskEvent documenting the spawn so the audit
// trail required by user story 49 is always present, regardless of whether
// the spawn happened through the old worktree path or the new spawner RPC.
func (o *Orchestrator) recordSpawnEvent(task *model.Task, agentType, containerID, image string) {
	o.recordSpawnEventWithWorkerID(task, agentType, containerID, image, "", uuid.Nil)
}

func (o *Orchestrator) recordSpawnEventWithWorkerID(task *model.Task, agentType, containerID, image, workerID string, attemptID uuid.UUID) {
	detail := model.JSONField{
		"agent_type":   agentType,
		"container_id": containerID,
		"image":        image,
	}
	if attemptID != uuid.Nil {
		detail["attempt_id"] = attemptID.String()
	}
	if workerID != "" {
		detail["worker_id"] = workerID
	}
	if agentID := o.assignedAgentIDForType(task, agentType); agentID != nil {
		detail["agent_id"] = agentID.String()
	}
	evt := &model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    task.ID,
		EventType: "worker_spawned",
		OldValue:  "",
		NewValue:  agentType,
		Details:   detail,
		Actor:     "orchestrator",
		CreatedAt: time.Now(),
	}
	if err := o.db.Create(evt).Error; err != nil {
		o.logger.Error("record spawn event", "task_id", task.ID, "error", err)
	}
}

func (o *Orchestrator) assignedAgentIDForType(task *model.Task, agentType string) *uuid.UUID {
	if task == nil || task.AssignedAgentID == nil {
		return nil
	}
	var ag model.Agent
	if err := o.db.Select("id", "agent_type").First(&ag, "id = ?", *task.AssignedAgentID).Error; err != nil {
		o.logger.Warn("resolve assigned agent for attempt", "task_id", task.ID, "agent_id", *task.AssignedAgentID, "error", err)
		return nil
	}
	if string(ag.AgentType) != agentType {
		return nil
	}
	id := ag.ID
	return &id
}

// recordSpawnFailureEvent logs a spawn failure to the audit trail so a later
// reviewer can correlate missing container IDs with spawner errors.
func (o *Orchestrator) recordSpawnFailureEvent(task *model.Task, agentType string, err error) {
	o.recordSpawnFailureEventWithReason(task, agentType, "", err)
}

// recordSpawnFailureEventWithReason is the reason-carrying variant used
// by policy checks (e.g. the ANTHROPIC_API_KEY rejection) so the audit
// trail surfaces a machine-readable classification in addition to the
// free-form error string.
func (o *Orchestrator) recordSpawnFailureEventWithReason(task *model.Task, agentType, reason string, err error) {
	detail := model.JSONField{
		"agent_type": agentType,
		"error":      err.Error(),
	}
	if reason != "" {
		detail["reason"] = reason
	}
	evt := &model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    task.ID,
		EventType: "worker_spawn_failed",
		OldValue:  "",
		NewValue:  agentType,
		Details:   detail,
		Actor:     "orchestrator",
		CreatedAt: time.Now(),
	}
	_ = o.db.Create(evt).Error
}

// spawnPolicyReasonAPIKey is the classifier attached to
// worker_spawn_failed events emitted when a worker spawn is rejected
// because ANTHROPIC_API_KEY appeared in the Env map. Subscription-only
// policy forbids shipping API-key auth through the default spawn path.
const spawnPolicyReasonAPIKey = "policy_violation_api_key"

// spawnPolicyReasonBranchMissing is the classifier attached to
// worker_spawn_failed events emitted when a subtask is dispatched but
// its parent task carries no WorktreeBranch. The fix path is upstream
// (parent planning produced a gap); surfacing this as a distinct audit
// reason keeps the symptom out of the generic git-error bucket so the
// operator can find the planning miss. See
// plans/orch-container-subtask-branch-provisioning.md §2.2.
const spawnPolicyReasonBranchMissing = "branch_missing"

// errSubtaskParentBranchMissing is the sentinel returned by
// resolveBranchSource when a subtask's parent has no WorktreeBranch.
// Kept as a package-private sentinel (rather than matched on message
// substrings) so the spawnTypedWorker hook can classify the failure
// reason with errors.Is and the audit row picks up the
// spawnPolicyReasonBranchMissing classifier.
var errSubtaskParentBranchMissing = errors.New("subtask parent has empty WorktreeBranch")

// rejectAPIKeyInEnv returns a non-nil error when env carries an
// ANTHROPIC_API_KEY key. The check codifies the subscription-only auth
// policy at the orchestrator boundary — if a future change accidentally
// reintroduces API-key plumbing, spawns fail closed at commit-lint
// time rather than silently charging an unintended auth pool.
func rejectAPIKeyInEnv(env map[string]string) error {
	if _, found := env["ANTHROPIC_API_KEY"]; found {
		return fmt.Errorf(
			"policy violation: ANTHROPIC_API_KEY must not be set on a worker spawn; subscription auth is the only supported path")
	}
	return nil
}

// resolveBranchSource returns the branch that spawnTypedWorker should
// fork the worker's feature branch off of. The contract is explicit:
//
//   - For subtasks (task.ParentTaskID != nil), the source is the parent
//     task's WorktreeBranch. A subtask always branches off its parent's
//     integration branch so merges chain cleanly. An empty parent
//     WorktreeBranch is treated as a planning gap (not a condition to
//     paper over by silently using main) and surfaces via the sentinel
//     errSubtaskParentBranchMissing.
//   - For parent tasks (no ParentTaskID), the source is the bare repo's
//     default branch, read via gitref.DefaultBranch so the "main vs
//     master" ambiguity is resolved at the ref database rather than via
//     a string compare on the Project row.
func (o *Orchestrator) resolveBranchSource(ctx context.Context, task *model.Task, bareRepo string) (string, error) {
	if task.ParentTaskID == nil {
		return gitref.DefaultBranch(ctx, bareRepo)
	}

	var parent model.Task
	if err := o.db.First(&parent, "id = ?", task.ParentTaskID).Error; err != nil {
		return "", fmt.Errorf("resolveBranchSource: load parent %s: %w", task.ParentTaskID, err)
	}
	if strings.TrimSpace(parent.WorktreeBranch) == "" {
		return "", errSubtaskParentBranchMissing
	}
	return parent.WorktreeBranch, nil
}

// ensureWorkerBranch pre-creates the feature branch in the bare repo
// so the worker container's `git clone --branch <DREM_BRANCH>` step
// succeeds. Idempotent: a branch that already exists (e.g. because a
// previous worker pushed commits before dying) is left untouched —
// EnsureBranch refuses to force-reset the tip. Callers that encounter
// a branch_missing sentinel should surface spawnPolicyReasonBranchMissing
// on the audit row.
func (o *Orchestrator) ensureWorkerBranch(ctx context.Context, task *model.Task, swc spawnWorkerContext) error {
	if swc.bareRepo == "" || swc.branch == "" {
		// Nothing to ensure — tests and future agent types that spawn
		// without a branch/bare-repo fall through without a git call.
		// Downstream SpawnWorker will reject a container that actually
		// needs a clone; this helper's job is specifically branch-provision.
		return nil
	}

	source, err := o.resolveBranchSource(ctx, task, swc.bareRepo)
	if err != nil {
		return err
	}
	if err := gitref.EnsureBranch(ctx, swc.bareRepo, swc.branch, source); err != nil {
		return fmt.Errorf("ensureWorkerBranch: create %s from %s: %w", swc.branch, source, err)
	}
	return nil
}

// destroyWorkerForTask tears down the container associated with a task
// (via its assigned agent) and marks the branch deleted in gitref.
// Idempotent: missing container/agent returns nil.
func (o *Orchestrator) destroyWorkerForTask(ctx context.Context, task *model.Task) error {
	if o.Spawner == nil || task.AssignedAgentID == nil {
		return nil
	}
	var ag model.Agent
	if err := o.db.First(&ag, "id = ?", task.AssignedAgentID).Error; err != nil {
		return nil
	}
	handle := workeridentity.FromAgent(ag)
	if !handle.HasContainer() {
		return nil
	}
	if err := o.Spawner.DestroyWorker(ctx, spawner.DestroyWorkerParams{ContainerID: handle.ContainerID}); err != nil {
		return fmt.Errorf("destroy worker %s: %w", handle.ContainerID, err)
	}

	if o.GitrefRegistry != nil && o.worktree != nil && ag.WorktreeBranch != "" {
		ref, err := o.GitrefRegistry.FindByBranch(ctx, o.worktree.BareRepo(), ag.WorktreeBranch)
		if err == nil && ref != nil {
			_ = o.GitrefRegistry.MarkDeleted(ctx, ref.ID)
		}
	}
	return nil
}
