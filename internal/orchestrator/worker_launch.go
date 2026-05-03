package orchestrator

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/workeridentity"
)

// WorkerLaunchService is the orchestration-domain boundary for container
// worker lifecycle operations. Callers describe the task and role they need;
// the implementation owns prompt delivery, auth mounts, branch provisioning,
// agent/attempt bookkeeping, audit events, and spawner RPC details.
type WorkerLaunchService interface {
	Launch(ctx context.Context, task *model.Task, role model.AgentType) (*WorkerLaunch, error)
	LaunchMerge(ctx context.Context, task *model.Task) (*MergeResult, error)
	DestroyForTask(ctx context.Context, task *model.Task) error
}

type WorkerLaunch struct {
	TaskID      uuid.UUID
	AgentID     uuid.UUID
	AttemptID   uuid.UUID
	WorkerID    string
	ContainerID string
	Branch      string
	AgentType   model.AgentType
	Image       string
	Provider    string
	ModelID     string
	Effort      string
}

type orchestratorWorkerLauncher struct {
	orchestrator *Orchestrator
}

func (o *Orchestrator) workerLaunchService() WorkerLaunchService {
	if o.workerLauncher != nil {
		return o.workerLauncher
	}
	return orchestratorWorkerLauncher{orchestrator: o}
}

// SetWorkerLaunchService overrides the worker launch boundary. Tests can use
// this to exercise scheduling/quickfix/merge behavior without knowing spawner
// params, prompt files, auth mounts, or WorkerAttempt details.
func (o *Orchestrator) SetWorkerLaunchService(s WorkerLaunchService) {
	o.workerLauncher = s
}

func (l orchestratorWorkerLauncher) Launch(ctx context.Context, task *model.Task, role model.AgentType) (*WorkerLaunch, error) {
	if task == nil {
		return nil, fmt.Errorf("launch worker: nil task")
	}
	if err := l.orchestrator.spawnTypedWorker(ctx, task, string(role)); err != nil {
		return nil, err
	}
	return l.orchestrator.workerLaunchForTask(task.ID, role)
}

func (l orchestratorWorkerLauncher) LaunchMerge(ctx context.Context, task *model.Task) (*MergeResult, error) {
	return l.orchestrator.dispatchMerge(ctx, task)
}

func (l orchestratorWorkerLauncher) DestroyForTask(ctx context.Context, task *model.Task) error {
	return l.orchestrator.destroyWorkerForTask(ctx, task)
}

func (o *Orchestrator) workerLaunchForTask(taskID uuid.UUID, role model.AgentType) (*WorkerLaunch, error) {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return nil, fmt.Errorf("load task after worker launch: %w", err)
	}
	if task.AssignedAgentID == nil {
		return nil, fmt.Errorf("worker launch produced no assigned agent for task %s", taskID)
	}

	var ag model.Agent
	if err := o.db.First(&ag, "id = ?", task.AssignedAgentID).Error; err != nil {
		return nil, fmt.Errorf("load agent after worker launch: %w", err)
	}

	attempt := model.WorkerAttempt{}
	attemptID := uuid.Nil
	if err := o.db.Where("task_id = ? AND agent_type = ?", taskID, string(role)).
		Order("created_at DESC").
		First(&attempt).Error; err == nil {
		attemptID = attempt.ID
	}

	handle := workeridentity.FromAgent(ag)
	return &WorkerLaunch{
		TaskID:      task.ID,
		AgentID:     ag.ID,
		AttemptID:   attemptID,
		WorkerID:    attempt.WorkerID,
		ContainerID: handle.LogContainerID(),
		Branch:      ag.WorktreeBranch,
		AgentType:   ag.AgentType,
		Image:       attempt.Image,
		Provider:    ag.Provider,
		ModelID:     ag.ModelID,
		Effort:      ag.Effort,
	}, nil
}
