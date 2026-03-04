package memory

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// OrchestratorSnapshot represents a checkpoint of orchestrator state.
type OrchestratorSnapshot struct {
	ActiveTasks    []uuid.UUID `json:"active_tasks"`
	PendingReviews []uuid.UUID `json:"pending_reviews"`
	ActiveAgents   []uuid.UUID `json:"active_agents"`
	StaleAgents    []uuid.UUID `json:"stale_agents"`
	LastCheckpoint time.Time   `json:"last_checkpoint"`
}

// SaveOrchestratorState saves a checkpoint of the current orchestrator state
// as a Memory record with type "orchestrator_state".
//
//  1. Query active tasks (BACKLOG, PLANNING, IN_PROGRESS, MERGING)
//  2. Query pending reviews (PLAN_REVIEW, MANUAL_TESTING)
//  3. Query active agents (WORKING status)
//  4. Store as a Memory with metadata containing the snapshot
func SaveOrchestratorState(db *gorm.DB, orchestratorAgentID uuid.UUID) error {
	now := time.Now()

	// 1. Active tasks
	var activeTasks []model.Task
	err := db.Where("status IN ?", []string{
		string(model.StatusBacklog),
		string(model.StatusPlanning),
		string(model.StatusInProgress),
		string(model.StatusMerging),
	}).Find(&activeTasks).Error
	if err != nil {
		return fmt.Errorf("save orchestrator state: query active tasks: %w", err)
	}

	activeTaskIDs := make([]uuid.UUID, len(activeTasks))
	for i, t := range activeTasks {
		activeTaskIDs[i] = t.ID
	}

	// 2. Pending reviews
	var pendingReviews []model.Task
	err = db.Where("status IN ?", []string{
		string(model.StatusPlanReview),
		string(model.StatusManualTesting),
	}).Find(&pendingReviews).Error
	if err != nil {
		return fmt.Errorf("save orchestrator state: query pending reviews: %w", err)
	}

	pendingReviewIDs := make([]uuid.UUID, len(pendingReviews))
	for i, t := range pendingReviews {
		pendingReviewIDs[i] = t.ID
	}

	// 3. Active agents
	var activeAgents []model.Agent
	err = db.Where("status = ?", string(model.AgentWorking)).Find(&activeAgents).Error
	if err != nil {
		return fmt.Errorf("save orchestrator state: query active agents: %w", err)
	}

	activeAgentIDs := make([]uuid.UUID, len(activeAgents))
	for i, a := range activeAgents {
		activeAgentIDs[i] = a.ID
	}

	snapshot := OrchestratorSnapshot{
		ActiveTasks:    activeTaskIDs,
		PendingReviews: pendingReviewIDs,
		ActiveAgents:   activeAgentIDs,
		StaleAgents:    nil,
		LastCheckpoint: now,
	}

	// Marshal snapshot to metadata
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("save orchestrator state: marshal snapshot: %w", err)
	}

	var metadata model.JSONField
	if err := json.Unmarshal(snapshotBytes, &metadata); err != nil {
		return fmt.Errorf("save orchestrator state: unmarshal to metadata: %w", err)
	}

	// 4. Store as Memory
	mem := &model.Memory{
		ID:         uuid.New(),
		AgentID:    orchestratorAgentID,
		Content:    fmt.Sprintf("Orchestrator checkpoint at %s", now.Format(time.RFC3339)),
		MemoryType: "orchestrator_state",
		Metadata:   metadata,
		CreatedAt:  now,
	}

	if err := db.Create(mem).Error; err != nil {
		return fmt.Errorf("save orchestrator state: create memory: %w", err)
	}

	return nil
}

// RestoreOrchestratorState loads the last checkpoint and reconciles stale
// agents.
//
//  1. Load latest "orchestrator_state" memory for the given orchestrator agent
//  2. Parse snapshot from metadata
//  3. Check heartbeats for active agents — mark stale ones
func RestoreOrchestratorState(db *gorm.DB, orchestratorAgentID uuid.UUID) (*OrchestratorSnapshot, error) {
	// 1. Load latest orchestrator_state memory
	var mem model.Memory
	err := db.
		Where("agent_id = ?", orchestratorAgentID).
		Where("memory_type = ?", "orchestrator_state").
		Order("created_at DESC").
		First(&mem).Error
	if err != nil {
		return nil, fmt.Errorf("restore orchestrator state: load checkpoint: %w", err)
	}

	// 2. Parse snapshot from metadata
	metadataBytes, err := json.Marshal(mem.Metadata)
	if err != nil {
		return nil, fmt.Errorf("restore orchestrator state: marshal metadata: %w", err)
	}

	var snapshot OrchestratorSnapshot
	if err := json.Unmarshal(metadataBytes, &snapshot); err != nil {
		return nil, fmt.Errorf("restore orchestrator state: parse snapshot: %w", err)
	}

	// 3. Check heartbeats for active agents — identify stale ones
	staleThreshold := time.Now().Add(-5 * time.Minute)
	var staleAgentIDs []uuid.UUID

	for _, agentID := range snapshot.ActiveAgents {
		var agent model.Agent
		if err := db.Where("id = ?", agentID).First(&agent).Error; err != nil {
			// Agent no longer exists — consider it stale
			staleAgentIDs = append(staleAgentIDs, agentID)
			continue
		}

		// Check heartbeat
		if agent.HeartbeatAt == nil || agent.HeartbeatAt.Before(staleThreshold) {
			staleAgentIDs = append(staleAgentIDs, agentID)
			// Mark agent as dead
			db.Model(&model.Agent{}).Where("id = ?", agentID).Update("status", string(model.AgentDead))
		}
	}

	snapshot.StaleAgents = staleAgentIDs

	return &snapshot, nil
}

// ShouldCompact returns true if an agent's memory count exceeds thresholds.
// Threshold: memory_count > 50 * compactionThreshold OR estimated tokens >
// 8000 * compactionThreshold.
func ShouldCompact(db *gorm.DB, agentID uuid.UUID, compactionThreshold float64) (bool, error) {
	// Count non-archived memories
	var count int64
	err := db.Model(&model.Memory{}).
		Where("agent_id = ?", agentID).
		Where("memory_type NOT LIKE ?", "archived_%").
		Where("memory_type != ?", "conversation_summary").
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("should compact: count memories: %w", err)
	}

	if float64(count) > 50*compactionThreshold {
		return true, nil
	}

	// Estimate tokens as sum(len(content)) / 4
	var totalLength struct {
		Total int64
	}
	err = db.Model(&model.Memory{}).
		Select("COALESCE(SUM(LENGTH(content)), 0) as total").
		Where("agent_id = ?", agentID).
		Where("memory_type NOT LIKE ?", "archived_%").
		Where("memory_type != ?", "conversation_summary").
		Scan(&totalLength).Error
	if err != nil {
		return false, fmt.Errorf("should compact: estimate tokens: %w", err)
	}

	estimatedTokens := float64(totalLength.Total) / 4.0
	if estimatedTokens > 8000*compactionThreshold {
		return true, nil
	}

	return false, nil
}
