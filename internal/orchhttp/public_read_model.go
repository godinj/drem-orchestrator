package orchhttp

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/workeridentity"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

type publicReadModel struct {
	db      *gorm.DB
	project ProjectInfo
}

type taskListQuery struct {
	ProjectName     string
	Status          string
	IncludeArchived bool
	Limit           int
	Offset          int
}

func (m publicReadModel) ListProjects(ctx context.Context) ([]orchdto.ProjectDTO, error) {
	var count int64
	if err := m.db.WithContext(ctx).Model(&model.Agent{}).
		Where("status = ?", model.AgentWorking).Count(&count).Error; err != nil {
		return nil, err
	}
	return []orchdto.ProjectDTO{{
		Name:        m.project.Name,
		Language:    m.project.Language,
		OrchURL:     m.project.OrchURL,
		WorkerCount: int(count),
	}}, nil
}

func (m publicReadModel) ListTasks(ctx context.Context, query taskListQuery) ([]orchdto.TaskDTO, error) {
	var project model.Project
	err := m.db.WithContext(ctx).Where("name = ?", query.ProjectName).First(&project).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return []orchdto.TaskDTO{}, nil
	}
	if err != nil {
		return nil, err
	}

	dbQuery := m.db.WithContext(ctx).
		Where("project_id = ?", project.ID).
		Order("created_at DESC").
		Limit(query.Limit).Offset(query.Offset)
	if query.Status != "" {
		dbQuery = dbQuery.Where("status = ?", query.Status)
	} else if !query.IncludeArchived {
		dbQuery = dbQuery.Where("status <> ?", model.StatusCancelled)
	}

	var tasks []model.Task
	if err := dbQuery.Find(&tasks).Error; err != nil {
		return nil, err
	}
	failureEvents, err := m.latestTaskFailureEvents(ctx, tasks)
	if err != nil {
		return nil, err
	}
	activeAttempts, err := m.activeTaskAttempts(ctx, tasks)
	if err != nil {
		return nil, err
	}
	out := make([]orchdto.TaskDTO, 0, len(tasks))
	for _, t := range tasks {
		d := toTaskDTO(t, failureEvents[t.ID])
		if attempts := activeAttempts[t.ID]; len(attempts) > 0 {
			d.ActiveAttempts = attempts
			d.ActiveAttemptCount = len(attempts)
		}
		out = append(out, d)
	}
	return out, nil
}

func (m publicReadModel) activeTaskAttempts(ctx context.Context, tasks []model.Task) (map[uuid.UUID][]orchdto.TaskAttemptLeaseDTO, error) {
	out := map[uuid.UUID][]orchdto.TaskAttemptLeaseDTO{}
	ids := make([]uuid.UUID, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.ID)
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
		out[attempt.TaskID] = append(out[attempt.TaskID], toTaskAttemptLeaseDTO(attempt))
	}
	return out, nil
}

func (m publicReadModel) latestTaskFailureEvents(ctx context.Context, tasks []model.Task) (map[uuid.UUID]model.TaskEvent, error) {
	out := map[uuid.UUID]model.TaskEvent{}
	ids := make([]uuid.UUID, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.ID)
	}
	if len(ids) == 0 {
		return out, nil
	}
	var events []model.TaskEvent
	if err := m.db.WithContext(ctx).
		Where("task_id IN ? AND event_type IN ?", ids, []string{recordTypeCrash, recordTypeBuildError, recordTypeTestResult, recordTypeMergeResult, "container_died", "branch_acceptance_rejected"}).
		Order("created_at DESC").
		Find(&events).Error; err != nil {
		return nil, err
	}
	for _, e := range events {
		if e.EventType == model.TaskEventQuarantined || e.TaskID == uuid.Nil {
			continue
		}
		if _, ok := out[e.TaskID]; ok {
			continue
		}
		failureType, summary := taskFailureFromEvent(e)
		if failureType == "" || summary == "" {
			continue
		}
		out[e.TaskID] = e
	}
	return out, nil
}

