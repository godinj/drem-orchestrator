package orchhttp

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

const inferenceUsageEventType = "inference_usage"

var taskReportPhaseOrder = []string{
	"planning", "plan_review", "test", "worker", "verification",
	"host_rework", "integration", "terminal",
}

func (m publicReadModel) TaskReport(ctx context.Context, taskID uuid.UUID) (orchdto.TaskReportDTO, error) {
	var task model.Task
	if err := m.db.WithContext(ctx).First(&task, "id = ?", taskID).Error; err != nil {
		return orchdto.TaskReportDTO{}, err
	}

	var children []model.Task
	if err := m.db.WithContext(ctx).Where("parent_task_id = ?", task.ID).
		Order("created_at ASC").Find(&children).Error; err != nil {
		return orchdto.TaskReportDTO{}, err
	}
	taskIDs := []uuid.UUID{task.ID}
	childDTOs := make([]orchdto.TaskReportChildDTO, 0, len(children))
	for _, child := range children {
		taskIDs = append(taskIDs, child.ID)
		childDTOs = append(childDTOs, orchdto.TaskReportChildDTO{
			ID: child.ID.String(), Title: child.Title, Status: string(child.Status),
			Phase: child.Phase, CreatedAt: child.CreatedAt, UpdatedAt: child.UpdatedAt,
		})
	}

	attempts, err := m.reportAttempts(ctx, taskIDs)
	if err != nil {
		return orchdto.TaskReportDTO{}, err
	}
	var events []model.TaskEvent
	if err := m.db.WithContext(ctx).Where("task_id IN ?", taskIDs).
		Order("created_at ASC").Find(&events).Error; err != nil {
		return orchdto.TaskReportDTO{}, err
	}

	now := time.Now().UTC()
	phaseByName := phaseDurations(task, events, now)
	totals, coverage := summarizeAttempts(attempts, phaseByName)
	applyInferenceUsage(events, phaseByName, &totals, &coverage)

	if err := m.reportEvidenceCounts(ctx, task.ID, &totals); err != nil {
		return orchdto.TaskReportDTO{}, err
	}
	codexGoals, err := m.reportCodexGoalUsage(ctx, task.ID, &totals)
	if err != nil {
		return orchdto.TaskReportDTO{}, err
	}
	coverage.ExternalCodexMeasured = len(codexGoals) > 0
	coverage.UnmeasuredInferenceRuns = coverage.EligibleInferenceRuns - coverage.MeasuredInferenceRuns
	if coverage.EligibleInferenceRuns > 0 {
		coverage.Percent = math.Round(float64(coverage.MeasuredInferenceRuns)*10000/
			float64(coverage.EligibleInferenceRuns)) / 100
	}

	finish := task.UpdatedAt
	if !taskReportTerminal(task.Status) {
		finish = now
	}
	if finish.Before(task.CreatedAt) {
		finish = task.CreatedAt
	}
	report := orchdto.TaskReportDTO{
		Project: m.project.Name, Task: toTaskDTO(task), GeneratedAt: now,
		WallDurationMS: finish.Sub(task.CreatedAt).Milliseconds(),
		Children:       childDTOs, Phases: orderedPhases(phaseByName), Attempts: attempts,
		CodexGoals: codexGoals,
		Totals:     totals, MeasurementCoverage: coverage,
	}
	if coverage.UnmeasuredInferenceRuns > 0 {
		report.Warnings = append(report.Warnings,
			"one or more terminal inference runs predate token instrumentation or lack a terminal usage summary")
	}
	if (totals.HostReworkSessions > 0 || totals.VerificationRuns > 0) && !coverage.ExternalCodexMeasured {
		report.Warnings = append(report.Warnings,
			"supervising Codex goal usage was not submitted; subscription inference is not measurable for this task")
	}
	if totals.VerificationRuns > 0 && totals.ComputerUseRuns == 0 {
		report.Warnings = append(report.Warnings,
			"verification exists without structured Computer Use interaction evidence")
	}
	return report, nil
}

func (m publicReadModel) reportCodexGoalUsage(ctx context.Context, taskID uuid.UUID, totals *orchdto.TaskReportTotalsDTO) ([]orchdto.CodexGoalUsageDTO, error) {
	var rows []model.CodexGoalUsage
	if err := m.db.WithContext(ctx).Where("task_id = ?", taskID).
		Order("created_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]orchdto.CodexGoalUsageDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, codexGoalUsageDTO(row))
		totals.CodexGoalCount++
		totals.CodexTokensUsed += row.TokensUsed
		totals.CodexElapsedMS += row.ElapsedMS
	}
	return out, nil
}

