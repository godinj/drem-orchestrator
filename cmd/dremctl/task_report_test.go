package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

func TestRenderTaskReportIncludesPhaseCoverageAndDeliveryEvidence(t *testing.T) {
	report := orchdto.TaskReportDTO{
		Task:           orchdto.TaskDTO{ID: "12345678-1234-1234-1234-123456789abc", Title: "Canvas canary", Status: "integration_ready"},
		WallDurationMS: 65000,
		Phases:         []orchdto.TaskReportPhaseDTO{{Name: "worker", DurationMS: 30000, Visits: 1, InferenceRuns: 2, TokensIn: 1200, TokensOut: 80}},
		Children:       []orchdto.TaskReportChildDTO{{ID: "child"}},
		Totals: orchdto.TaskReportTotalsDTO{
			TokensIn: 1200, TokensOut: 80, WorkerAttempts: 2, CompletedAttempts: 1,
			FailedAttempts: 1, ArtifactVersions: 2, VerificationRuns: 1,
			ComputerUseRuns: 2, HostReworkSessions: 1, HostReworkSubmissions: 1,
			CodexGoalCount: 1, CodexTokensUsed: 3456, CodexElapsedMS: 90000,
		},
		CodexGoals:          []orchdto.CodexGoalUsageDTO{{ThreadID: "thread-1", GoalObjective: "supervise canary", GoalStatus: "complete", TokensUsed: 3456, ElapsedMS: 90000}},
		MeasurementCoverage: orchdto.TaskReportCoverageDTO{EligibleInferenceRuns: 2, MeasuredInferenceRuns: 2, Percent: 100, ExternalCodexMeasured: true},
		Warnings:            []string{"external Codex usage unavailable"},
	}

	var out bytes.Buffer
	require.NoError(t, renderTaskReport(&out, false, report))
	text := out.String()
	for _, expected := range []string{
		"# Drem task report: Canvas canary", "Measurement coverage: 100.00% (2/2 inference runs)",
		"SGLang tokens: 1200 input / 80 output", "Codex goal usage: 3456 tokens / 1m30s across 1 goal(s)",
		"| worker | 30s | 1 | 2 | 1200 | 80 |", "Computer Use runs: 2",
		"Host rework: 1 sessions / 1 submissions", "supervise canary", "external Codex usage unavailable",
	} {
		require.Truef(t, strings.Contains(text, expected), "missing %q in:\n%s", expected, text)
	}
}

func TestRenderTaskReportJSONIsMachineReadable(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, renderTaskReport(&out, true, orchdto.TaskReportDTO{
		Project: "canvas-local", Task: orchdto.TaskDTO{ID: "task-id"},
	}))
	require.JSONEq(t, `{"project":"canvas-local","task":{"id":"task-id","title":"","status":"","state_version":0,"created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z","assigned_worker":""},"generated_at":"0001-01-01T00:00:00Z","wall_duration_ms":0,"children":null,"phases":null,"attempts":null,"codex_goals":null,"totals":{"tokens_in":0,"tokens_out":0,"worker_attempts":0,"completed_attempts":0,"failed_attempts":0,"aborted_attempts":0,"artifact_versions":0,"verification_runs":0,"computer_use_runs":0,"host_rework_sessions":0,"host_rework_submissions":0,"codex_goal_count":0,"codex_tokens_used":0,"codex_elapsed_ms":0},"measurement_coverage":{"eligible_inference_runs":0,"measured_inference_runs":0,"unmeasured_inference_runs":0,"percent":0,"external_codex_measured":false}}`, out.String())
}