func (m publicReadModel) ListWorkers(ctx context.Context, projectName string) ([]orchdto.WorkerDTO, error) {
	var project model.Project
	err := m.db.WithContext(ctx).Where("name = ?", projectName).First(&project).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return []orchdto.WorkerDTO{}, nil
	}
	if err != nil {
		return nil, err
	}

	var agents []model.Agent
	if err := m.db.WithContext(ctx).
		Where("project_id = ?", project.ID).
		Order("created_at DESC").
		Find(&agents).Error; err != nil {
		return nil, err
	}
	out := make([]orchdto.WorkerDTO, 0, len(agents))
	for _, a := range agents {
		out = append(out, toWorkerDTO(a, m.project.Name))
	}
	return out, nil
}

func (m publicReadModel) GetWorker(ctx context.Context, id uuid.UUID) (orchdto.WorkerDTO, error) {
	var agent model.Agent
	if err := m.db.WithContext(ctx).Where("id = ?", id).First(&agent).Error; err != nil {
		return orchdto.WorkerDTO{}, err
	}
	return toWorkerDTO(agent, m.project.Name), nil
}

func (m publicReadModel) TaskAttempts(ctx context.Context, taskID uuid.UUID) ([]orchdto.WorkerAttemptDTO, error) {
	var task model.Task
	if err := m.db.WithContext(ctx).First(&task, "id = ?", taskID).Error; err != nil {
		return nil, err
	}

	var agents []model.Agent
	agentQuery := m.db.WithContext(ctx).Where("current_task_id = ?", taskID)
	if task.AssignedAgentID != nil {
		agentQuery = agentQuery.Or("id = ?", *task.AssignedAgentID)
	}
	if err := agentQuery.Order("created_at ASC").Find(&agents).Error; err != nil {
		return nil, err
	}

	agentsByID := make(map[string]model.Agent, len(agents))
	for _, a := range agents {
		agentsByID[a.ID.String()] = a
	}

	var spawns []model.TaskEvent
	if err := m.db.WithContext(ctx).
		Where("task_id = ? AND event_type = ?", taskID, "worker_spawned").
		Order("created_at ASC").
		Find(&spawns).Error; err != nil {
		return nil, err
	}

	var durableAttempts []model.WorkerAttempt
	if err := m.db.WithContext(ctx).
		Where("task_id = ?", taskID).
		Order("created_at ASC").
		Find(&durableAttempts).Error; err != nil {
		return nil, err
	}

	out := make([]orchdto.WorkerAttemptDTO, 0, len(durableAttempts)+len(spawns)+len(agents))
	coveredAgents := map[string]struct{}{}
	coveredAttemptIDs := map[string]struct{}{}
	for _, attempt := range durableAttempts {
		agent, hasAgent := model.Agent{}, false
		if attempt.AgentID != nil {
			agent, hasAgent = agentsByID[attempt.AgentID.String()]
			coveredAgents[attempt.AgentID.String()] = struct{}{}
		}
		coveredAttemptIDs[attempt.ID.String()] = struct{}{}
		out = append(out, toWorkerAttemptDTOFromDurable(attempt, agent, hasAgent))
	}
	for _, e := range spawns {
		if _, ok := coveredAttemptIDs[stringField(e.Details, "attempt_id")]; ok {
			continue
		}
		agentID := stringField(e.Details, "agent_id")
		agent, ok := agentsByID[agentID]
		if agentID != "" {
			coveredAgents[agentID] = struct{}{}
		}
		out = append(out, toWorkerAttemptDTOFromSpawn(e, agent, ok))
	}
	for _, a := range agents {
		if _, ok := coveredAgents[a.ID.String()]; ok {
			continue
		}
		out = append(out, toWorkerAttemptDTOFromAgent(taskID, a))
	}
	if len(out) > 0 {
		var failureEvents []model.TaskEvent
		if err := m.db.WithContext(ctx).
			Where("task_id = ? AND event_type IN ?", taskID, []string{recordTypeCrash, recordTypeBuildError, recordTypeTestResult, "container_died", "branch_acceptance_rejected"}).
			Order("created_at ASC").
			Find(&failureEvents).Error; err != nil {
			return nil, err
		}
		applyFailureEvidence(out, failureEvents, task)
	}

	return out, nil
}

