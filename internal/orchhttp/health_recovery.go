package orchhttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

const (
	healthFreshWindow       = 30 * time.Minute
	healthHeartbeatFresh    = 15 * time.Minute
	staleAssignmentEvent    = "stale_assignment_recovered"
	staleAssignmentActor    = "dremctl"
	missingFailureEvidence  = "missing_failure_evidence"
	staleAssignedWorker     = "stale_assigned_worker"
	activeTaskNoFreshEvents = "active_task_no_fresh_events"
	orphanBacklogChild      = "orphan_backlog_child"
	plannerCapacityIssue    = "planner_capacity_exhausted"
)

func (s *Server) handleHealthIssues(w http.ResponseWriter, r *http.Request) {
	if !s.requireProject(w, r) {
		return
	}
	issues, err := publicReadModel{db: s.DB, project: s.Project}.HealthIssues(r.Context(), time.Now())
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, issues)
}

func (m publicReadModel) HealthIssues(ctx context.Context, now time.Time) ([]orchdto.HealthIssueDTO, error) {
	var project model.Project
	if err := m.db.WithContext(ctx).Where("name = ?", m.project.Name).First(&project).Error; err != nil {
		return nil, err
	}

	var tasks []model.Task
	if err := m.db.WithContext(ctx).Where("project_id = ?", project.ID).Find(&tasks).Error; err != nil {
		return nil, err
	}
	var agents []model.Agent
	if err := m.db.WithContext(ctx).Where("project_id = ?", project.ID).Find(&agents).Error; err != nil {
		return nil, err
	}
	latestEvents, err := m.latestEventTimes(ctx, tasks)
	if err != nil {
		return nil, err
	}
	failures, err := m.latestTaskFailureEvents(ctx, tasks)
	if err != nil {
		return nil, err
	}

	agentsByID := make(map[uuid.UUID]model.Agent, len(agents))
	for _, agent := range agents {
		agentsByID[agent.ID] = agent
	}
	tasksByID := make(map[uuid.UUID]model.Task, len(tasks))
	for _, task := range tasks {
		tasksByID[task.ID] = task
	}

	issues := make([]orchdto.HealthIssueDTO, 0)
	for _, task := range tasks {
		if task.AssignedAgentID != nil {
			classification := classifyAssignment(task, agentsByID[*task.AssignedAgentID], agentsByID, now)
			if classification.Safe {
				issues = append(issues, orchdto.HealthIssueDTO{
					Type:       staleAssignedWorker,
					Severity:   "warning",
					TaskID:     task.ID.String(),
					WorkerID:   task.AssignedAgentID.String(),
					Status:     string(task.Status),
					DetectedAt: now,
					AgeSeconds: assignmentAgeSeconds(classification.Worker, now),
					Message:    classification.Message,
				})
			}
		}

		if taskActiveForHealth(task.Status) {
			last := latestEvents[task.ID]
			base := task.UpdatedAt
			if !last.IsZero() && last.After(base) {
				base = last
			}
			if now.Sub(base) > healthFreshWindow {
				lastPtr := (*time.Time)(nil)
				if !last.IsZero() {
					v := last
					lastPtr = &v
				}
				issues = append(issues, orchdto.HealthIssueDTO{
					Type:       activeTaskNoFreshEvents,
					Severity:   "warning",
					TaskID:     task.ID.String(),
					Status:     string(task.Status),
					DetectedAt: now,
					AgeSeconds: int64(now.Sub(base).Seconds()),
					LastEvent:  lastPtr,
					Message:    fmt.Sprintf("active task has no event or update newer than %s", healthFreshWindow),
				})
			}
		}

		if task.Status == model.StatusBacklog && task.ParentTaskID != nil {
			if parent, ok := tasksByID[*task.ParentTaskID]; ok && terminalForHealth(parent.Status) {
				issues = append(issues, orchdto.HealthIssueDTO{
					Type:       orphanBacklogChild,
					Severity:   "warning",
					TaskID:     task.ID.String(),
					Status:     string(task.Status),
					DetectedAt: now,
					Message:    fmt.Sprintf("backlog child is under terminal parent %s in %s", parent.ID, parent.Status),
				})
			}
		}

		if jsonBoolField(task.Context, plannerCapacityIssue) {
			issues = append(issues, orchdto.HealthIssueDTO{
				Type:       plannerCapacityIssue,
				Severity:   "warning",
				TaskID:     task.ID.String(),
				Status:     string(task.Status),
				DetectedAt: now,
				Message:    firstNonEmpty(stringField(task.Context, "planner_capacity_message"), "planner spawn capacity is exhausted"),
			})
		}

		if task.Status == model.StatusFailed {
			failure := failures[task.ID]
			failureType, summary := taskFailureFromEvent(failure)
			if failure.ID == uuid.Nil || strings.TrimSpace(failureType) == "" || strings.TrimSpace(summary) == "" {
				issues = append(issues, orchdto.HealthIssueDTO{
					Type:       missingFailureEvidence,
					Severity:   "warning",
					TaskID:     task.ID.String(),
					Status:     string(task.Status),
					DetectedAt: now,
					Message:    "failed task has no supported failure evidence event",
				})
			}
		}
	}

	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Type == issues[j].Type {
			return issues[i].TaskID < issues[j].TaskID
		}
		return issues[i].Type < issues[j].Type
	})
	return issues, nil
}