func (m publicReadModel) reportAttempts(ctx context.Context, taskIDs []uuid.UUID) ([]orchdto.WorkerAttemptDTO, error) {
	var durable []model.WorkerAttempt
	if err := m.db.WithContext(ctx).Where("task_id IN ?", taskIDs).
		Order("created_at ASC").Find(&durable).Error; err != nil {
		return nil, err
	}
	agentIDs := make([]uuid.UUID, 0, len(durable))
	for _, attempt := range durable {
		if attempt.AgentID != nil {
			agentIDs = append(agentIDs, *attempt.AgentID)
		}
	}
	agents := map[uuid.UUID]model.Agent{}
	if len(agentIDs) > 0 {
		var rows []model.Agent
		if err := m.db.WithContext(ctx).Where("id IN ?", agentIDs).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, agent := range rows {
			agents[agent.ID] = agent
		}
	}
	out := make([]orchdto.WorkerAttemptDTO, 0, len(durable))
	for _, attempt := range durable {
		agent, ok := model.Agent{}, false
		if attempt.AgentID != nil {
			agent, ok = agents[*attempt.AgentID]
		}
		out = append(out, toWorkerAttemptDTOFromDurable(attempt, agent, ok))
	}
	return out, nil
}

func (m publicReadModel) reportEvidenceCounts(ctx context.Context, taskID uuid.UUID, totals *orchdto.TaskReportTotalsDTO) error {
	counts := []struct {
		model any
		out   *int
	}{
		{&model.DeliveryArtifact{}, &totals.ArtifactVersions},
		{&model.VerificationRecord{}, &totals.VerificationRuns},
		{&model.VerificationInteraction{}, &totals.ComputerUseRuns},
		{&model.HostReworkSession{}, &totals.HostReworkSessions},
		{&model.HostReworkSubmission{}, &totals.HostReworkSubmissions},
	}
	for _, item := range counts {
		var count int64
		if err := m.db.WithContext(ctx).Model(item.model).Where("task_id = ?", taskID).Count(&count).Error; err != nil {
			return err
		}
		*item.out = int(count)
	}
	return nil
}

func summarizeAttempts(attempts []orchdto.WorkerAttemptDTO, phases map[string]*orchdto.TaskReportPhaseDTO) (orchdto.TaskReportTotalsDTO, orchdto.TaskReportCoverageDTO) {
	var totals orchdto.TaskReportTotalsDTO
	var coverage orchdto.TaskReportCoverageDTO
	worker := ensurePhase(phases, "worker")
	for _, attempt := range attempts {
		totals.WorkerAttempts++
		totals.TokensIn += attempt.TokensIn
		totals.TokensOut += attempt.TokensOut
		worker.TokensIn += attempt.TokensIn
		worker.TokensOut += attempt.TokensOut
		switch attempt.LeaseState {
		case model.WorkerAttemptCompleted:
			totals.CompletedAttempts++
		case model.WorkerAttemptFailed:
			totals.FailedAttempts++
		case model.WorkerAttemptAborted, model.WorkerAttemptSuperseded:
			totals.AbortedAttempts++
		}
		if attempt.LeaseState != model.WorkerAttemptCompleted && attempt.LeaseState != model.WorkerAttemptFailed {
			continue
		}
		if attempt.FailureClassification == "worker_image_unavailable" {
			continue
		}
		coverage.EligibleInferenceRuns++
		worker.InferenceRuns++
		if attempt.TokensIn > 0 || attempt.TokensOut > 0 {
			coverage.MeasuredInferenceRuns++
		}
	}
	return totals, coverage
}

func applyInferenceUsage(events []model.TaskEvent, phases map[string]*orchdto.TaskReportPhaseDTO, totals *orchdto.TaskReportTotalsDTO, coverage *orchdto.TaskReportCoverageDTO) {
	for _, event := range events {
		if event.EventType != inferenceUsageEventType {
			continue
		}
		phase := stringDetail(event.Details, "phase")
		if phase == "" {
			phase = "planning"
		}
		tokensIn := intDetail(event.Details, "tokens_in")
		tokensOut := intDetail(event.Details, "tokens_out")
		bucket := ensurePhase(phases, phase)
		bucket.TokensIn += tokensIn
		bucket.TokensOut += tokensOut
		bucket.InferenceRuns++
		totals.TokensIn += tokensIn
		totals.TokensOut += tokensOut
		coverage.EligibleInferenceRuns++
		if tokensIn > 0 || tokensOut > 0 {
			coverage.MeasuredInferenceRuns++
		}
	}
}

