package orchhttp_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

func TestHealthIssuesReportsStaleAssignmentAndPlannerCapacity(t *testing.T) {
	fake, project, _, baseURL := setupGateHTTPTest(t)
	db := fake.db

	now := time.Now().Add(-time.Hour)
	task := testutil.CreateTask(t, db, project.ID, "stale", model.StatusInProgress)
	agent := model.Agent{ID: uuid.New(), ProjectID: project.ID, AgentType: model.AgentCoder, Status: model.AgentDead, CurrentTaskID: &task.ID, HeartbeatAt: &now}
	require.NoError(t, db.Create(&agent).Error)
	task.AssignedAgentID = &agent.ID
	require.NoError(t, db.Save(&task).Error)

	planner := testutil.CreateTask(t, db, project.ID, "planner cap", model.StatusPlanning)
	planner.Context = model.JSONField{"planner_capacity_exhausted": true, "planner_capacity_message": "cap reached"}
	require.NoError(t, db.Save(&planner).Error)

	resp, body := doJSON(t, http.MethodGet, baseURL+"/projects/"+projectName+"/health/issues", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var issues []orchdto.HealthIssueDTO
	require.NoError(t, json.Unmarshal(body, &issues))
	requireIssueType(t, issues, "stale_assigned_worker")
	requireIssueType(t, issues, "planner_capacity_exhausted")
}

func TestHealthIssuesDoesNotReportMissingFailureEvidenceForSupportedContextEvidence(t *testing.T) {
	fake, project, _, baseURL := setupGateHTTPTest(t)
	db := fake.db
	task := testutil.CreateTask(t, db, project.ID, "failed with evidence", model.StatusFailed)
	task.Context = model.JSONField{
		"latest_failure_type":    "tool_loop",
		"latest_failure_summary": "retry budget exhausted for tool loop",
		"failure_reason":         "retry budget exhausted",
	}
	require.NoError(t, db.Save(&task).Error)

	resp, body := doJSON(t, http.MethodGet, baseURL+"/projects/"+projectName+"/health/issues", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var issues []orchdto.HealthIssueDTO
	require.NoError(t, json.Unmarshal(body, &issues))
	requireNoIssueType(t, issues, "missing_failure_evidence")
}

func TestHealthIssuesReportsFailedTaskWithActiveAttemptRecoveryAction(t *testing.T) {
	fake, project, _, baseURL := setupGateHTTPTest(t)
	db := fake.db
	failed := testutil.CreateTask(t, db, project.ID, "failed with live attempt", model.StatusFailed)
	attemptID := uuid.New()
	require.NoError(t, db.Create(&model.WorkerAttempt{
		ID:        attemptID,
		TaskID:    failed.ID,
		WorkerID:  "worker-failed-active",
		AgentType: string(model.AgentCoder),
		Branch:    "feature/failed-active",
		State:     model.WorkerAttemptRunning,
	}).Error)

	resp, body := doJSON(t, http.MethodGet, baseURL+"/projects/"+projectName+"/health/issues", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var issues []orchdto.HealthIssueDTO
	require.NoError(t, json.Unmarshal(body, &issues))
	issue := findIssueType(t, issues, "failed_task_active_attempt")
	require.Equal(t, failed.ID.String(), issue.TaskID)
	require.Equal(t, []string{attemptID.String()}, issue.AttemptIDs)
	require.Contains(t, issue.Message, "failed task still has 1 reserved/running worker attempt")
	require.Contains(t, issue.RecommendedAction, "dremctl recover exited-container "+failed.ID.String()+" --dry-run")
}

func TestHealthIssuesReportsMissingAssignedAgentRecoveryAction(t *testing.T) {
	fake, project, _, baseURL := setupGateHTTPTest(t)
	db := fake.db
	task := testutil.CreateTask(t, db, project.ID, "missing assigned agent", model.StatusInProgress)
	missingAgentID := uuid.New()
	task.AssignedAgentID = &missingAgentID
	require.NoError(t, db.Save(&task).Error)

	resp, body := doJSON(t, http.MethodGet, baseURL+"/projects/"+projectName+"/health/issues", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var issues []orchdto.HealthIssueDTO
	require.NoError(t, json.Unmarshal(body, &issues))
	issue := findIssueType(t, issues, "stale_assigned_worker")
	require.Equal(t, task.ID.String(), issue.TaskID)
	require.Equal(t, missingAgentID.String(), issue.WorkerID)
	require.Contains(t, issue.Message, "assigned worker row is missing")
	require.Contains(t, issue.RecommendedAction, "dremctl recover stale-assignment "+task.ID.String()+" --dry-run")
}

func TestHealthIssuesDoesNotRecommendDeadAssignedAgentRecoveryForTerminalTask(t *testing.T) {
	fake, project, _, baseURL := setupGateHTTPTest(t)
	db := fake.db
	task := testutil.CreateTask(t, db, project.ID, "terminal with dead assignment", model.StatusDone)
	heartbeat := time.Now().Add(-time.Hour)
	agent := model.Agent{ID: uuid.New(), ProjectID: project.ID, AgentType: model.AgentCoder, Status: model.AgentDead, CurrentTaskID: &task.ID, HeartbeatAt: &heartbeat}
	require.NoError(t, db.Create(&agent).Error)
	task.AssignedAgentID = &agent.ID
	require.NoError(t, db.Save(&task).Error)

	resp, body := doJSON(t, http.MethodGet, baseURL+"/projects/"+projectName+"/health/issues", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var issues []orchdto.HealthIssueDTO
	require.NoError(t, json.Unmarshal(body, &issues))
	requireNoIssueType(t, issues, "dead_assigned_agent")
}

func TestHealthIssuesReportsLegacyDependencyFailureStall(t *testing.T) {
	fake, project, _, baseURL := setupGateHTTPTest(t)
	parent := testutil.CreateTask(t, fake.db, project.ID, "stalled parent", model.StatusInProgress)
	failed := testutil.CreateTask(t, fake.db, project.ID, "failed implementation", model.StatusFailed)
	failed.ParentTaskID = &parent.ID
	require.NoError(t, fake.db.Save(&failed).Error)
	blocked := testutil.CreateTask(t, fake.db, project.ID, "blocked integration", model.StatusBacklog)
	blocked.ParentTaskID = &parent.ID
	blocked.DependencyIDs = model.JSONArray{failed.ID.String()}
	require.NoError(t, fake.db.Save(&blocked).Error)

	resp, body := doJSON(t, http.MethodGet, baseURL+"/projects/"+projectName+"/health/issues", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var issues []orchdto.HealthIssueDTO
	require.NoError(t, json.Unmarshal(body, &issues))
	issue := findIssueType(t, issues, "dependency_failure_stall")
	require.Equal(t, "critical", issue.Severity)
	require.Equal(t, failed.ID.String(), issue.BlockedDependencies[0].DependencyID)
	require.Equal(t, blocked.ID.String(), issue.BlockedDependencies[0].TaskID)
}

func TestHealthIssuesReportsDuplicateAttemptsBlockedDependenciesAndBranchGate(t *testing.T) {
	fake, project, _, baseURL := setupGateHTTPTest(t)
	db := fake.db
	task := testutil.CreateTask(t, db, project.ID, "duplicates", model.StatusInProgress)
	require.NoError(t, db.Exec("DROP INDEX IF EXISTS idx_worker_attempt_active_task_role_branch").Error)
	for i := 0; i < 2; i++ {
		require.NoError(t, db.Create(&model.WorkerAttempt{
			ID:        uuid.New(),
			TaskID:    task.ID,
			WorkerID:  "worker-dup",
			AgentType: string(model.AgentCoder),
			Branch:    "feature/dup",
			State:     model.WorkerAttemptRunning,
		}).Error)
	}

	parent := testutil.CreateTask(t, db, project.ID, "parent blocked", model.StatusTestWriting)
	parent.Context = model.JSONField{
		"parent_readiness_blockers": "dependency-blocked: subtask 11111111-1111-1111-1111-111111111111 (phase \"test\") depends on 22222222-2222-2222-2222-222222222222 (phase \"implementation\", status in_progress)",
	}
	require.NoError(t, db.Save(&parent).Error)

	gate := testutil.CreateTask(t, db, project.ID, "gate failed", model.StatusTestingReady)
	require.NoError(t, db.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    gate.ID,
		EventType: "branch_acceptance_rejected",
		Details: model.JSONField{
			"reason": "branch_contamination",
		},
		CreatedAt: time.Now(),
		Actor:     "orchestrator",
	}).Error)
	historicalGate := testutil.CreateTask(t, db, project.ID, "gate recovered", model.StatusIntegrationReady)
	historicalGate.Context = model.JSONField{"latest_failure_current": false}
	require.NoError(t, db.Save(&historicalGate).Error)
	require.NoError(t, db.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    historicalGate.ID,
		EventType: "branch_acceptance_rejected",
		Details:   model.JSONField{"reason": "branch_contamination"},
		CreatedAt: time.Now(),
		Actor:     "orchestrator",
	}).Error)
	acceptedGate := testutil.CreateTask(t, db, project.ID, "gate accepted", model.StatusIntegrationReady)
	acceptedGate.Context = model.JSONField{"branch_acceptance_reason": "accepted_parent_delivery_candidate"}
	require.NoError(t, db.Save(&acceptedGate).Error)
	require.NoError(t, db.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    acceptedGate.ID,
		EventType: "branch_acceptance_rejected",
		Details:   model.JSONField{"reason": "accepted_parent_delivery_candidate"},
		CreatedAt: time.Now(),
		Actor:     "orchestrator",
	}).Error)

	resp, body := doJSON(t, http.MethodGet, baseURL+"/projects/"+projectName+"/health/issues", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var issues []orchdto.HealthIssueDTO
	require.NoError(t, json.Unmarshal(body, &issues))
	duplicate := findIssueType(t, issues, "duplicate_active_attempts")
	require.Equal(t, string(model.AgentCoder), duplicate.Role)
	require.Equal(t, "feature/dup", duplicate.Branch)
	require.Len(t, duplicate.AttemptIDs, 2)
	blocked := findIssueType(t, issues, "parent_readiness_blocked")
	require.Len(t, blocked.BlockedDependencies, 1)
	require.Equal(t, "22222222-2222-2222-2222-222222222222", blocked.BlockedDependencies[0].DependencyID)
	gateIssue := findIssueType(t, issues, "branch_hygiene_gate_failure")
	require.NotNil(t, gateIssue.GateFailure)
	require.Equal(t, "branch_hygiene", gateIssue.GateFailure.Gate)
	for _, issue := range issues {
		require.NotEqual(t, historicalGate.ID.String(), issue.TaskID, "historical recovered gate must not remain a health issue")
		require.NotEqual(t, acceptedGate.ID.String(), issue.TaskID, "successful branch acceptance must not be reported as a gate failure")
	}
}

func TestRecoverStaleAssignmentRefusesFreshWorkingWorker(t *testing.T) {
	fake, project, _, baseURL := setupGateHTTPTest(t)
	db := fake.db
	task := testutil.CreateTask(t, db, project.ID, "live", model.StatusInProgress)
	heartbeat := time.Now()
	agent := model.Agent{ID: uuid.New(), ProjectID: project.ID, AgentType: model.AgentCoder, Status: model.AgentWorking, CurrentTaskID: &task.ID, HeartbeatAt: &heartbeat}
	require.NoError(t, db.Create(&agent).Error)
	task.AssignedAgentID = &agent.ID
	require.NoError(t, db.Save(&task).Error)

	resp, body := doJSON(t, http.MethodPost, baseURL+"/projects/"+projectName+"/tasks/"+task.ID.String()+"/recover/stale-assignment", `{"dry_run":true}`)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	require.Contains(t, decodeErr(t, body), "active worker")
}

func TestRecoverStaleAssignmentCurrentAttemptExitOutranksFreshHeartbeat(t *testing.T) {
	fake, project, _, baseURL := setupGateHTTPTest(t)
	db := fake.db
	task := testutil.CreateTask(t, db, project.ID, "exited", model.StatusInProgress)
	heartbeat := time.Now()
	agent := model.Agent{ID: uuid.New(), ProjectID: project.ID, AgentType: model.AgentCoder, Status: model.AgentWorking, CurrentTaskID: &task.ID, HeartbeatAt: &heartbeat, TmuxSession: "c-exited"}
	require.NoError(t, db.Create(&agent).Error)
	task.AssignedAgentID = &agent.ID
	require.NoError(t, db.Save(&task).Error)
	attempt := model.WorkerAttempt{
		ID:          uuid.New(),
		TaskID:      task.ID,
		AgentID:     &agent.ID,
		WorkerID:    "worker-exited",
		ContainerID: "c-exited",
		AgentType:   string(model.AgentCoder),
		State:       model.WorkerAttemptRunning,
	}
	require.NoError(t, db.Create(&attempt).Error)
	require.NoError(t, db.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    task.ID,
		EventType: "container_died",
		NewValue:  "tool_exit_nonzero",
		Details: model.JSONField{
			"attempt_id":        attempt.ID.String(),
			"container_id":      "c-exited",
			"worker_id":         "worker-exited",
			"exit_code":         float64(1),
			"normalized_reason": "tool_exit_nonzero",
		},
		Actor:     "docker-events",
		CreatedAt: time.Now(),
	}).Error)

	url := baseURL + "/projects/" + projectName + "/tasks/" + task.ID.String() + "/recover/stale-assignment"
	resp, body := doJSON(t, http.MethodPost, url, `{"dry_run":true}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var dry orchdto.StaleAssignmentRecoveryDTO
	require.NoError(t, json.Unmarshal(body, &dry))
	require.True(t, dry.Safe)
	require.Equal(t, "current_attempt_exited", dry.Classification)
	require.Contains(t, dry.Message, "outranks heartbeat")
}

func TestRecoverStaleAssignmentRefusalMessagesDistinguishMissingEvidenceAndSafetyPolicy(t *testing.T) {
	fake, project, _, baseURL := setupGateHTTPTest(t)
	db := fake.db
	unassigned := testutil.CreateTask(t, db, project.ID, "unassigned", model.StatusInProgress)

	resp, body := doJSON(t, http.MethodPost, baseURL+"/projects/"+projectName+"/tasks/"+unassigned.ID.String()+"/recover/stale-assignment", `{"dry_run":true}`)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	require.Contains(t, decodeErr(t, body), "missing evidence")

	terminal := testutil.CreateTask(t, db, project.ID, "terminal", model.StatusDone)
	heartbeat := time.Now().Add(-time.Hour)
	agent := model.Agent{ID: uuid.New(), ProjectID: project.ID, AgentType: model.AgentCoder, Status: model.AgentWorking, CurrentTaskID: &terminal.ID, HeartbeatAt: &heartbeat}
	require.NoError(t, db.Create(&agent).Error)
	terminal.AssignedAgentID = &agent.ID
	require.NoError(t, db.Save(&terminal).Error)

	resp, body = doJSON(t, http.MethodPost, baseURL+"/projects/"+projectName+"/tasks/"+terminal.ID.String()+"/recover/stale-assignment", `{"dry_run":true}`)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	require.Contains(t, decodeErr(t, body), "safety policy")
}

func TestRecoverStaleAssignmentDryRunAndApplyClearsDeadWorker(t *testing.T) {
	fake, project, _, baseURL := setupGateHTTPTest(t)
	db := fake.db
	task := testutil.CreateTask(t, db, project.ID, "dead", model.StatusInProgress)
	heartbeat := time.Now().Add(-time.Hour)
	agent := model.Agent{ID: uuid.New(), ProjectID: project.ID, AgentType: model.AgentCoder, Status: model.AgentDead, CurrentTaskID: &task.ID, HeartbeatAt: &heartbeat}
	require.NoError(t, db.Create(&agent).Error)
	task.AssignedAgentID = &agent.ID
	require.NoError(t, db.Save(&task).Error)

	url := baseURL + "/projects/" + projectName + "/tasks/" + task.ID.String() + "/recover/stale-assignment"
	resp, body := doJSON(t, http.MethodPost, url, `{"dry_run":true}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var dry orchdto.StaleAssignmentRecoveryDTO
	require.NoError(t, json.Unmarshal(body, &dry))
	require.True(t, dry.Safe)
	require.False(t, dry.Applied)

	resp, body = doJSON(t, http.MethodPost, url, `{"apply":true,"actor":"codex:operator:test"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var applied orchdto.StaleAssignmentRecoveryDTO
	require.NoError(t, json.Unmarshal(body, &applied))
	require.True(t, applied.Applied)

	var updated model.Task
	require.NoError(t, db.First(&updated, "id = ?", task.ID).Error)
	require.Nil(t, updated.AssignedAgentID)
	require.Equal(t, task.StateVersion+1, updated.StateVersion)
	var updatedAgent model.Agent
	require.NoError(t, db.First(&updatedAgent, "id = ?", agent.ID).Error)
	require.Nil(t, updatedAgent.CurrentTaskID)
	var events int64
	require.NoError(t, db.Model(&model.TaskEvent{}).Where("task_id = ? AND event_type = ?", task.ID, "stale_assignment_recovered").Count(&events).Error)
	require.Equal(t, int64(1), events)
	var mutations int64
	require.NoError(t, db.Model(&model.TaskMutationRecord{}).Where("task_id = ? AND operation LIKE ? AND outcome = ?", task.ID, "recover-stale-assignment%", "succeeded").Count(&mutations).Error)
	require.Equal(t, int64(1), mutations)
}

func TestRecoverContaminatedBranchFailGateMarksTaskFailed(t *testing.T) {
	fake, project, _, baseURL := setupGateHTTPTest(t)
	db := fake.db
	task := testutil.CreateTask(t, db, project.ID, "contaminated", model.StatusTestingReady)
	task.Context = model.JSONField{"branch_hygiene_failure": "worker trace file committed"}
	require.NoError(t, db.Save(&task).Error)

	url := baseURL + "/projects/" + projectName + "/tasks/" + task.ID.String() + "/recover/contaminated-branch-fail-gate"
	resp, body := doJSON(t, http.MethodPost, url, `{"dry_run":true}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var dry orchdto.TaskRecoveryDTO
	require.NoError(t, json.Unmarshal(body, &dry))
	require.True(t, dry.Safe)
	require.Contains(t, dry.Message, "would mark")

	resp, body = doJSON(t, http.MethodPost, url, `{"apply":true,"actor":"codex:operator:test"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var applied orchdto.TaskRecoveryDTO
	require.NoError(t, json.Unmarshal(body, &applied))
	require.True(t, applied.Applied)

	var reloaded model.Task
	require.NoError(t, db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusFailed, reloaded.Status)
	require.Equal(t, task.StateVersion+1, reloaded.StateVersion)
	var events int64
	require.NoError(t, db.Model(&model.TaskEvent{}).Where("task_id = ? AND event_type = ?", task.ID, "operator_recovery_applied").Count(&events).Error)
	require.Equal(t, int64(1), events)
	var transition model.TaskEvent
	require.NoError(t, db.Where("task_id = ? AND event_type = ? AND new_value = ?",
		task.ID, "status_change", model.StatusFailed).First(&transition).Error)
	evidence, _ := transition.Details["evidence"].(map[string]any)
	require.Equal(t, "operator_recovery", evidence["source"])
}

func TestRecoverUnsupportedBreakGlassCaseReturnsRefusalDTO(t *testing.T) {
	fake, project, _, baseURL := setupGateHTTPTest(t)
	db := fake.db
	task := testutil.CreateTask(t, db, project.ID, "parent", model.StatusTestWriting)

	url := baseURL + "/projects/" + projectName + "/tasks/" + task.ID.String() + "/recover/stuck-parent-phase"
	resp, body := doJSON(t, http.MethodPost, url, `{"dry_run":true}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var result orchdto.TaskRecoveryDTO
	require.NoError(t, json.Unmarshal(body, &result))
	require.False(t, result.Safe)
	require.Equal(t, "safety_policy", result.RefusalReason)
	require.Contains(t, result.Message, "diagnostic-only")
}

func TestDremCanvasCascadeReplaySurfacesEvidenceThroughAPIs(t *testing.T) {
	fake, project, _, baseURL := setupGateHTTPTest(t)
	db := fake.db
	now := time.Now().UTC().Add(-10 * time.Minute)

	accepted := testutil.CreateTask(t, db, project.ID, "canvas exit-zero accepted", model.StatusDone)
	agentID := uuid.New()
	require.NoError(t, db.Create(&model.Agent{
		ID:             agentID,
		ProjectID:      project.ID,
		AgentType:      model.AgentCoder,
		Name:           "coder-canvas",
		Status:         model.AgentIdle,
		CurrentTaskID:  &accepted.ID,
		TmuxSession:    "canvas-ok",
		WorktreeBranch: "feature/canvas-ok",
		CompletedAt:    ptrTime(now.Add(2 * time.Minute)),
	}).Error)
	attemptID := uuid.New()
	completedAt := now.Add(2 * time.Minute)
	require.NoError(t, db.Create(&model.WorkerAttempt{
		ID:          attemptID,
		TaskID:      accepted.ID,
		AgentID:     &agentID,
		WorkerID:    "worker-canvas-ok",
		ContainerID: "canvas-ok",
		AgentType:   string(model.AgentCoder),
		Branch:      "feature/canvas-ok",
		State:       model.WorkerAttemptCompleted,
		CompletedAt: &completedAt,
		CreatedAt:   now,
	}).Error)
	for _, event := range []model.TaskEvent{
		{
			TaskID:    accepted.ID,
			EventType: "container_died",
			Actor:     "docker-events",
			NewValue:  "exit_zero",
			Details: model.JSONField{
				"attempt_id": attemptID.String(), "container_id": "canvas-ok", "exit_code": float64(0), "normalized_reason": "exit_zero",
			},
			CreatedAt: now.Add(time.Minute),
		},
		{
			TaskID:    accepted.ID,
			EventType: "branch_acceptance_accepted",
			Actor:     "orchestrator",
			Details:   model.JSONField{"agent_id": agentID.String(), "reason": "accepted_worker_completion", "accepted": true},
			CreatedAt: now.Add(90 * time.Second),
		},
		{
			TaskID:    accepted.ID,
			EventType: "worker_completion_evidence",
			Actor:     "docker-events",
			Details: model.JSONField{"evidence": map[string]any{
				"task_id": accepted.ID.String(), "attempt_id": attemptID.String(), "source": "docker_exit_zero", "reason": "accepted", "normalized_reason": "exit_zero_current_attempt",
			}},
			CreatedAt: now.Add(2 * time.Minute),
		},
	} {
		require.NoError(t, db.Create(&event).Error)
	}

	live := testutil.CreateTask(t, db, project.ID, "canvas live stale-ref refusal", model.StatusInProgress)
	heartbeat := now.Add(9 * time.Minute)
	liveAgent := model.Agent{ID: uuid.New(), ProjectID: project.ID, AgentType: model.AgentCoder, Name: "live-coder", Status: model.AgentWorking, CurrentTaskID: &live.ID, HeartbeatAt: &heartbeat, TmuxSession: "canvas-live"}
	require.NoError(t, db.Create(&liveAgent).Error)
	live.AssignedAgentID = &liveAgent.ID
	require.NoError(t, db.Save(&live).Error)
	require.NoError(t, db.Create(&model.WorkerAttempt{ID: uuid.New(), TaskID: live.ID, AgentID: &liveAgent.ID, WorkerID: "worker-live", ContainerID: "canvas-live", AgentType: string(model.AgentCoder), Branch: "feature/canvas-live", State: model.WorkerAttemptRunning}).Error)

	duplicate := testutil.CreateTask(t, db, project.ID, "canvas duplicate respawn", model.StatusInProgress)
	require.NoError(t, db.Exec("DROP INDEX IF EXISTS idx_worker_attempt_active_task_role_branch").Error)
	for _, workerID := range []string{"worker-dup-a", "worker-dup-b"} {
		require.NoError(t, db.Create(&model.WorkerAttempt{ID: uuid.New(), TaskID: duplicate.ID, WorkerID: workerID, AgentType: string(model.AgentCoder), Branch: "feature/canvas-dup", State: model.WorkerAttemptRunning}).Error)
	}

	parent := testutil.CreateTask(t, db, project.ID, "canvas parent blocked", model.StatusTestWriting)
	parent.Context = model.JSONField{"parent_readiness_blockers": "dependency-blocked: subtask 11111111-1111-1111-1111-111111111111 (phase \"test\") depends on 22222222-2222-2222-2222-222222222222 (phase \"implementation\", status in_progress)"}
	require.NoError(t, db.Save(&parent).Error)

	contaminated := testutil.CreateTask(t, db, project.ID, "canvas contaminated branch", model.StatusTestingReady)
	contaminated.Context = model.JSONField{"branch_hygiene_failure": "worker trace file committed"}
	require.NoError(t, db.Save(&contaminated).Error)
	require.NoError(t, db.Create(&model.TaskEvent{TaskID: contaminated.ID, EventType: "branch_acceptance_rejected", Actor: "orchestrator", Details: model.JSONField{"reason": "branch_contamination", "path": ".drem/trace.json"}, CreatedAt: now.Add(3 * time.Minute)}).Error)

	failed := testutil.CreateTask(t, db, project.ID, "canvas implementation failed", model.StatusFailed)
	failed.Context = model.JSONField{"latest_failure_type": "tool_exit_nonzero", "latest_failure_summary": "implementation attempt exited 1"}
	require.NoError(t, db.Save(&failed).Error)

	resp, body := doJSON(t, http.MethodPost, baseURL+"/projects/"+projectName+"/tasks/"+failed.ID.String()+"/comments", `{"author":"kyle","body":"Replay note: duplicate respawn refused; branch contamination must use supported recovery."}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var comment orchdto.TaskCommentDTO
	require.NoError(t, json.Unmarshal(body, &comment))
	require.Equal(t, failed.ID.String(), comment.TaskID)

	resp, body = doJSON(t, http.MethodGet, baseURL+"/projects/"+projectName+"/tasks", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var tasks []orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(body, &tasks))
	require.Equal(t, string(model.StatusDone), findTaskDTO(t, tasks, accepted.ID).Status)
	liveDTO := findTaskDTO(t, tasks, live.ID)
	require.Equal(t, string(model.StatusInProgress), liveDTO.Status)
	require.Equal(t, 1, liveDTO.ActiveAttemptCount)
	require.Equal(t, "feature/canvas-live", liveDTO.ActiveAttempts[0].Branch)
	require.Equal(t, string(model.StatusFailed), findTaskDTO(t, tasks, failed.ID).Status)

	resp, body = doJSON(t, http.MethodGet, baseURL+"/tasks/"+accepted.ID.String()+"/attempts", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var attempts []orchdto.WorkerAttemptDTO
	require.NoError(t, json.Unmarshal(body, &attempts))
	require.Len(t, attempts, 1)
	require.Equal(t, attemptID.String(), attempts[0].AttemptID)
	require.Equal(t, "feature/canvas-ok", attempts[0].Branch)
	require.NotNil(t, attempts[0].CompletedAt)

	resp, body = doJSON(t, http.MethodGet, baseURL+"/projects/"+projectName+"/health/issues", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var issues []orchdto.HealthIssueDTO
	require.NoError(t, json.Unmarshal(body, &issues))
	require.Equal(t, "feature/canvas-dup", findIssueType(t, issues, "duplicate_active_attempts").Branch)
	require.Len(t, findIssueType(t, issues, "parent_readiness_blocked").BlockedDependencies, 1)
	require.Equal(t, "branch_hygiene", findIssueType(t, issues, "branch_hygiene_gate_failure").GateFailure.Gate)

	resp, body = doJSON(t, http.MethodPost, baseURL+"/projects/"+projectName+"/tasks/"+live.ID.String()+"/recover/stale-assignment", `{"dry_run":true}`)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	require.Contains(t, decodeErr(t, body), "active worker")

	resp, body = doJSON(t, http.MethodPost, baseURL+"/projects/"+projectName+"/tasks/"+contaminated.ID.String()+"/recover/contaminated-branch-fail-gate", `{"dry_run":true}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var recovery orchdto.TaskRecoveryDTO
	require.NoError(t, json.Unmarshal(body, &recovery))
	require.True(t, recovery.Safe)
	require.False(t, recovery.Applied)
	require.Contains(t, recovery.Evidence, "worker trace file committed")

	resp, body = doJSON(t, http.MethodGet, baseURL+"/events?limit=20", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var events []orchdto.EventDTO
	require.NoError(t, json.Unmarshal(body, &events))
	requireEventType(t, events, "worker_completion_evidence")
	requireEventType(t, events, "branch_acceptance_accepted")
	requireEventType(t, events, "branch_acceptance_rejected")

	var storedComment model.TaskComment
	require.NoError(t, db.First(&storedComment, "id = ?", comment.ID).Error)
	require.Contains(t, storedComment.Body, "supported recovery")
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

func requireIssueType(t *testing.T, issues []orchdto.HealthIssueDTO, issueType string) {
	t.Helper()
	for _, issue := range issues {
		if issue.Type == issueType {
			return
		}
	}
	t.Fatalf("issue type %q not found in %#v", issueType, issues)
}

func findTaskDTO(t *testing.T, tasks []orchdto.TaskDTO, id uuid.UUID) orchdto.TaskDTO {
	t.Helper()
	for _, task := range tasks {
		if task.ID == id.String() {
			return task
		}
	}
	t.Fatalf("task %s not found in %#v", id, tasks)
	return orchdto.TaskDTO{}
}

func requireEventType(t *testing.T, events []orchdto.EventDTO, eventType string) {
	t.Helper()
	for _, event := range events {
		if event.Type == eventType {
			return
		}
	}
	t.Fatalf("event type %q not found in %#v", eventType, events)
}

func findIssueType(t *testing.T, issues []orchdto.HealthIssueDTO, issueType string) orchdto.HealthIssueDTO {
	t.Helper()
	for _, issue := range issues {
		if issue.Type == issueType {
			return issue
		}
	}
	t.Fatalf("issue type %q not found in %#v", issueType, issues)
	return orchdto.HealthIssueDTO{}
}

func requireNoIssueType(t *testing.T, issues []orchdto.HealthIssueDTO, issueType string) {
	t.Helper()
	for _, issue := range issues {
		require.NotEqual(t, issueType, issue.Type, "unexpected issue: %#v", issue)
	}
}