// toTaskDTO marshals an internal model.Task into the public TaskDTO
// shape. AssignedAgentID is rendered as an empty string when nil so the
// JSON shape stays stable across assigned/unassigned tasks.
func toTaskDTO(t model.Task, events ...model.TaskEvent) orchdto.TaskDTO {
	assigned := ""
	if t.AssignedAgentID != nil {
		assigned = t.AssignedAgentID.String()
	}
	d := orchdto.TaskDTO{
		ID:             t.ID.String(),
		Title:          t.Title,
		Status:         string(t.Status),
		CreatedAt:      t.CreatedAt,
		UpdatedAt:      t.UpdatedAt,
		AssignedWorker: assigned,
		Category:       string(t.Category),
	}
	d.CurrentHealth = taskCurrentHealth(t)
	applyTaskFailureDiagnostics(&d, t, events...)
	return d
}

func taskCurrentHealth(t model.Task) string {
	if h := firstNonEmpty(
		stringField(t.Context, "current_health"),
		stringField(t.Context, "task_health"),
		stringField(t.Context, "health"),
	); h != "" {
		return h
	}
	if t.Status == model.StatusDone || t.Status == model.StatusCancelled {
		return ""
	}
	if t.Status == model.StatusFailed {
		return "failed"
	}
	if t.NeedsHumanReview || t.Status.IsHumanGate() {
		return "needs_attention"
	}
	return ""
}

func applyTaskFailureDiagnostics(d *orchdto.TaskDTO, t model.Task, events ...model.TaskEvent) {
	summary := firstNonEmpty(
		stringField(t.Context, "latest_failure_summary"),
		stringField(t.Context, "failure_diagnosis"),
		stringField(t.Context, "failure_reason"),
		stringField(t.Context, "merge_failure_reason"),
		stringField(t.Context, "testing_ready_failure"),
		stringField(t.Context, "test_writing_failure"),
		stringField(t.Context, "test_failure_output"),
	)
	failureType := firstNonEmpty(
		stringField(t.Context, "latest_failure_type"),
		stringField(t.Context, "failure_class"),
		stringField(t.Context, "failure_category"),
	)
	var failureAt *time.Time
	if at := timeField(t.Context, "latest_failure_at"); !at.IsZero() {
		failureAt = &at
	}
	for _, e := range events {
		eventType, eventSummary := taskFailureFromEvent(e)
		if eventType == "" || eventSummary == "" {
			continue
		}
		if summary == "" {
			summary = eventSummary
		}
		if failureType == "" {
			failureType = eventType
		}
		at := e.CreatedAt
		failureAt = &at
		break
	}
	if summary == "" {
		return
	}
	d.LatestFailureSummary = boundFailureEvidence(summary)
	d.LatestFailureType = firstNonEmpty(failureType, "task_failure")
	d.LatestFailureAt = failureAt
	current := taskFailureIsCurrent(t, d.CurrentHealth)
	if v, ok := boolField(t.Context, "latest_failure_current"); ok {
		current = v
	}
	d.LatestFailureCurrent = &current
}

func taskFailureFromEvent(e model.TaskEvent) (string, string) {
	switch e.EventType {
	case "branch_acceptance_rejected":
		return "branch_hygiene_gate_failure", firstNonEmpty(stringField(e.Details, "reason"), stringField(e.Details, "error"), e.NewValue)
	case recordTypeMergeResult:
		if success, ok := boolField(e.Details, "success"); ok && success {
			return "", ""
		}
		return "merge_failure", firstNonEmpty(stringField(e.Details, "failure_reason"), e.NewValue)
	}
	return evidenceFromEvent(e)
}