func (m publicReadModel) latestEventTimes(ctx context.Context, tasks []model.Task) (map[uuid.UUID]time.Time, error) {
	out := map[uuid.UUID]time.Time{}
	ids := make([]uuid.UUID, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	if len(ids) == 0 {
		return out, nil
	}
	var events []model.TaskEvent
	if err := m.db.WithContext(ctx).Where("task_id IN ?", ids).Order("created_at DESC").Find(&events).Error; err != nil {
		return nil, err
	}
	for _, event := range events {
		if _, ok := out[event.TaskID]; !ok {
			out[event.TaskID] = event.CreatedAt
		}
	}
	return out, nil
}

func (s *Server) handleRecoverStaleAssignment(w http.ResponseWriter, r *http.Request) {
	if !s.requireProject(w, r) {
		return
	}
	task, ok := s.loadTaskForMutation(w, r)
	if !ok {
		return
	}
	var req orchdto.StaleAssignmentRecoveryRequest
	if err := decodeOptionalJSON(r.Body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.DryRun == req.Apply {
		writeJSONError(w, http.StatusBadRequest, "exactly one of dry_run or apply is required")
		return
	}
	req.Actor = strings.TrimSpace(req.Actor)
	if req.Actor == "" {
		req.Actor = staleAssignmentActor
	}

	result, err := s.classifyStaleAssignment(r.Context(), task)
	if err != nil {
		writeDBError(w, err)
		return
	}
	if !result.Safe {
		writeJSONError(w, http.StatusConflict, result.Message)
		return
	}
	if req.DryRun {
		writeJSON(w, http.StatusOK, result)
		return
	}
	if err := s.applyStaleAssignmentRecovery(r.Context(), task, result, req.Actor); err != nil {
		writeDBError(w, err)
		return
	}
	result.Applied = true
	result.Message = "stale assignment cleared"
	writeJSON(w, http.StatusOK, result)
}

type assignmentClassification struct {
	Worker  model.Agent
	Safe    bool
	Kind    string
	Message string
}

func (s *Server) classifyStaleAssignment(ctx context.Context, task model.Task) (orchdto.StaleAssignmentRecoveryDTO, error) {
	agentsByID := map[uuid.UUID]model.Agent{}
	var agent model.Agent
	if task.AssignedAgentID != nil {
		err := s.DB.WithContext(ctx).Where("id = ?", *task.AssignedAgentID).First(&agent).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
		case err != nil:
			return orchdto.StaleAssignmentRecoveryDTO{}, err
		default:
			agentsByID[agent.ID] = agent
		}
	}
	classification := classifyAssignment(task, agent, agentsByID, time.Now())
	workerStatus := ""
	if agent.ID != uuid.Nil {
		workerStatus = string(agent.Status)
	}
	return orchdto.StaleAssignmentRecoveryDTO{
		TaskID:         task.ID.String(),
		Status:         string(task.Status),
		AssignedWorker: assignedID(task),
		WorkerStatus:   workerStatus,
		Classification: classification.Kind,
		Safe:           classification.Safe,
		Message:        classification.Message,
	}, nil
}

func classifyAssignment(task model.Task, agent model.Agent, agentsByID map[uuid.UUID]model.Agent, now time.Time) assignmentClassification {
	if task.AssignedAgentID == nil {
		return assignmentClassification{Safe: false, Kind: "unassigned", Message: "task has no assigned worker"}
	}
	if _, ok := agentsByID[*task.AssignedAgentID]; !ok {
		return assignmentClassification{Safe: true, Kind: "missing_worker", Message: "assigned worker row is missing"}
	}
	if agent.Status == model.AgentDead {
		return assignmentClassification{Worker: agent, Safe: true, Kind: "dead_worker", Message: "assigned worker is dead"}
	}
	if agent.Status != model.AgentWorking {
		return assignmentClassification{Worker: agent, Safe: true, Kind: "non_working_worker", Message: fmt.Sprintf("assigned worker is %s, not working", agent.Status)}
	}
	if agent.HeartbeatAt == nil || now.Sub(*agent.HeartbeatAt) > healthHeartbeatFresh {
		return assignmentClassification{Worker: agent, Safe: true, Kind: "stale_heartbeat", Message: fmt.Sprintf("assigned worker heartbeat is older than %s", healthHeartbeatFresh)}
	}
	return assignmentClassification{Worker: agent, Safe: false, Kind: "live_working_worker", Message: "refusing to clear live working assignment with fresh heartbeat"}
}

func (s *Server) applyStaleAssignmentRecovery(ctx context.Context, task model.Task, result orchdto.StaleAssignmentRecoveryDTO, actor string) error {
	now := time.Now()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if task.AssignedAgentID != nil {
			if err := tx.Model(&model.Agent{}).
				Where("id = ? AND current_task_id = ?", *task.AssignedAgentID, task.ID).
				Updates(map[string]any{"current_task_id": nil, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&model.Task{}).
			Where("id = ?", task.ID).
			Updates(map[string]any{"assigned_agent_id": nil, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.TaskEvent{
			ID:        uuid.New(),
			TaskID:    task.ID,
			EventType: staleAssignmentEvent,
			OldValue:  result.AssignedWorker,
			NewValue:  "",
			Details: model.JSONField{
				"actor":          actor,
				"classification": result.Classification,
				"worker_status":  result.WorkerStatus,
				"message":        result.Message,
			},
			Actor:     actor,
			CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		return tx.Create(&model.TaskComment{
			ID:        uuid.New(),
			TaskID:    task.ID,
			Author:    actor,
			Body:      fmt.Sprintf("Recovered stale assignment: %s (%s). Previous worker: %s", result.Message, result.Classification, result.AssignedWorker),
			CreatedAt: now,
		}).Error
	})
}

func taskActiveForHealth(status model.TaskStatus) bool {
	switch status {
	case model.StatusClassifying, model.StatusPlanning, model.StatusTestWriting, model.StatusInProgress, model.StatusMerging:
		return true
	default:
		return false
	}
}

func terminalForHealth(status model.TaskStatus) bool {
	switch status {
	case model.StatusDone, model.StatusFailed, model.StatusRejected, model.StatusCancelled:
		return true
	default:
		return false
	}
}

func jsonBoolField(fields model.JSONField, key string) bool {
	if fields == nil {
		return false
	}
	value, _ := fields[key].(bool)
	return value
}

func assignedID(task model.Task) string {
	if task.AssignedAgentID == nil {
		return ""
	}
	return task.AssignedAgentID.String()
}

func assignmentAgeSeconds(agent model.Agent, now time.Time) int64 {
	if agent.ID == uuid.Nil || agent.HeartbeatAt == nil {
		return 0
	}
	return int64(now.Sub(*agent.HeartbeatAt).Seconds())
}