func phaseDurations(task model.Task, events []model.TaskEvent, now time.Time) map[string]*orchdto.TaskReportPhaseDTO {
	phases := map[string]*orchdto.TaskReportPhaseDTO{}
	parentEvents := make([]model.TaskEvent, 0, len(events))
	for _, event := range events {
		if event.TaskID == task.ID && event.NewValue != "" {
			parentEvents = append(parentEvents, event)
		}
	}
	sort.Slice(parentEvents, func(i, j int) bool { return parentEvents[i].CreatedAt.Before(parentEvents[j].CreatedAt) })
	status := string(task.Status)
	if len(parentEvents) > 0 {
		if parentEvents[0].OldValue != "" {
			status = parentEvents[0].OldValue
		} else {
			status = parentEvents[0].NewValue
		}
	}
	phase := phaseForStatus(status)
	ensurePhase(phases, phase).Visits++
	cursor := task.CreatedAt
	for _, event := range parentEvents {
		if event.CreatedAt.After(cursor) {
			ensurePhase(phases, phase).DurationMS += event.CreatedAt.Sub(cursor).Milliseconds()
		}
		newPhase := phaseForStatus(event.NewValue)
		if newPhase != phase {
			ensurePhase(phases, newPhase).Visits++
		}
		phase = newPhase
		cursor = event.CreatedAt
	}
	finish := task.UpdatedAt
	if !taskReportTerminal(task.Status) {
		finish = now
	}
	if finish.After(cursor) {
		ensurePhase(phases, phase).DurationMS += finish.Sub(cursor).Milliseconds()
	}
	return phases
}

func phaseForStatus(status string) string {
	switch model.TaskStatus(status) {
	case model.StatusClassifying, model.StatusBacklog, model.StatusPlanning, model.StatusNeedsClarification:
		return "planning"
	case model.StatusPlanReview:
		return "plan_review"
	case model.StatusTestWriting, model.StatusTestReview:
		return "test"
	case model.StatusInProgress:
		return "worker"
	case model.StatusTestingReady, model.StatusVerificationReady:
		return "verification"
	case model.StatusHostRework:
		return "host_rework"
	case model.StatusIntegrationReady, model.StatusMerging, model.StatusDone:
		return "integration"
	default:
		return "terminal"
	}
}

func taskReportTerminal(status model.TaskStatus) bool {
	switch status {
	case model.StatusIntegrationReady, model.StatusDone, model.StatusFailed, model.StatusRejected, model.StatusCancelled:
		return true
	default:
		return false
	}
}

func ensurePhase(phases map[string]*orchdto.TaskReportPhaseDTO, name string) *orchdto.TaskReportPhaseDTO {
	if phase := phases[name]; phase != nil {
		return phase
	}
	phase := &orchdto.TaskReportPhaseDTO{Name: name}
	phases[name] = phase
	return phase
}

func orderedPhases(phases map[string]*orchdto.TaskReportPhaseDTO) []orchdto.TaskReportPhaseDTO {
	out := make([]orchdto.TaskReportPhaseDTO, 0, len(phases))
	for _, name := range taskReportPhaseOrder {
		phase := phases[name]
		if phase == nil || (phase.DurationMS == 0 && phase.Visits == 0 && phase.InferenceRuns == 0) {
			continue
		}
		out = append(out, *phase)
	}
	return out
}

func stringDetail(details model.JSONField, key string) string {
	value, _ := details[key].(string)
	return strings.TrimSpace(value)
}

func intDetail(details model.JSONField, key string) int {
	switch value := details[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func (s *Server) handleTaskReport(w http.ResponseWriter, r *http.Request) {
	if !s.requireProject(w, r) {
		return
	}
	taskID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid task id: "+r.PathValue("id"))
		return
	}
	report, err := (publicReadModel{db: s.DB, project: s.Project}).TaskReport(r.Context(), taskID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeJSONError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}
