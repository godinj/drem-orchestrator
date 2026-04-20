package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
	"github.com/godinj/drem-orchestrator/internal/spawner"
)

// workerCredsPathEnv is the environment variable the per-project compose
// template passes to orch carrying the host path of the operator's
// claude subscription credentials file. Set by `drem project register`;
// consumed by buildSpawnContext for every claude-backed worker.
const workerCredsPathEnv = "DREM_WORKER_CREDS_PATH"

// workerPromptRootEnv is the environment variable the per-project
// compose template passes to orch carrying the host directory under
// which per-task prompt files are written. The spawner later bind-mounts
// the individual prompt file from that dir into the worker. See
// plans/worker-prompt-delivery.md §§2, 4.
const workerPromptRootEnv = "DREM_PROMPT_ROOT_HOST"

// spawnPolicyReasonPromptMissing classifies worker_spawn_failed
// events emitted when orch cannot resolve a host prompt root OR the
// prompt render/write step fails. Lets audit queries filter by reason
// without parsing the free-form error string. Companion to
// spawnPolicyReasonAPIKey defined below.
const spawnPolicyReasonPromptMissing = "prompt_render_failed"

// credsMountRequired reports whether the given agent_type runs the
// claude CLI harness inside the container and therefore needs the
// subscription credentials file bind-mounted. Roles not in this set
// (notably merger, a Go binary) get an empty CredsMount and the
// spawner omits the mount entry.
//
// The default for an unknown agent type is fail-closed (false): a
// future type has to be added here deliberately alongside a test
// covering it, so auth coverage cannot silently lag behind a new role.
// See plans/worker-subscription-auth.md §5.
func credsMountRequired(agentType string) bool {
	switch agentType {
	case string(model.AgentCoder),
		string(model.AgentReviewer),
		string(model.AgentFixer),
		// Tester and supervisor are not model.AgentType constants yet;
		// they are carried as string literals elsewhere in orch and
		// remain literals here until the enum catches up.
		"tester",
		"supervisor":
		return true
	case "merger":
		return false
	}
	return false
}

// resolveWorkerCredsPath returns the host path of the subscription
// credentials file the spawner should bind-mount into a claude-backed
// worker. It reads DREM_WORKER_CREDS_PATH first (set by `drem project
// register` from the operator's host $HOME) and falls back to
// $HOME/.claude/.credentials.json only if the env var is unset — the
// fallback is there for ad-hoc local invocations; production always
// sets the env var explicitly. Returns the empty string when neither
// source yields a path; caller is responsible for fail-closing.
func resolveWorkerCredsPath() string {
	if p := os.Getenv(workerCredsPathEnv); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", ".credentials.json")
}

// promptRequired reports whether the agent_type runs the claude CLI
// in a mode that needs a prompt file on disk. Mirrors
// credsMountRequired — every claude-backed role gets a prompt; merger
// (a Go binary that takes argv flags) does not. The two tables are
// kept separate so a future role can need auth without a prompt (or
// vice versa) without dragging one policy along with the other. See
// plans/worker-prompt-delivery.md §5.
func promptRequired(agentType string) bool {
	switch agentType {
	case string(model.AgentCoder),
		string(model.AgentReviewer),
		string(model.AgentFixer),
		"tester",
		"supervisor":
		return true
	case "merger":
		return false
	}
	// Unknown agent types default to false: a new role must add an
	// explicit entry + test here before it can get a prompt, same
	// deny-by-default discipline as credsMountRequired.
	return false
}

// resolveWorkerPromptRoot returns the host directory under which orch
// writes per-task prompt files. Reads DREM_PROMPT_ROOT_HOST first
// (set by `drem project register` on host), falls back to
// $HOME/.drem/projects/<project>/prompts — the same layout
// Manager.PromptDir already uses for the legacy host path (see
// internal/agent/spawn.go:207). Returns empty when neither source
// resolves; caller fail-closes on empty.
func resolveWorkerPromptRoot(project string) string {
	if p := os.Getenv(workerPromptRootEnv); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".drem", "projects", project, "prompts")
}

