package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
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
// can record the resulting container ID on the right DB row.
type spawnWorkerContext struct {
	project    string
	agentType  string
	workerID   string
	branch     string
	image      string
	language   string
	bareRepo   string
	envVars    map[string]string
	extraLabel map[string]string
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

	swc := o.buildSpawnContext(task, agentType)
	params := spawner.SpawnWorkerParams{
		Project:       swc.project,
		AgentType:     swc.agentType,
		WorkerID:      swc.workerID,
		Branch:        swc.branch,
		Image:         swc.image,
		Env:           swc.envVars,
		Labels:        swc.extraLabel,
		BareRepoMount: swc.bareRepo,
	}

	res, err := o.Spawner.SpawnWorker(ctx, params)
	if err != nil {
		o.recordSpawnFailureEvent(task, agentType, err)
		return fmt.Errorf("spawn %s worker: %w", agentType, err)
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
// derivation.
func (o *Orchestrator) buildSpawnContext(task *model.Task, agentType string) spawnWorkerContext {
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

	return spawnWorkerContext{
		project:    project,
		agentType:  agentType,
		workerID:   workerID,
		branch:     branch,
		bareRepo:   bareRepo,
		envVars:    env,
		extraLabel: labels,
	}
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
	detail := model.JSONField{
		"agent_type": agentType,
		"error":      err.Error(),
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