func taskFailureIsCurrent(t model.Task, health string) bool {
	switch health {
	case "failed", "needs_attention", "stuck", "unhealthy", "degraded":
		return true
	}
	switch t.Status {
	case model.StatusFailed, model.StatusRejected, model.StatusPaused:
		return true
	}
	return false
}

func toTaskCommentDTO(c model.TaskComment) orchdto.TaskCommentDTO {
	return orchdto.TaskCommentDTO{
		ID:        c.ID.String(),
		TaskID:    c.TaskID.String(),
		Author:    c.Author,
		Body:      c.Body,
		CreatedAt: c.CreatedAt,
	}
}

// toWorkerDTO marshals an internal model.Agent into the public WorkerDTO
// shape. HeartbeatAt is the "last I saw you alive" timestamp; a nil
// pointer surfaces as the zero time, which is the agreed sentinel.
func toWorkerDTO(a model.Agent, project string) orchdto.WorkerDTO {
	current := ""
	if a.CurrentTaskID != nil {
		current = a.CurrentTaskID.String()
	}
	hb := time.Time{}
	if a.HeartbeatAt != nil {
		hb = *a.HeartbeatAt
	}
	handle := workeridentity.FromAgent(a)
	return orchdto.WorkerDTO{
		ID:                   a.ID.String(),
		ContainerID:          handle.LogContainerID(),
		Project:              project,
		AgentType:            string(a.AgentType),
		Branch:               a.WorktreeBranch,
		Status:               string(a.Status),
		StartedAt:            a.CreatedAt,
		LastHeartbeat:        hb,
		CurrentTask:          current,
		Provider:             a.Provider,
		ModelID:              a.ModelID,
		Effort:               a.Effort,
		CompletedAt:          a.CompletedAt,
		ExitReason:           a.ExitReason,
		TotalCostUSD:         a.TotalCostUSD,
		FinalContextPct:      a.FinalContextPct,
		TokensIn:             a.TokensIn,
		TokensOut:            a.TokensOut,
		ConstraintViolations: a.ConstraintViolations,
	}
}

func toWorkerAttemptDTOFromAgent(taskID uuid.UUID, a model.Agent) orchdto.WorkerAttemptDTO {
	hb := time.Time{}
	if a.HeartbeatAt != nil {
		hb = *a.HeartbeatAt
	}
	handle := workeridentity.FromAgent(a)
	return orchdto.WorkerAttemptDTO{
		AttemptID:            a.ID.String(),
		TaskID:               taskID.String(),
		WorkerID:             a.ID.String(),
		AgentID:              a.ID.String(),
		ContainerID:          handle.LogContainerID(),
		WorkerLabel:          a.Name,
		AgentType:            string(a.AgentType),
		Branch:               a.WorktreeBranch,
		Provider:             a.Provider,
		ModelID:              a.ModelID,
		Effort:               a.Effort,
		Status:               string(a.Status),
		StartedAt:            a.CreatedAt,
		CompletedAt:          a.CompletedAt,
		LastHeartbeat:        hb,
		ExitReason:           a.ExitReason,
		TokensIn:             a.TokensIn,
		TokensOut:            a.TokensOut,
		TotalCostUSD:         a.TotalCostUSD,
		FinalContextPct:      a.FinalContextPct,
		ConstraintViolations: a.ConstraintViolations,
	}
}

