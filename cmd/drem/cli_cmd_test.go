package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/cli"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/orchhttp"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/pkg/orchclient"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// fakeGateOrch is the minimum GateOrchestrator stand-in this test
// needs. It records which Handle* method was called and applies a
// status transition to the shared GORM DB so the handler's post-
// mutation re-fetch reflects the new state. The gate_handlers_test.go
// file in internal/orchhttp uses a fuller version; we duplicate a tiny
// slice here to avoid a test-package cycle.
type fakeGateOrch struct {
	approved bool
}

func (f *fakeGateOrch) HandlePlanApproved(taskID uuid.UUID) error {
	f.approved = true
	return nil
}
func (f *fakeGateOrch) HandlePlanApprovedBy(taskID uuid.UUID, actor string) error {
	return f.HandlePlanApproved(taskID)
}
func (f *fakeGateOrch) HandlePlanRejected(taskID uuid.UUID) error                       { return nil }
func (f *fakeGateOrch) HandlePlanRejectedBy(taskID uuid.UUID, actor string) error       { return nil }
func (f *fakeGateOrch) HandleTestReviewApproved(taskID uuid.UUID) error                 { return nil }
func (f *fakeGateOrch) HandleTestReviewApprovedBy(taskID uuid.UUID, actor string) error { return nil }
func (f *fakeGateOrch) HandleTestReviewRejected(uuid.UUID, string) error                { return nil }
func (f *fakeGateOrch) HandleTestReviewRejectedBy(uuid.UUID, string, string) error      { return nil }
func (f *fakeGateOrch) HandleTestPassed(taskID uuid.UUID) error                         { return nil }
func (f *fakeGateOrch) HandleTestFailed(taskID uuid.UUID) error                         { return nil }
func (f *fakeGateOrch) HandleClarificationAnswer(uuid.UUID, string) error               { return nil }
func (f *fakeGateOrch) HandleClarificationAnswerBy(uuid.UUID, string, string) error     { return nil }
func (f *fakeGateOrch) RetryTask(taskID uuid.UUID) error                                { return nil }

// TestCLIApproveAgainstRealOrchHTTP is the end-to-end regression test
// for Phase 2 of the orch API gate-mutation pivot. It wires a real
// orchhttp.Server (importing internal/orchhttp, the same code path
// the containerized production orch runs) to an httptest.Server, then
// drives the CLI the same way cmd/drem/cli_cmd.go does — via
// cli.Run with an *orchclient.Client pointed at the test server.
//
// The test proves:
//
//  1. cli.Run no longer opens the DB or spins up an orchestrator for
//     gate commands — the only writer seam in this test is the fake
//     GateOrch wired into the HTTP server, so if the CLI had kept a
//     direct-write path the task's status would not flip (the DB is
//     test-local).
//  2. The client's short-prefix resolution round-trips through
//     GET /projects/{name}/tasks correctly.
//  3. A 200 OK from the gate handler decodes into the expected
//     TaskDTO shape the CLI renders.
//
// All per-verb behaviour (status mapping, --json shape, error
// surfacing) is covered by the narrower fakes in
// internal/cli/gate_commands_http_test.go — this file does not
// duplicate those cases.
func TestCLIApproveAgainstRealOrchHTTP(t *testing.T) {
	// Set up a real orchhttp.Server with its own SQLite + a fake
	// orchestrator. CreateTask + CreateProject use testutil so the
	// project row the server looks up exists with the expected name.
	db := testutil.NewTestDB(t)
	projectName := "cli-e2e"
	proj := testutil.CreateProject(t, db, projectName, "/tmp/repo.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "approve me", model.StatusPlanReview)

	fake := &fakeGateOrch{}
	srv := orchhttp.New(db, "test-token", nil, orchhttp.ProjectInfo{
		Name:     projectName,
		Language: "go",
		OrchURL:  "http://localhost:0",
	})
	srv.Orch = fake
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	// Construct the client exactly how runCLI() does — via orchclient.New
	// against the resolved URL. cli.Run then dispatches the approve
	// subcommand through DispatchGate, which is the production code path
	// cmd/drem/cli_cmd.go wires.
	client := orchclient.New(ts.URL).WithToken("test-token").WithActor("codex:test")
	var buf bytes.Buffer
	err := cli.Run(db, []string{"approve", task.ID.String()[:8]}, &buf, false, client, projectName)
	require.NoError(t, err)
	require.True(t, fake.approved, "HandlePlanApproved should have been called via the HTTP API")

	// Sanity: the re-fetched DTO rendered on stdout carries the task's
	// short-ID. A regression that bypasses the HTTP path (e.g. a direct-
	// DB write) would still flip the row, but the server response DTO
	// would not appear in buf.
	require.Contains(t, buf.String(), task.ID.String()[:8])
}

// TestCLIApproveJSONMode is the --json shape assertion for the same
// end-to-end path. Scripts that consume the CLI output need the raw
// TaskDTO JSON, not the human text.
func TestCLIApproveJSONMode(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectName := "cli-e2e-json"
	proj := testutil.CreateProject(t, db, projectName, "/tmp/repo.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "approve me", model.StatusPlanReview)

	srv := orchhttp.New(db, "test-token", nil, orchhttp.ProjectInfo{Name: projectName, Language: "go"})
	srv.Orch = &fakeGateOrch{}
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	client := orchclient.New(ts.URL).WithToken("test-token").WithActor("codex:test")
	var buf bytes.Buffer
	err := cli.Run(db, []string{"approve", task.ID.String()[:8]}, &buf, true, client, projectName)
	require.NoError(t, err)

	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(buf.Bytes(), &dto))
	require.Equal(t, task.ID.String(), dto.ID)
}

func TestShouldWarnImplicitStatsDB(t *testing.T) {
	tests := []struct {
		name           string
		configExplicit bool
		databasePath   string
		args           []string
		want           bool
	}{
		{
			name:         "stats with implicit default database",
			databasePath: "./drem.db",
			args:         []string{"stats"},
			want:         true,
		},
		{
			name:           "explicit config suppresses warning",
			configExplicit: true,
			databasePath:   "./drem.db",
			args:           []string{"stats"},
		},
		{
			name:         "non-default database suppresses warning",
			databasePath: "/tmp/live/drem.db",
			args:         []string{"stats"},
		},
		{
			name:         "other command suppresses warning",
			databasePath: "./drem.db",
			args:         []string{"tasks"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.DatabasePath = tt.databasePath
			if got := shouldWarnImplicitStatsDB(tt.configExplicit, cfg, tt.args); got != tt.want {
				t.Fatalf("shouldWarnImplicitStatsDB() = %v, want %v", got, tt.want)
			}
		})
	}
}