// renderAndWritePrompt renders the agent prompt via internal/prompt
// and writes it to <promptRoot>/<task-id>.md atomically (tmp file +
// rename). Returns the absolute host path of the written file.
//
// The rename pattern is load-bearing: SpawnWorker stats the file on
// the same goroutine immediately after this returns; any window where
// the file is half-written would race into a fail-closed spawn error.
// rename(2) within a single filesystem is atomic, so the file either
// does not exist or has the complete rendered content — never partial.
//
// Errors are returned as-is; buildSpawnContext turns them into a
// worker_spawn_failed event with reason=prompt_render_failed.
func (o *Orchestrator) renderAndWritePrompt(
	task *model.Task,
	project *model.Project,
	agentType string,
	promptRoot string,
) (string, error) {
	if task == nil {
		return "", fmt.Errorf("nil task")
	}
	if promptRoot == "" {
		return "", fmt.Errorf("prompt root is empty")
	}

	// WorktreePath: for container workers the worktree lives inside
	// the worker at /home/drem/work, but the prompt renderer reads it
	// as a logical handle (the generator doesn't actually open the
	// directory for most branches). Pass the host-identical bare repo
	// path when available so prompt.Generate has something truthful to
	// display in the rendered markdown. The legacy host path had
	// worktreePath point at a per-feature checkout on host; the
	// container path doesn't create one, so we use the bare repo path
	// here as a stable handle.
	var worktreePath string
	if o.worktree != nil {
		worktreePath = o.worktree.BareRepo()
	}

	rendered := prompt.Generate(prompt.Opts{
		Task:         task,
		Project:      project,
		AgentType:    model.AgentType(agentType),
		WorktreePath: worktreePath,
	})
	if strings.TrimSpace(rendered) == "" {
		return "", fmt.Errorf("prompt.Generate produced empty output for agent_type=%q task_id=%s",
			agentType, task.ID)
	}

	// Ensure the destination exists before writing. The per-project
	// compose template creates the dir at `drem project register`
	// time, but a fresh worktree or CI checkout may have raced ahead.
	if err := os.MkdirAll(promptRoot, 0o755); err != nil {
		return "", fmt.Errorf("mkdir prompt root %s: %w", promptRoot, err)
	}

	finalPath := filepath.Join(promptRoot, task.ID.String()+".md")
	// Write to a sibling tmp file first so the rename is atomic and
	// the worker's pre-stat never sees a partial write.
	tmpPath := finalPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(rendered), 0o644); err != nil {
		return "", fmt.Errorf("write prompt tmp %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		// Best-effort cleanup of the tmp file; ignore errors because
		// the caller is about to fail the spawn anyway.
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("rename prompt %s -> %s: %w", tmpPath, finalPath, err)
	}
	return finalPath, nil
}

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
// can record the resulting container ID on the right DB row.
type spawnWorkerContext struct {
	project     string
	agentType   string
	workerID    string
	branch      string
	image       string
	language    string
	bareRepo    string
	credsMount  string
	promptMount string
	envVars     map[string]string
	extraLabel  map[string]string
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
		AgentType:     swc.agentType,
		WorkerID:      swc.workerID,
		Branch:        swc.branch,
		Image:         swc.image,
		Env:           swc.envVars,
		Labels:        swc.extraLabel,
		BareRepoMount: swc.bareRepo,
		CredsMount:    swc.credsMount,
		PromptMount:   swc.promptMount,
	}

	res, spawnErr := o.Spawner.SpawnWorker(ctx, params)
	if spawnErr != nil {
		o.recordSpawnFailureEvent(task, agentType, spawnErr)
		return fmt.Errorf("spawn %s worker: %w", agentType, spawnErr)
	}

	// Record container ID and image identity on the agent row so the audit
	// trail in user story 49 has a single join from task → agent → container.
	if err := o.recordContainerOnAgent(task, res.ContainerID, params.Image, agentType); err != nil {
		o.logger.Error("spawn worker: record agent container", "task_id", task.ID, "error", err)
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

	o.recordSpawnEvent(task, agentType, res.ContainerID, params.Image)
	return nil
}

