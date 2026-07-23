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
	taskRecoveryEvent       = "operator_recovery_applied"
	staleAssignmentActor    = "dremctl"
	missingFailureEvidence  = "missing_failure_evidence"
	staleAssignedWorker     = "stale_assigned_worker"
	activeTaskNoFreshEvents = "active_task_no_fresh_events"
	orphanBacklogChild      = "orphan_backlog_child"
	plannerCapacityIssue    = "planner_capacity_exhausted"
	duplicateActiveAttempts = "duplicate_active_attempts"
	parentReadinessBlocked  = "parent_readiness_blocked"
	branchGateFailure       = "branch_hygiene_gate_failure"
	failedTaskActiveAttempt = "failed_task_active_attempt"
	staleActiveAttempt      = "stale_active_attempt"
	deadAssignedAgent       = "dead_assigned_agent"
	dependencyFailureStall  = "dependency_failure_stall"
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
	activeAttempts, err := m.activeWorkerAttempts(ctx, tasks)
	if err != nil {
		return nil, err
	}

	agentsByID := make(map[uuid.UUID]model.Agent, len(agents))
	for _, agent := range agents {
		agentsByID[agent.ID] = agent
	}
	tasksByID := make(map[uuid.UUID]model.Task, len(tasks))
	childrenByParent := make(map[uuid.UUID][]model.Task)
	for _, task := range tasks {
		tasksByID[task.ID] = task
		if task.ParentTaskID != nil {
			childrenByParent[*task.ParentTaskID] = append(childrenByParent[*task.ParentTaskID], task)
		}
	}

	issues := make([]orchdto.HealthIssueDTO, 0)
	for _, task := range tasks {
		for _, issue := range activeAttemptHealthIssues(task, activeAttempts[task.ID], agentsByID, now) {
			issues = append(issues, issue)
		}

		for _, issue := range duplicateAttemptIssues(task, activeAttempts[task.ID], now) {
			issues = append(issues, issue)
		}

		if blocked := parentReadinessIssue(task, now); blocked.Type != "" {
			issues = append(issues, blocked)
		}
		if stalled := dependencyFailureIssue(task, childrenByParent[task.ID], now); stalled.Type != "" {
			issues = append(issues, stalled)
		}

		if gate := branchGateIssue(task, failures[task.ID], now); gate.Type != "" {
			issues = append(issues, gate)
		}

		if task.AssignedAgentID != nil {
			agent, hasAgent := agentsByID[*task.AssignedAgentID]
			if hasAgent && agent.Status == model.AgentDead && !terminalForHealth(task.Status) {
				issues = append(issues, orchdto.HealthIssueDTO{
					Type:              deadAssignedAgent,
					Severity:          "warning",
					TaskID:            task.ID.String(),
					WorkerID:          task.AssignedAgentID.String(),
					Status:            string(task.Status),
					DetectedAt:        now,
					AgeSeconds:        assignmentAgeSeconds(agent, now),
					Message:           "task is assigned to a dead agent",
					RecommendedAction: recommendedRecoveryAction("stale-assignment", task.ID),
				})
			}
			classification, err := m.classifyAssignmentWithEvidence(ctx, task, agent, agentsByID, now)
			if err != nil {
				return nil, err
			}
			if classification.Safe {
				issues = append(issues, orchdto.HealthIssueDTO{
					Type:              staleAssignedWorker,
					Severity:          "warning",
					TaskID:            task.ID.String(),
					WorkerID:          task.AssignedAgentID.String(),
					Status:            string(task.Status),
					DetectedAt:        now,
					AgeSeconds:        assignmentAgeSeconds(classification.Worker, now),
					Message:           classification.Message,
					RecommendedAction: recommendedRecoveryAction("stale-assignment", task.ID),
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
			if !hasSupportedFailureEvidence(task, failureType, summary) {
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

func activeAttemptHealthIssues(task model.Task, attempts []model.WorkerAttempt, agentsByID map[uuid.UUID]model.Agent, now time.Time) []orchdto.HealthIssueDTO {
	out := make([]orchdto.HealthIssueDTO, 0)
	if len(attempts) == 0 {
		return out
	}
	if task.Status == model.StatusFailed {
		out = append(out, orchdto.HealthIssueDTO{
			Type:              failedTaskActiveAttempt,
			Severity:          "warning",
			TaskID:            task.ID.String(),
			AttemptIDs:        attemptIDs(attempts),
			Status:            string(task.Status),
			DetectedAt:        now,
			Message:           fmt.Sprintf("failed task still has %d reserved/running worker attempt(s)", len(attempts)),
			RecommendedAction: recommendedRecoveryAction("exited-container", task.ID),
		})
	}
	for _, attempt := range attempts {
		if attempt.AgentID == nil {
			out = append(out, staleAttemptIssue(task, attempt, now, "active worker_attempt has no agent_id"))
			continue
		}
		agent, ok := agentsByID[*attempt.AgentID]
		if !ok {
			out = append(out, staleAttemptIssue(task, attempt, now, "active worker_attempt references a missing agent"))
			continue
		}
		if agent.Status == model.AgentDead {
			out = append(out, staleAttemptIssue(task, attempt, now, "active worker_attempt is owned by a dead agent"))
		}
	}
	return out
}

func staleAttemptIssue(task model.Task, attempt model.WorkerAttempt, now time.Time, message string) orchdto.HealthIssueDTO {
	return orchdto.HealthIssueDTO{
		Type:              staleActiveAttempt,
		Severity:          "warning",
		TaskID:            task.ID.String(),
		WorkerID:          attemptWorkerID(attempt),
		Role:              attempt.AgentType,
		Branch:            attempt.Branch,
		AttemptIDs:        []string{attempt.ID.String()},
		Status:            string(task.Status),
		DetectedAt:        now,
		AgeSeconds:        int64(now.Sub(attempt.UpdatedAt).Seconds()),
		Message:           message,
		RecommendedAction: recommendedRecoveryAction("exited-container", task.ID),
	}
}

func recommendedRecoveryAction(action string, taskID uuid.UUID) string {
	return fmt.Sprintf("dremctl recover %s %s --dry-run", action, taskID.String())
}

func (m publicReadModel) activeWorkerAttempts(ctx context.Context, tasks []model.Task) (map[uuid.UUID][]model.WorkerAttempt, error) {
	out := map[uuid.UUID][]model.WorkerAttempt{}
	ids := make([]uuid.UUID, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	if len(ids) == 0 {
		return out, nil
	}
	var attempts []model.WorkerAttempt
	if err := m.db.WithContext(ctx).
		Where("task_id IN ? AND state IN ?", ids, []string{model.WorkerAttemptReserved, model.WorkerAttemptRunning}).
		Order("created_at DESC").
		Find(&attempts).Error; err != nil {
		return nil, err
	}
	for _, attempt := range attempts {
		out[attempt.TaskID] = append(out[attempt.TaskID], attempt)
	}
	return out, nil
}

func duplicateAttemptIssues(task model.Task, attempts []model.WorkerAttempt, now time.Time) []orchdto.HealthIssueDTO {
	groups := map[string][]model.WorkerAttempt{}
	for _, attempt := range attempts {
		key := attempt.AgentType + "\x00" + attempt.Branch
		groups[key] = append(groups[key], attempt)
	}
	out := make([]orchdto.HealthIssueDTO, 0)
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		attemptIDs := make([]string, 0, len(group))
		for _, attempt := range group {
			attemptIDs = append(attemptIDs, attempt.ID.String())
		}
		out = append(out, orchdto.HealthIssueDTO{
			Type:       duplicateActiveAttempts,
			Severity:   "warning",
			TaskID:     task.ID.String(),
			Role:       group[0].AgentType,
			Branch:     group[0].Branch,
			AttemptIDs: attemptIDs,
			Status:     string(task.Status),
			DetectedAt: now,
			Message:    fmt.Sprintf("%d active attempts share task, role, and branch", len(group)),
		})
	}
	return out
}

func parentReadinessIssue(task model.Task, now time.Time) orchdto.HealthIssueDTO {
	blockers := strings.TrimSpace(stringField(task.Context, "parent_readiness_blockers"))
	if blockers == "" {
		blockers = strings.TrimSpace(stringField(task.Context, "parent_readiness_blocked"))
	}
	if blockers == "" {
		return orchdto.HealthIssueDTO{}
	}
	details := parseBlockedDependencies(blockers)
	return orchdto.HealthIssueDTO{
		Type:                parentReadinessBlocked,
		Severity:            "warning",
		TaskID:              task.ID.String(),
		Status:              string(task.Status),
		DetectedAt:          now,
		BlockedDependencies: details,
		Message:             firstNonEmpty(strings.Split(blockers, "\n")[0], "parent readiness is blocked by child dependencies"),
	}
}

func branchGateIssue(task model.Task, failure model.TaskEvent, now time.Time) orchdto.HealthIssueDTO {
	current, currentSet := boolField(task.Context, "latest_failure_current")
	if branchGateResolved(string(task.Status)) || (currentSet && !current) {
		return orchdto.HealthIssueDTO{}
	}
	failureType, summary := taskFailureFromEvent(failure)
	if failureType != branchGateFailure {
		summary = stringField(task.Context, "branch_hygiene_failure")
	}
	if strings.TrimSpace(summary) == "" {
		return orchdto.HealthIssueDTO{}
	}
	return orchdto.HealthIssueDTO{
		Type:       branchGateFailure,
		Severity:   "warning",
		TaskID:     task.ID.String(),
		Status:     string(task.Status),
		DetectedAt: now,
		GateFailure: &orchdto.GateFailureDTO{
			Gate:    "branch_hygiene",
			Reason:  firstNonEmpty(failureType, "branch_hygiene_failure"),
			Message: summary,
		},
		Message: "branch hygiene gate failed: " + summary,
	}
}

func parseBlockedDependencies(blockers string) []orchdto.BlockedDependencyDTO {
	lines := strings.Split(blockers, "\n")
	out := make([]orchdto.BlockedDependencyDTO, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		detail := orchdto.BlockedDependencyDTO{Message: line}
		if strings.HasPrefix(line, "dependency-blocked:") {
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				detail.TaskID = fields[2]
			}
			for i, field := range fields {
				if field == "depends" && i+2 < len(fields) && fields[i+1] == "on" {
					detail.DependencyID = fields[i+2]
				}
				if field == "status" && i+1 < len(fields) {
					detail.Status = strings.TrimRight(fields[i+1], ")")
				}
			}
		}
		out = append(out, detail)
	}
	return out
}

func hasSupportedFailureEvidence(task model.Task, eventType, eventSummary string) bool {
	if strings.TrimSpace(eventType) != "" && strings.TrimSpace(eventSummary) != "" {
		return true
	}
	if strings.TrimSpace(stringField(task.Context, "latest_failure_type")) != "" && strings.TrimSpace(stringField(task.Context, "latest_failure_summary")) != "" {
		return true
	}
	if strings.TrimSpace(stringField(task.Context, "failure_class")) != "" && strings.TrimSpace(stringField(task.Context, "failure_reason")) != "" {
		return true
	}
	return strings.TrimSpace(stringField(task.Context, "failure_reason")) != ""
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
		req.Actor = strings.TrimSpace(r.Header.Get(mutationActorHeader))
	}
	if guardConflict(task, req.ObservedStatus, req.ObservedUpdatedAt) {
		writeJSONError(w, http.StatusConflict, "observed task state is stale; refresh and retry")
		return
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
	if err := s.applyStaleAssignmentRecovery(r.Context(), task, result, req.Actor, req.ObservedStatus, req.ObservedUpdatedAt); err != nil {
		if errors.Is(err, errRecoveryConflict) {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		writeDBError(w, err)
		return
	}
	result.Applied = true
	result.Message = "stale assignment cleared"
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleTaskRecovery(w http.ResponseWriter, r *http.Request) {
	if !s.requireProject(w, r) {
		return
	}
	task, ok := s.loadTaskForMutation(w, r)
	if !ok {
		return
	}
	action := strings.TrimSpace(r.PathValue("action"))
	var req orchdto.TaskRecoveryRequest
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
		req.Actor = strings.TrimSpace(r.Header.Get(mutationActorHeader))
	}
	if guardConflict(task, req.ObservedStatus, req.ObservedUpdatedAt) {
		writeJSONError(w, http.StatusConflict, "observed task state is stale; refresh and retry")
		return
	}

	result, err := s.classifyTaskRecovery(r.Context(), task, action)
	if err != nil {
		writeDBError(w, err)
		return
	}
	if req.DryRun || !result.Safe {
		writeJSON(w, http.StatusOK, result)
		return
	}
	if err := s.applyTaskRecovery(r.Context(), task, result, req.Actor, req.ObservedStatus, req.ObservedUpdatedAt); err != nil {
		if errors.Is(err, errRecoveryConflict) {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		writeDBError(w, err)
		return
	}
	result.Applied = true
	result.Result = "applied"
	result.Message = "recovery applied: " + result.Action
	writeJSON(w, http.StatusOK, result)
}

type assignmentClassification struct {
	Worker  model.Agent
	Safe    bool
	Kind    string
	Message string
}

var errRecoveryConflict = errors.New("observed task state is stale; refresh and retry")

func (s *Server) classifyTaskRecovery(ctx context.Context, task model.Task, action string) (orchdto.TaskRecoveryDTO, error) {
	base := orchdto.TaskRecoveryDTO{
		TaskID: task.ID.String(),
		Status: string(task.Status),
		Action: action,
		Policy: "supported_recovery_surface",
	}
	switch action {
	case "exited-container-with-fresh-heartbeat", "exited-container":
		stale, err := s.classifyStaleAssignment(ctx, task)
		if err != nil {
			return orchdto.TaskRecoveryDTO{}, err
		}
		base.Action = "exited-container-with-fresh-heartbeat"
		base.Safe = stale.Safe && stale.Classification == "current_attempt_exited"
		base.Evidence = stale.Classification
		base.Result = dryRunResult(base.Safe)
		base.Message = stale.Message
		if !base.Safe {
			base.RefusalReason = "missing_evidence"
			base.Policy = "container_exit_evidence_required"
		}
		return base, nil
	case "duplicate-active-attempts":
		attempts, err := s.activeAttempts(ctx, task.ID)
		if err != nil {
			return orchdto.TaskRecoveryDTO{}, err
		}
		duplicateIDs := duplicateActiveAttemptIDs(attempts)
		base.Evidence = fmt.Sprintf("active_attempts=%d", len(attempts))
		base.AffectedCount = len(duplicateIDs)
		if len(duplicateIDs) == 0 {
			base.Policy = "active_lease_invariant"
			base.RefusalReason = "missing_evidence"
			base.Result = "refused"
			base.Message = "missing evidence: no duplicate active attempts for the same task, role, and branch"
			return base, nil
		}
		base.Safe = true
		base.Result = "would_apply"
		base.Message = fmt.Sprintf("would supersede %d older duplicate active attempt(s), keeping newest attempt per role and branch", len(duplicateIDs))
		return base, nil
	case "contaminated-branch-fail-gate", "contaminated-branch":
		base.Action = "contaminated-branch-fail-gate"
		base.Policy = "branch_contamination_requires_failed_gate"
		base.Evidence = firstNonEmpty(stringField(task.Context, "branch_acceptance_reason"), stringField(task.Context, "branch_hygiene_failure"), stringField(task.Context, "failure_reason"))
		if base.Evidence == "" {
			base.RefusalReason = "missing_evidence"
			base.Result = "refused"
			base.Message = "missing evidence: no branch contamination or branch hygiene failure evidence on task"
			return base, nil
		}
		if task.Status != model.StatusTestingReady && task.Status != model.StatusTestReview {
			base.RefusalReason = "safety_policy"
			base.Result = "refused"
			base.Message = fmt.Sprintf("safety policy: fail-gate recovery requires testing_ready or test_review, got %s", task.Status)
			return base, nil
		}
		base.Safe = true
		base.Result = "would_apply"
		base.Message = "would mark the contaminated branch gate failed with audit evidence"
		return base, nil
	case "stuck-parent-phase":
		base.Policy = "parent_readiness_guard"
		base.Evidence = firstNonEmpty(stringField(task.Context, "parent_readiness_blocked"), stringField(task.Context, "subtask_dispatch_blocked_signature"))
		base.RefusalReason = "safety_policy"
		base.Result = "refused"
		base.Message = "safety policy: parent phase recovery is diagnostic-only until a parent-readiness apply mutation is implemented"
		return base, nil
	case "accepted-child-work-adoption":
		base.Policy = "child_work_adoption_guard"
		base.Evidence = firstNonEmpty(stringField(task.Context, "reconcile_reason"), stringField(task.Context, "accepted_child_work"))
		base.RefusalReason = "safety_policy"
		base.Result = "refused"
		base.Message = "safety policy: accepted child work adoption is diagnostic-only until branch adoption can prove merged work"
		return base, nil
	default:
		base.Policy = "supported_recovery_surface"
		base.RefusalReason = "safety_policy"
		base.Result = "refused"
		base.Message = "safety policy: unsupported recovery action"
		return base, nil
	}
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
	classification, err := publicReadModel{db: s.DB}.classifyAssignmentWithEvidence(ctx, task, agent, agentsByID, time.Now())
	if err != nil {
		return orchdto.StaleAssignmentRecoveryDTO{}, err
	}
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
		return assignmentClassification{Safe: false, Kind: "missing_evidence", Message: "missing evidence: task has no assigned worker"}
	}
	if _, ok := agentsByID[*task.AssignedAgentID]; !ok {
		return assignmentClassification{Safe: true, Kind: "missing_worker", Message: "assigned worker row is missing"}
	}
	if terminalForHealth(task.Status) {
		return assignmentClassification{Worker: agent, Safe: false, Kind: "safety_policy_terminal_task", Message: fmt.Sprintf("safety policy: refusing stale-assignment recovery for terminal task in %s", task.Status)}
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
	return assignmentClassification{Worker: agent, Safe: false, Kind: "active_worker", Message: "active worker: refusing to clear live working assignment with fresh heartbeat"}
}

func (m publicReadModel) classifyAssignmentWithEvidence(ctx context.Context, task model.Task, agent model.Agent, agentsByID map[uuid.UUID]model.Agent, now time.Time) (assignmentClassification, error) {
	classification := classifyAssignment(task, agent, agentsByID, now)
	if task.AssignedAgentID == nil || classification.Kind == "missing_worker" || classification.Kind == "missing_evidence" || strings.HasPrefix(classification.Kind, "safety_policy") {
		return classification, nil
	}
	attempt, ok, err := m.currentAssignedAttempt(ctx, task.ID, *task.AssignedAgentID)
	if err != nil || !ok {
		return classification, err
	}
	exit, ok, err := m.latestDockerExitForAttempt(ctx, task.ID, attempt)
	if err != nil || !ok {
		return classification, err
	}
	reason := firstNonEmpty(stringField(exit.Details, "normalized_reason"), exit.NewValue, "container_exit")
	return assignmentClassification{
		Worker:  agent,
		Safe:    true,
		Kind:    "current_attempt_exited",
		Message: fmt.Sprintf("current attempt has Docker exit evidence (%s); container exit outranks heartbeat freshness", reason),
	}, nil
}

func (m publicReadModel) currentAssignedAttempt(ctx context.Context, taskID uuid.UUID, agentID uuid.UUID) (model.WorkerAttempt, bool, error) {
	var attempt model.WorkerAttempt
	err := m.db.WithContext(ctx).
		Where("task_id = ? AND agent_id = ?", taskID, agentID).
		Order("created_at DESC").
		First(&attempt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.WorkerAttempt{}, false, nil
	}
	if err != nil {
		return model.WorkerAttempt{}, false, err
	}
	return attempt, true, nil
}

func (m publicReadModel) latestDockerExitForAttempt(ctx context.Context, taskID uuid.UUID, attempt model.WorkerAttempt) (model.TaskEvent, bool, error) {
	var events []model.TaskEvent
	if err := m.db.WithContext(ctx).
		Where("task_id = ? AND event_type = ?", taskID, "container_died").
		Order("created_at DESC").
		Find(&events).Error; err != nil {
		return model.TaskEvent{}, false, err
	}
	for _, event := range events {
		if dockerExitEventMatchesAttempt(event, attempt) {
			return event, true, nil
		}
	}
	return model.TaskEvent{}, false, nil
}

func dockerExitEventMatchesAttempt(event model.TaskEvent, attempt model.WorkerAttempt) bool {
	if event.Details == nil {
		return false
	}
	if attemptID := stringField(event.Details, "attempt_id"); attemptID != "" {
		return attemptID == attempt.ID.String()
	}
	if attempt.ContainerID != "" && stringField(event.Details, "container_id") == attempt.ContainerID {
		return true
	}
	return attempt.WorkerID != "" && stringField(event.Details, "worker_id") == attempt.WorkerID
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
