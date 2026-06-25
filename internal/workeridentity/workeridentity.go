package workeridentity

import (
	"context"
	"errors"
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

const DefaultLeaseTTL = 2 * time.Minute

var (
	ErrTaskAlreadyClaimed = errors.New("task already has an active worker reservation")
	ErrLeaseConflict      = errors.New("worker attempt lease is not held by owner")
	ErrLeaseExpired       = errors.New("worker attempt lease is expired")
	ErrAttemptTerminal    = errors.New("worker attempt is terminal")
)

func NewStore(db *gorm.DB) *Store {
	return &Store{db: db}
}

type SpawnRecord struct {
	Task                    *model.Task
	ProjectID               uuid.UUID
	AgentType               string
	WorkerID                string
	ContainerID             string
	Image                   string
	Branch                  string
	Provider                string
	ModelID                 string
	Effort                  string
	PromptAssetVersionsJSON string
	RenderedPromptHash      string
	RenderedPromptPath      string
	Now                     time.Time
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

type Reservation struct {
	TaskID    uuid.UUID
	AgentID   uuid.UUID
	AttemptID uuid.UUID
	WorkerID  string
	AgentType string
	Branch    string
	Image     string
	Provider  string
	ModelID   string
	Effort    string
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
		ID:                      uuid.New(),
		TaskID:                  r.Task.ID,
		WorkerID:                r.WorkerID,
		ContainerID:             r.ContainerID,
		AgentType:               r.AgentType,
		Branch:                  r.Branch,
		Image:                   r.Image,
		State:                   model.WorkerAttemptRunning,
		LeaseOwner:              r.WorkerID,
		LeaseExpiresAt:          ptrTime(now.Add(DefaultLeaseTTL)),
		PromptAssetVersionsJSON: r.PromptAssetVersionsJSON,
		RenderedPromptHash:      r.RenderedPromptHash,
		RenderedPromptPath:      r.RenderedPromptPath,
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

func (s *Store) ReserveSpawn(ctx context.Context, r SpawnRecord) (Reservation, error) {
	if r.Task == nil {
		return Reservation{}, fmt.Errorf("workeridentity.ReserveSpawn: task is nil")
	}
	now := r.Now
	if now.IsZero() {
		now = time.Now()
	}

	var out Reservation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.First(&task, "id = ?", r.Task.ID).Error; err != nil {
			return fmt.Errorf("load task: %w", err)
		}
		if task.AssignedAgentID != nil {
			return ErrTaskAlreadyClaimed
		}
		if err := tx.Model(&model.WorkerAttempt{}).
			Where("task_id = ? AND agent_type = ? AND branch = ? AND completed_at IS NULL", task.ID, r.AgentType, r.Branch).
			Updates(map[string]any{
				"state":        model.WorkerAttemptSuperseded,
				"completed_at": &now,
			}).Error; err != nil {
			return fmt.Errorf("close stale attempts: %w", err)
		}

		ag := model.Agent{
			ID:             uuid.New(),
			ProjectID:      r.ProjectID,
			AgentType:      model.AgentType(r.AgentType),
			Name:           fmt.Sprintf("%s-%s", r.AgentType, task.ID.String()[:8]),
			Status:         model.AgentWorking,
			CurrentTaskID:  &task.ID,
			WorktreeBranch: r.Branch,
			Provider:       r.Provider,
			ModelID:        r.ModelID,
			Effort:         r.Effort,
			HeartbeatAt:    &now,
		}
		if err := tx.Create(&ag).Error; err != nil {
			return fmt.Errorf("create agent: %w", err)
		}

		attempt := model.WorkerAttempt{
			ID:                      uuid.New(),
			TaskID:                  task.ID,
			AgentID:                 &ag.ID,
			WorkerID:                r.WorkerID,
			AgentType:               r.AgentType,
			Branch:                  r.Branch,
			Image:                   r.Image,
			State:                   model.WorkerAttemptReserved,
			LeaseOwner:              r.WorkerID,
			LeaseExpiresAt:          ptrTime(now.Add(DefaultLeaseTTL)),
			PromptAssetVersionsJSON: r.PromptAssetVersionsJSON,
			RenderedPromptHash:      r.RenderedPromptHash,
			RenderedPromptPath:      r.RenderedPromptPath,
		}
		if err := tx.Create(&attempt).Error; err != nil {
			return fmt.Errorf("create attempt: %w", err)
		}

		res := tx.Model(&model.Task{}).
			Where("id = ? AND assigned_agent_id IS NULL", task.ID).
			Update("assigned_agent_id", ag.ID)
		if res.Error != nil {
			return fmt.Errorf("claim task: %w", res.Error)
		}
		if res.RowsAffected != 1 {
			return ErrTaskAlreadyClaimed
		}

		out = Reservation{
			TaskID:    task.ID,
			AgentID:   ag.ID,
			AttemptID: attempt.ID,
			WorkerID:  r.WorkerID,
			AgentType: r.AgentType,
			Branch:    r.Branch,
			Image:     r.Image,
			Provider:  r.Provider,
			ModelID:   r.ModelID,
			Effort:    r.Effort,
		}
		return nil
	})
	if err != nil {
		return Reservation{}, fmt.Errorf("workeridentity.ReserveSpawn: %w", err)
	}
	r.Task.AssignedAgentID = &out.AgentID
	return out, nil
}

func (s *Store) FinalizeSpawn(ctx context.Context, res Reservation, containerID string) (Handle, error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ag model.Agent
		if err := tx.First(&ag, "id = ?", res.AgentID).Error; err != nil {
			return fmt.Errorf("load agent: %w", err)
		}
		updates := map[string]any{
			"container_id":     containerID,
			"state":            model.WorkerAttemptRunning,
			"lease_expires_at": time.Now().Add(DefaultLeaseTTL),
		}
		result := tx.Model(&model.WorkerAttempt{}).
			Where("id = ? AND agent_id = ? AND task_id = ? AND state = ?", res.AttemptID, res.AgentID, res.TaskID, model.WorkerAttemptReserved).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("update attempt: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("attempt is not reserved")
		}

		ag.TmuxSession = containerID
		if err := tx.Save(&ag).Error; err != nil {
			return fmt.Errorf("update agent: %w", err)
		}
		return nil
	})
	if err != nil {
		return Handle{}, fmt.Errorf("workeridentity.FinalizeSpawn: %w", err)
	}
	return Handle{
		TaskID:      res.TaskID,
		AgentID:     res.AgentID,
		AttemptID:   res.AttemptID,
		WorkerID:    res.WorkerID,
		ContainerID: containerID,
		AgentType:   res.AgentType,
		Branch:      res.Branch,
		Image:       res.Image,
		Runtime:     RuntimeContainer,
	}, nil
}

