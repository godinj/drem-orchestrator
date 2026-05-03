package workeridentity

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

type RuntimeKind string

const (
	RuntimeNone      RuntimeKind = ""
	RuntimeContainer RuntimeKind = "container"
	RuntimeTmux      RuntimeKind = "tmux"
)

type Store struct {
	db *gorm.DB
}

func NewStore(db *gorm.DB) *Store {
	return &Store{db: db}
}

type SpawnRecord struct {
	Task        *model.Task
	ProjectID   uuid.UUID
	AgentType   string
	WorkerID    string
	ContainerID string
	Image       string
	Branch      string
	Provider    string
	ModelID     string
	Effort      string
	Now         time.Time
}

type Handle struct {
	TaskID      uuid.UUID
	AgentID     uuid.UUID
	AttemptID   uuid.UUID
	WorkerID    string
	ContainerID string
	TmuxSession string
	AgentType   string
	Branch      string
	Image       string
	Runtime     RuntimeKind
}

func (h Handle) HasContainer() bool {
	return h.Runtime == RuntimeContainer && h.ContainerID != ""
}

func (h Handle) CanJumpToTmux() bool {
	return h.Runtime == RuntimeTmux && h.TmuxSession != ""
}

func (h Handle) LogContainerID() string {
	if h.HasContainer() {
		return h.ContainerID
	}
	return ""
}

func FromAgent(a model.Agent) Handle {
	h := Handle{
		AgentID:   a.ID,
		AgentType: string(a.AgentType),
		Branch:    a.WorktreeBranch,
		Runtime:   RuntimeNone,
	}
	if a.CurrentTaskID != nil {
		h.TaskID = *a.CurrentTaskID
	}
	if a.TmuxSession == "" {
		return h
	}
	if isLegacyTmuxSession(a.TmuxSession) {
		h.TmuxSession = a.TmuxSession
		h.Runtime = RuntimeTmux
		return h
	}
	h.ContainerID = a.TmuxSession
	h.Runtime = RuntimeContainer
	return h
}

func (s *Store) RecordSpawn(ctx context.Context, r SpawnRecord) (Handle, error) {
	if r.Task == nil {
		return Handle{}, fmt.Errorf("workeridentity.RecordSpawn: task is nil")
	}
	now := r.Now
	if now.IsZero() {
		now = time.Now()
	}

	h := Handle{
		TaskID:      r.Task.ID,
		WorkerID:    r.WorkerID,
		ContainerID: r.ContainerID,
		AgentType:   r.AgentType,
		Branch:      r.Branch,
		Image:       r.Image,
		Runtime:     RuntimeContainer,
	}

	if r.ContainerID != "" && r.AgentType != "merger" {
		ag, err := s.recordAgent(ctx, r, now)
		if err != nil {
			return Handle{}, err
		}
		h.AgentID = ag.ID
		h = mergeAgent(h, ag)
	}

	attempt := model.WorkerAttempt{
		ID:          uuid.New(),
		TaskID:      r.Task.ID,
		WorkerID:    r.WorkerID,
		ContainerID: r.ContainerID,
		AgentType:   r.AgentType,
		Image:       r.Image,
	}
	if h.AgentID != uuid.Nil {
		agentID := h.AgentID
		attempt.AgentID = &agentID
	}
	if err := s.db.WithContext(ctx).Create(&attempt).Error; err != nil {
		return Handle{}, fmt.Errorf("workeridentity.RecordSpawn: create attempt: %w", err)
	}
	h.AttemptID = attempt.ID
	return h, nil
}

func (s *Store) ForTask(ctx context.Context, task *model.Task) (Handle, error) {
	if task == nil {
		return Handle{}, fmt.Errorf("workeridentity.ForTask: task is nil")
	}
	if task.AssignedAgentID == nil {
		return Handle{TaskID: task.ID}, nil
	}
	h, err := s.ForAgent(ctx, *task.AssignedAgentID)
	if err != nil {
		return Handle{}, err
	}
	if h.TaskID == uuid.Nil {
		h.TaskID = task.ID
	}
	return h, nil
}

