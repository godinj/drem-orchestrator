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
	require.Contains(t, decodeErr(t, body), "refusing to clear live working assignment")
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

	resp, body = doJSON(t, http.MethodPost, url, `{"apply":true,"actor":"operator"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var applied orchdto.StaleAssignmentRecoveryDTO
	require.NoError(t, json.Unmarshal(body, &applied))
	require.True(t, applied.Applied)

	var updated model.Task
	require.NoError(t, db.First(&updated, "id = ?", task.ID).Error)
	require.Nil(t, updated.AssignedAgentID)
	var updatedAgent model.Agent
	require.NoError(t, db.First(&updatedAgent, "id = ?", agent.ID).Error)
	require.Nil(t, updatedAgent.CurrentTaskID)
	var events int64
	require.NoError(t, db.Model(&model.TaskEvent{}).Where("task_id = ? AND event_type = ?", task.ID, "stale_assignment_recovered").Count(&events).Error)
	require.Equal(t, int64(1), events)
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