func (s *Store) AbortReservation(ctx context.Context, res Reservation, reason string) error {
	now := time.Now()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{
			"state":        model.WorkerAttemptAborted,
			"completed_at": &now,
		}
		if reason != "" {
			updates["container_id"] = ""
		}
		attemptResult := tx.Model(&model.WorkerAttempt{}).
			Where("id = ? AND agent_id = ? AND task_id = ? AND state IN ?", res.AttemptID, res.AgentID, res.TaskID, []string{model.WorkerAttemptReserved, model.WorkerAttemptRunning}).
			Updates(updates)
		if attemptResult.Error != nil {
			return fmt.Errorf("mark attempt aborted: %w", attemptResult.Error)
		}
		if attemptResult.RowsAffected != 1 {
			return nil
		}

		if err := tx.Model(&model.Agent{}).
			Where("id = ?", res.AgentID).
			Updates(map[string]any{
				"status":       model.AgentDead,
				"completed_at": &now,
				"exit_reason":  reason,
			}).Error; err != nil {
			return fmt.Errorf("mark agent done: %w", err)
		}

		if err := tx.Model(&model.Task{}).
			Where("id = ? AND assigned_agent_id = ?", res.TaskID, res.AgentID).
			Update("assigned_agent_id", nil).Error; err != nil {
			return fmt.Errorf("clear task assignment: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("workeridentity.AbortReservation: %w", err)
	}
	return nil
}

func (s *Store) RenewLease(ctx context.Context, attemptID uuid.UUID, owner string, ttl time.Duration, now time.Time) error {
	if now.IsZero() {
		now = time.Now()
	}
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	res := s.db.WithContext(ctx).Model(&model.WorkerAttempt{}).
		Where("id = ? AND lease_owner = ? AND state IN ? AND completed_at IS NULL AND (lease_expires_at IS NULL OR lease_expires_at > ?)", attemptID, owner, activeAttemptStates(), now).
		Updates(map[string]any{"lease_expires_at": now.Add(ttl), "updated_at": now})
	if res.Error != nil {
		return fmt.Errorf("workeridentity.RenewLease: %w", res.Error)
	}
	if res.RowsAffected == 1 {
		return nil
	}
	return s.classifyLeaseMiss(ctx, attemptID, owner, now)
}

func (s *Store) FinishAttempt(ctx context.Context, attemptID uuid.UUID, owner, state, firstError string, now time.Time) error {
	if now.IsZero() {
		now = time.Now()
	}
	updates := map[string]any{
		"state":            state,
		"completed_at":     &now,
		"updated_at":       now,
		"lease_expires_at": &now,
	}
	if state == model.WorkerAttemptFailed {
		updates["failed_at"] = &now
	}
	if firstError != "" {
		updates["first_error"] = firstError
	}
	res := s.db.WithContext(ctx).Model(&model.WorkerAttempt{}).
		Where("id = ? AND lease_owner = ? AND state IN ? AND completed_at IS NULL AND (lease_expires_at IS NULL OR lease_expires_at > ?)", attemptID, owner, activeAttemptStates(), now).
		Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("workeridentity.FinishAttempt: %w", res.Error)
	}
	if res.RowsAffected == 1 {
		return nil
	}
	return s.classifyLeaseMiss(ctx, attemptID, owner, now)
}

func (s *Store) classifyLeaseMiss(ctx context.Context, attemptID uuid.UUID, owner string, now time.Time) error {
	var attempt model.WorkerAttempt
	if err := s.db.WithContext(ctx).First(&attempt, "id = ?", attemptID).Error; err != nil {
		return ErrLeaseConflict
	}
	if attempt.CompletedAt != nil || !isActiveAttemptState(attempt.State) {
		return ErrAttemptTerminal
	}
	if attempt.LeaseOwner != owner {
		return ErrLeaseConflict
	}
	if attempt.LeaseExpiresAt != nil && !attempt.LeaseExpiresAt.After(now) {
		return ErrLeaseExpired
	}
	return ErrLeaseConflict
}

func activeAttemptStates() []string {
	return []string{model.WorkerAttemptReserved, model.WorkerAttemptRunning}
}

func isActiveAttemptState(state string) bool {
	return state == model.WorkerAttemptReserved || state == model.WorkerAttemptRunning
}

func ptrTime(t time.Time) *time.Time {
	return &t
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
		if h.Branch == "" {
			h.Branch = attempt.Branch
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
		Branch:      attempt.Branch,
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
	if agentHandle.Branch == "" {
		agentHandle.Branch = attempt.Branch
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
