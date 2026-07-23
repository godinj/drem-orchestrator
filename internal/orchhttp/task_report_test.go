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

func TestTaskReportCorrelatesLifecycleInferenceAndHostEvidence(t *testing.T) {
	srv, ts, project := setupHTTPTest(t, nil)
	now := time.Now().UTC().Truncate(time.Second)
	parent := testutil.CreateTask(t, srv.DB, project.ID, "measured canary", model.StatusIntegrationReady)
	require.NoError(t, srv.DB.Model(&parent).Updates(map[string]any{
		"created_at": now.Add(-time.Hour), "updated_at": now,
	}).Error)
	parent.CreatedAt, parent.UpdatedAt = now.Add(-time.Hour), now
	child := testutil.CreateTask(t, srv.DB, project.ID, "implement canary", model.StatusDone)
	require.NoError(t, srv.DB.Model(&child).Updates(map[string]any{
		"parent_task_id": parent.ID, "phase": "implementation",
		"created_at": now.Add(-50 * time.Minute), "updated_at": now.Add(-20 * time.Minute),
	}).Error)

	completedAt := now.Add(-25 * time.Minute)
	for _, attempt := range []model.WorkerAttempt{
		{ID: uuid.New(), TaskID: child.ID, AgentType: "coder", Branch: "feature/measured", State: model.WorkerAttemptCompleted, TokensIn: 100, TokensOut: 20, CompletedAt: &completedAt, CreatedAt: now.Add(-45 * time.Minute)},
		{ID: uuid.New(), TaskID: child.ID, AgentType: "coder", Branch: "feature/unmeasured", State: model.WorkerAttemptFailed, CompletedAt: &completedAt, CreatedAt: now.Add(-43 * time.Minute)},
		{ID: uuid.New(), TaskID: child.ID, AgentType: "coder", Branch: "feature/preflight", State: model.WorkerAttemptAborted, FailureClassification: "worker_image_unavailable", CompletedAt: &completedAt, CreatedAt: now.Add(-42 * time.Minute)},
	} {
		require.NoError(t, srv.DB.Create(&attempt).Error)
	}

	events := []model.TaskEvent{
		{TaskID: parent.ID, EventType: "task_created", NewValue: "plan_review", Actor: "codex:test", CreatedAt: now.Add(-time.Hour)},
		{TaskID: parent.ID, EventType: "inference_usage", Actor: "orchestrator", Details: model.JSONField{"phase": "plan_review", "tokens_in": 10, "tokens_out": 2}, CreatedAt: now.Add(-55 * time.Minute)},
		{TaskID: parent.ID, EventType: "status_change", OldValue: "plan_review", NewValue: "in_progress", Actor: "policy", CreatedAt: now.Add(-50 * time.Minute)},
		{TaskID: parent.ID, EventType: "status_change", OldValue: "in_progress", NewValue: "testing_ready", Actor: "orchestrator", CreatedAt: now.Add(-30 * time.Minute)},
		{TaskID: parent.ID, EventType: "status_change", OldValue: "verification_ready", NewValue: "host_rework", Actor: "codex:test", CreatedAt: now.Add(-20 * time.Minute)},
		{TaskID: parent.ID, EventType: "status_change", OldValue: "host_rework", NewValue: "testing_ready", Actor: "codex:test", CreatedAt: now.Add(-15 * time.Minute)},
		{TaskID: parent.ID, EventType: "status_change", OldValue: "verification_ready", NewValue: "integration_ready", Actor: "codex:test", CreatedAt: now.Add(-5 * time.Minute)},
	}
	for i := range events {
		require.NoError(t, srv.DB.Create(&events[i]).Error)
	}

	artifact := model.DeliveryArtifact{
		ID: uuid.New(), TaskID: parent.ID, ArtifactVersion: 1, Branch: "feature/canary",
		CommitSHA: "1111111111111111111111111111111111111111", BaseBranch: "master",
		BaseSHA: "0000000000000000000000000000000000000000", PreliminaryEvidence: model.JSONField{},
		CreatorActor: "orchestrator", CreatorSource: "test", CreatedAt: now.Add(-30 * time.Minute),
	}
	require.NoError(t, srv.DB.Create(&artifact).Error)
	verification := model.VerificationRecord{
		ID: uuid.New(), TaskID: parent.ID, DeliveryArtifactID: artifact.ID, ArtifactVersion: 1,
		CommitSHA: artifact.CommitSHA, VerifierActor: "codex:test", EnvironmentFingerprint: "mac-arm64",
		CommandEvidence: model.JSONField{}, Result: model.VerificationPassed,
		IdempotencyKey: uuid.NewString(), RequestHash: "hash", CreatedAt: now.Add(-10 * time.Minute),
	}
	require.NoError(t, srv.DB.Create(&verification).Error)
	require.NoError(t, srv.DB.Create(&model.VerificationInteraction{
		ID: uuid.New(), TaskID: parent.ID, VerificationRecordID: verification.ID, DeliveryArtifactID: artifact.ID,
		AcceptanceCriterionKey: "title", ScenarioName: "launch", InteractionStepsJSON: "[]",
		ObservedResult: "pass", EvidenceRefsJSON: "[]", BinarySHA256: "abc", ApplicationVersion: "test",
		HostEnvironment: "mac", RunPID: 42, Result: model.VerificationPassed, CreatedAt: now.Add(-10 * time.Minute),
	}).Error)
	sessionID := uuid.New()
	finished := now.Add(-15 * time.Minute)
	require.NoError(t, srv.DB.Create(&model.HostReworkSession{
		ID: sessionID, TaskID: parent.ID, DeliveryArtifactID: artifact.ID, PriorArtifactVersion: 1,
		PriorCommitSHA: artifact.CommitSHA, Branch: artifact.Branch, OwnerActor: "codex:test", Reason: "native discrepancy",
		AllowedScope: model.JSONArray{"src/Main.cpp"}, Attestation: model.JSONField{},
		StartIdempotencyKey: uuid.NewString(), StartRequestHash: "start", Disposition: model.HostReworkSubmitted,
		ReplacementCommitSHA: "2222222222222222222222222222222222222222", StartedAt: now.Add(-20 * time.Minute), FinishedAt: &finished,
	}).Error)
	require.NoError(t, srv.DB.Create(&model.HostReworkSubmission{
		ID: uuid.New(), SessionID: sessionID, TaskID: parent.ID, PriorCommitSHA: artifact.CommitSHA,
		ReplacementCommitSHA: "2222222222222222222222222222222222222222", Actor: "codex:test",
		IdempotencyKey: uuid.NewString(), RequestHash: "submit", ChangedPaths: model.JSONArray{"src/Main.cpp"}, CreatedAt: finished,
	}).Error)
	require.NoError(t, srv.DB.Create(&model.CodexGoalUsage{
		ID: uuid.New(), TaskID: parent.ID, Actor: "codex:test", ThreadID: "test",
		GoalObjective: "supervise measured canary", GoalStatus: "complete",
		TokensUsed: 9000, ElapsedMS: 120000, Source: "codex_get_goal",
		IdempotencyKey: uuid.NewString(), RequestHash: "goal", UsageCapturedAt: now,
	}).Error)

	resp, err := http.Get(ts.URL + "/projects/" + projectName + "/tasks/" + parent.ID.String() + "/report")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var report orchdto.TaskReportDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&report))
	require.Equal(t, parent.ID.String(), report.Task.ID)
	require.Len(t, report.Children, 1)
	require.Len(t, report.Attempts, 3)
	require.Equal(t, 110, report.Totals.TokensIn)
	require.Equal(t, 22, report.Totals.TokensOut)
	require.Equal(t, 1, report.Totals.CompletedAttempts)
	require.Equal(t, 1, report.Totals.FailedAttempts)
	require.Equal(t, 1, report.Totals.AbortedAttempts)
	require.Equal(t, 1, report.Totals.ArtifactVersions)
	require.Equal(t, 1, report.Totals.VerificationRuns)
	require.Equal(t, 1, report.Totals.ComputerUseRuns)
	require.Equal(t, 1, report.Totals.HostReworkSessions)
	require.Equal(t, 1, report.Totals.CodexGoalCount)
	require.Equal(t, int64(9000), report.Totals.CodexTokensUsed)
	require.Equal(t, int64(120000), report.Totals.CodexElapsedMS)
	require.Len(t, report.CodexGoals, 1)
	require.True(t, report.MeasurementCoverage.ExternalCodexMeasured)
	require.Equal(t, 3, report.MeasurementCoverage.EligibleInferenceRuns)
	require.Equal(t, 2, report.MeasurementCoverage.MeasuredInferenceRuns)
	require.Equal(t, 1, report.MeasurementCoverage.UnmeasuredInferenceRuns)
	require.InDelta(t, 66.67, report.MeasurementCoverage.Percent, 0.01)
	require.Len(t, report.Warnings, 1)
}