func (s *Store) ForAgent(ctx context.Context, agentID uuid.UUID) (Handle, error) {
	var ag model.Agent
	if err := s.db.WithContext(ctx).First(&ag, "id = ?", agentID).Error; err != nil {
		return Handle{}, fmt.Errorf("workeridentity.ForAgent: load agent: %w", err)
	}
	h := FromAgent(ag)
	var attempt model.WorkerAttempt
	if err := s.db.WithContext(ctx).Where("agent_id = ?", agentID).Order("created_at DESC").First(&attempt).Error; err == nil {
		h.AttemptID = attempt.ID
		h.WorkerID = attempt.WorkerID
		h.Image = attempt.Image
		if h.ContainerID == "" {
			h.ContainerID = attempt.ContainerID
		}
	}
	return h, nil
}

func (s *Store) ForAttempt(ctx context.Context, attemptID uuid.UUID) (Handle, error) {
	var attempt model.WorkerAttempt
	if err := s.db.WithContext(ctx).First(&attempt, "id = ?", attemptID).Error; err != nil {
		return Handle{}, fmt.Errorf("workeridentity.ForAttempt: load attempt: %w", err)
	}
	h := Handle{
		TaskID:      attempt.TaskID,
		AttemptID:   attempt.ID,
		WorkerID:    attempt.WorkerID,
		ContainerID: attempt.ContainerID,
		AgentType:   attempt.AgentType,
		Image:       attempt.Image,
		Runtime:     RuntimeContainer,
	}
	if attempt.AgentID == nil {
		return h, nil
	}
	agentHandle, err := s.ForAgent(ctx, *attempt.AgentID)
	if err != nil {
		return Handle{}, err
	}
	agentHandle.AttemptID = attempt.ID
	agentHandle.WorkerID = attempt.WorkerID
	agentHandle.Image = attempt.Image
	if agentHandle.ContainerID == "" {
		agentHandle.ContainerID = attempt.ContainerID
	}
	return agentHandle, nil
}

func (s *Store) recordAgent(ctx context.Context, r SpawnRecord, now time.Time) (model.Agent, error) {
	var ag model.Agent
	if r.Task.AssignedAgentID != nil {
		if err := s.db.WithContext(ctx).First(&ag, "id = ?", *r.Task.AssignedAgentID).Error; err == nil {
			updateAgent(&ag, r, now)
			if err := s.db.WithContext(ctx).Save(&ag).Error; err != nil {
				return model.Agent{}, fmt.Errorf("workeridentity.RecordSpawn: update agent: %w", err)
			}
			return ag, nil
		}
	}

	ag = model.Agent{
		ID:             uuid.New(),
		ProjectID:      r.ProjectID,
		AgentType:      model.AgentType(r.AgentType),
		Name:           fmt.Sprintf("%s-%s", r.AgentType, r.Task.ID.String()[:8]),
		Status:         model.AgentWorking,
		CurrentTaskID:  &r.Task.ID,
		WorktreeBranch: r.Branch,
		TmuxSession:    r.ContainerID,
		Provider:       r.Provider,
		ModelID:        r.ModelID,
		Effort:         r.Effort,
		HeartbeatAt:    &now,
	}
	if err := s.db.WithContext(ctx).Create(&ag).Error; err != nil {
		return model.Agent{}, fmt.Errorf("workeridentity.RecordSpawn: create agent: %w", err)
	}
	r.Task.AssignedAgentID = &ag.ID
	if err := s.db.WithContext(ctx).Save(r.Task).Error; err != nil {
		return model.Agent{}, fmt.Errorf("workeridentity.RecordSpawn: save task: %w", err)
	}
	return ag, nil
}

func updateAgent(ag *model.Agent, r SpawnRecord, now time.Time) {
	ag.TmuxSession = r.ContainerID
	ag.Provider = r.Provider
	ag.ModelID = r.ModelID
	ag.Effort = r.Effort
	if r.AgentType != "" {
		ag.AgentType = model.AgentType(r.AgentType)
	}
	ag.WorktreeBranch = r.Branch
	ag.CurrentTaskID = &r.Task.ID
	ag.HeartbeatAt = &now
}

func mergeAgent(h Handle, ag model.Agent) Handle {
	agentHandle := FromAgent(ag)
	h.AgentID = agentHandle.AgentID
	h.TmuxSession = agentHandle.TmuxSession
	h.Runtime = agentHandle.Runtime
	if agentHandle.ContainerID != "" {
		h.ContainerID = agentHandle.ContainerID
	}
	if h.Branch == "" {
		h.Branch = agentHandle.Branch
	}
	return h
}

func isLegacyTmuxSession(s string) bool {
	for _, r := range s {
		if r == '/' || r == ' ' || r == ':' {
			return true
		}
	}
	return false
}