func toWorkerAttemptDTOFromDurable(attempt model.WorkerAttempt, a model.Agent, hasAgent bool) orchdto.WorkerAttemptDTO {
	d := orchdto.WorkerAttemptDTO{
		AttemptID:   attempt.ID.String(),
		TaskID:      attempt.TaskID.String(),
		WorkerID:    attempt.WorkerID,
		ContainerID: attempt.ContainerID,
		AgentType:   attempt.AgentType,
		StartedAt:   attempt.CreatedAt,
	}
	if attempt.AgentID != nil {
		d.AgentID = attempt.AgentID.String()
	}
	if hasAgent {
		fromAgent := toWorkerAttemptDTOFromAgent(attempt.TaskID, a)
		fromAgent.AttemptID = d.AttemptID
		fromAgent.WorkerID = firstNonEmpty(d.WorkerID, fromAgent.WorkerID)
		fromAgent.AgentID = firstNonEmpty(d.AgentID, fromAgent.AgentID)
		fromAgent.ContainerID = firstNonEmpty(d.ContainerID, fromAgent.ContainerID)
		fromAgent.AgentType = firstNonEmpty(d.AgentType, fromAgent.AgentType)
		fromAgent.StartedAt = d.StartedAt
		return fromAgent
	}
	return d
}

func toTaskAttemptLeaseDTO(attempt model.WorkerAttempt) orchdto.TaskAttemptLeaseDTO {
	d := orchdto.TaskAttemptLeaseDTO{
		AttemptID:   attempt.ID.String(),
		TaskID:      attempt.TaskID.String(),
		WorkerID:    attempt.WorkerID,
		ContainerID: attempt.ContainerID,
		Role:        attempt.AgentType,
		Branch:      attempt.Branch,
		LeaseState:  attempt.State,
		StartedAt:   attempt.CreatedAt,
		UpdatedAt:   attempt.UpdatedAt,
	}
	if attempt.AgentID != nil {
		d.AgentID = attempt.AgentID.String()
	}
	return d
}

func toWorkerAttemptDTOFromSpawn(e model.TaskEvent, a model.Agent, hasAgent bool) orchdto.WorkerAttemptDTO {
	d := orchdto.WorkerAttemptDTO{
		AttemptID:   firstNonEmpty(stringField(e.Details, "attempt_id"), e.ID.String()),
		TaskID:      e.TaskID.String(),
		WorkerID:    firstNonEmpty(stringField(e.Details, "worker_id"), stringField(e.Details, "agent_id")),
		AgentID:     stringField(e.Details, "agent_id"),
		ContainerID: stringField(e.Details, "container_id"),
		WorkerLabel: firstNonEmpty(stringField(e.Details, "worker_label"), stringField(e.Details, "worker_id")),
		AgentType:   firstNonEmpty(stringField(e.Details, "agent_type"), e.NewValue),
		StartedAt:   e.CreatedAt,
	}
	if hasAgent {
		fromAgent := toWorkerAttemptDTOFromAgent(e.TaskID, a)
		fromAgent.AttemptID = d.AttemptID
		fromAgent.WorkerID = firstNonEmpty(d.WorkerID, fromAgent.WorkerID)
		fromAgent.AgentID = firstNonEmpty(d.AgentID, fromAgent.AgentID)
		fromAgent.ContainerID = firstNonEmpty(d.ContainerID, fromAgent.ContainerID)
		fromAgent.WorkerLabel = firstNonEmpty(d.WorkerLabel, fromAgent.WorkerLabel)
		fromAgent.AgentType = firstNonEmpty(d.AgentType, fromAgent.AgentType)
		fromAgent.StartedAt = d.StartedAt
		return fromAgent
	}
	return d
}