// buildSpawnContext derives the SpawnWorkerParams-shaped bundle from a task.
// Kept in one place so every spawn path uses consistent label/env/branch
// derivation. Returns an error when an agent type requires a
// subscription-auth creds mount but no host path is available — in that
// case spawning must fail-closed rather than produce an unauth'd worker.
func (o *Orchestrator) buildSpawnContext(task *model.Task, agentType string) (spawnWorkerContext, error) {
	project := o.projectID.String()
	branch := task.WorktreeBranch
	if branch == "" {
		branch = "feature/" + taskFeatureName(task)
	}
	workerID := fmt.Sprintf("%s-%s-%s", agentType, task.ID.String()[:shortIDLen], uuid.New().String()[:shortIDLen])

	env := map[string]string{
		"DREM_TASK_ID":   task.ID.String(),
		"DREM_PROJECT":   project,
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
	if credsMountRequired(agentType) {
		credsMount = resolveWorkerCredsPath()
		if credsMount == "" {
			return spawnWorkerContext{}, fmt.Errorf(
				"subscription-auth required for agent_type=%q but %s is unset and $HOME is unresolvable",
				agentType, workerCredsPathEnv)
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
	if promptRequired(agentType) {
		promptRoot := resolveWorkerPromptRoot(project)
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
		written, err := o.renderAndWritePrompt(task, proj, agentType, promptRoot)
		if err != nil {
			return spawnWorkerContext{}, fmt.Errorf(
				"render prompt for agent_type=%q task_id=%s: %w",
				agentType, task.ID, err)
		}
		promptMount = written
	}

	return spawnWorkerContext{
		project:     project,
		agentType:   agentType,
		workerID:    workerID,
		branch:      branch,
		bareRepo:    bareRepo,
		credsMount:  credsMount,
		promptMount: promptMount,
		envVars:     env,
		extraLabel:  labels,
	}, nil
}

// resolveProjectLanguage returns the project language from the Project row,
// defaulting to "go" when the column is empty. The spawner requires this
// label for coder workers to pick the right image (coder-<lang>).
func (o *Orchestrator) resolveProjectLanguage() string {
	var proj model.Project
	if err := o.db.First(&proj, "id = ?", o.projectID).Error; err != nil {
		return "go"
	}
	// Project does not carry language yet; fall back to "go" so the spawner
	// mapping resolves. A future migration can add Project.Language and the
	// caller will pick it up without changing the spawn contract.
	_ = proj
	return "go"
}

// recordContainerOnAgent writes the container ID and image onto the assigned
// agent row. When no agent is assigned yet, a synthetic one is created and
// attached so the audit trail is complete (user story 49).
func (o *Orchestrator) recordContainerOnAgent(task *model.Task, containerID, image, agentType string) error {
	if containerID == "" {
		return nil
	}

	now := time.Now()
	var ag model.Agent
	if task.AssignedAgentID != nil {
		if err := o.db.First(&ag, "id = ?", task.AssignedAgentID).Error; err == nil {
			return o.updateAgentContainer(&ag, containerID, image, now)
		}
	}

	// No agent yet — create one carrying the container fields so the TUI and
	// audit queries can find the spawn immediately.
	ag = model.Agent{
		ID:            uuid.New(),
		ProjectID:     o.projectID,
		AgentType:     model.AgentType(agentType),
		Name:          fmt.Sprintf("%s-%s", agentType, task.ID.String()[:shortIDLen]),
		Status:        model.AgentWorking,
		CurrentTaskID: &task.ID,
		TmuxSession:   containerID, // re-use TmuxSession as the container handle
		ModelID:       image,
		HeartbeatAt:   &now,
	}
	if err := o.db.Create(&ag).Error; err != nil {
		return fmt.Errorf("recordContainerOnAgent: create agent: %w", err)
	}

	task.AssignedAgentID = &ag.ID
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("recordContainerOnAgent: save task: %w", err)
	}
	return nil
}

// updateAgentContainer writes container metadata onto an existing agent.
func (o *Orchestrator) updateAgentContainer(ag *model.Agent, containerID, image string, now time.Time) error {
	ag.TmuxSession = containerID
	if image != "" {
		ag.ModelID = image
	}
	ag.HeartbeatAt = &now
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("updateAgentContainer: save: %w", err)
	}
	return nil
}

// recordSpawnEvent writes a TaskEvent documenting the spawn so the audit
// trail required by user story 49 is always present, regardless of whether
// the spawn happened through the old worktree path or the new spawner RPC.
func (o *Orchestrator) recordSpawnEvent(task *model.Task, agentType, containerID, image string) {
	detail := model.JSONField{
		"agent_type":   agentType,
		"container_id": containerID,
		"image":        image,
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
	containerID := strings.TrimSpace(ag.TmuxSession)
	if containerID == "" {
		return nil
	}
	if err := o.Spawner.DestroyWorker(ctx, spawner.DestroyWorkerParams{ContainerID: containerID}); err != nil {
		return fmt.Errorf("destroy worker %s: %w", containerID, err)
	}

	if o.GitrefRegistry != nil && o.worktree != nil && ag.WorktreeBranch != "" {
		ref, err := o.GitrefRegistry.FindByBranch(ctx, o.worktree.BareRepo(), ag.WorktreeBranch)
		if err == nil && ref != nil {
			_ = o.GitrefRegistry.MarkDeleted(ctx, ref.ID)
		}
	}
	return nil
}