func applyFailureEvidence(attempts []orchdto.WorkerAttemptDTO, events []model.TaskEvent, task model.Task) {
	taskFailure := stringField(task.Context, "failure_reason")
	for i := range attempts {
		if classification, firstError := evidenceFromExitReason(attempts[i].ExitReason); classification != "" {
			attempts[i].FailureClassification = classification
			attempts[i].FirstError = boundFailureEvidence(firstError)
		}
		for _, e := range events {
			if len(attempts) > 1 && !attemptMatchesEvent(attempts[i], e) {
				continue
			}
			classification, firstError := evidenceFromEvent(e)
			if classification == "" {
				continue
			}
			attempts[i].FailureClassification = classification
			attempts[i].FirstError = boundFailureEvidence(firstError)
			break
		}
		if attempts[i].FailureClassification == "" && taskFailure != "" {
			attempts[i].FailureClassification = "task_failure"
			attempts[i].FirstError = boundFailureEvidence(taskFailure)
		}
	}
}

func evidenceFromExitReason(reason string) (string, string) {
	reason = strings.TrimSpace(reason)
	if reason == "" || reason == "success" || reason == "completed" {
		return "", ""
	}
	return "exit_reason", reason
}

func evidenceFromEvent(e model.TaskEvent) (string, string) {
	switch e.EventType {
	case recordTypeCrash:
		return "crash", firstNonEmpty(stringField(e.Details, "reason"), e.NewValue)
	case "container_died":
		if exitCode, ok := intField(e.Details, "exit_code"); ok && exitCode == 0 && !jsonBoolField(e.Details, "oom_killed") {
			return "", ""
		}
		return "container_exit", firstNonEmpty(stringField(e.Details, "normalized_reason"), e.NewValue)
	case recordTypeBuildError:
		return "build_error", firstNonEmpty(stringField(e.Details, "message"), e.NewValue)
	case "branch_acceptance_rejected":
		return "branch_hygiene_gate_failure", firstNonEmpty(stringField(e.Details, "reason"), stringField(e.Details, "error"), e.NewValue)
	case recordTypeTestResult:
		if success, ok := boolField(e.Details, "success"); ok && success {
			return "", ""
		}
		return "test_failure", firstNonEmpty(stringField(e.Details, "summary"), e.NewValue)
	}
	return "", ""
}

func intField(fields model.JSONField, key string) (int, bool) {
	if fields == nil {
		return 0, false
	}
	switch v := fields[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func timeField(fields model.JSONField, key string) time.Time {
	if fields == nil {
		return time.Time{}
	}
	s, _ := fields[key].(string)
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func attemptMatchesEvent(a orchdto.WorkerAttemptDTO, e model.TaskEvent) bool {
	selectors := map[string]struct{}{}
	addSelector(selectors, a.WorkerID)
	addSelector(selectors, a.AgentID)
	addSelector(selectors, a.ContainerID)
	addSelector(selectors, a.WorkerLabel)
	return eventMatchesSelectors(e, selectors)
}

func eventMatchesSelectors(e model.TaskEvent, selectors map[string]struct{}) bool {
	if len(selectors) == 0 {
		return false
	}
	if _, ok := selectors[e.Actor]; ok && e.Actor != "" {
		return true
	}
	if _, ok := selectors[e.OldValue]; ok && e.OldValue != "" {
		return true
	}
	for _, key := range []string{"agent_id", "worker_id", "container_id", "worker_label"} {
		if v := stringField(e.Details, key); v != "" {
			if _, ok := selectors[v]; ok {
				return true
			}
		}
	}
	return false
}

func boundFailureEvidence(s string) string {
	s = strings.TrimSpace(s)
	for _, pattern := range secretEvidencePatterns {
		s = pattern.ReplaceAllString(s, `${1}[REDACTED]`)
	}
	if len(s) <= maxWorkerAttemptFirstErrorLen {
		return s
	}
	return s[:maxWorkerAttemptFirstErrorLen]
}

func boolField(m map[string]any, key string) (bool, bool) {
	v, ok := m[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func mapKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if strings.TrimSpace(k) != "" {
			out = append(out, k)
		}
	}
	return out
}

func uuidMapKeys(m map[uuid.UUID]struct{}) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		if k != uuid.Nil {
			out = append(out, k)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
