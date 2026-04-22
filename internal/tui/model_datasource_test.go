package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/pkg/orchclient"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// fakeDataSource is an in-memory stub used by model-level tests that don't
// want to spin up an httptest.Server. It records the calls made against it
// so individual tests can assert wiring without scraping logs.
type fakeDataSource struct {
	tasks     []orchdto.TaskDTO
	workers   []orchdto.WorkerDTO
	events    []orchdto.EventDTO
	history   orchdto.WorkerHistoryDTO
	logReader io.ReadCloser

	// forceErr, if non-nil, is returned from every method.
	forceErr error

	taskCalls   int
	workerCalls int
}

func (f *fakeDataSource) ListTasks(_ context.Context, _ orchclient.TaskFilter) ([]orchdto.TaskDTO, error) {
	f.taskCalls++
	if f.forceErr != nil {
		return nil, f.forceErr
	}
	return f.tasks, nil
}

func (f *fakeDataSource) ListWorkers(_ context.Context) ([]orchdto.WorkerDTO, error) {
	f.workerCalls++
	if f.forceErr != nil {
		return nil, f.forceErr
	}
	return f.workers, nil
}

func (f *fakeDataSource) Events(_ context.Context, _ time.Time) ([]orchdto.EventDTO, error) {
	if f.forceErr != nil {
		return nil, f.forceErr
	}
	return f.events, nil
}

func (f *fakeDataSource) WorkerHistory(_ context.Context, _ string) (orchdto.WorkerHistoryDTO, error) {
	if f.forceErr != nil {
		return orchdto.WorkerHistoryDTO{}, f.forceErr
	}
	return f.history, nil
}

func (f *fakeDataSource) StreamLogs(_ context.Context, _ string) (io.ReadCloser, error) {
	if f.forceErr != nil {
		return nil, f.forceErr
	}
	if f.logReader == nil {
		return io.NopCloser(strings.NewReader("")), nil
	}
	return f.logReader, nil
}

// TestLoadTasks_FromFakeDataSource verifies that the tasks loader goes
// through the DataSource seam and surfaces rendered model.Task rows. This
// is the smoke test that the DTO adapter preserves id, title, and status.
func TestLoadTasks_FromFakeDataSource(t *testing.T) {
	id := uuid.New()
	ds := &fakeDataSource{
		tasks: []orchdto.TaskDTO{
			{ID: id.String(), Title: "Ship it", Status: "in_progress"},
		},
	}
	m := Model{dataSource: ds}

	cmd := m.loadTasks()
	require.NotNil(t, cmd)
	msg := cmd()
	loaded, ok := msg.(tasksLoadedMsg)
	require.True(t, ok, "expected tasksLoadedMsg, got %T", msg)
	require.Len(t, loaded.tasks, 1)
	require.Equal(t, "Ship it", loaded.tasks[0].Title)
	require.Equal(t, id, loaded.tasks[0].ID)
	require.Equal(t, 1, ds.taskCalls)
}

// TestLoadAgents_FromFakeDataSource mirrors TestLoadTasks_FromFakeDataSource
// for the workers endpoint.
func TestLoadAgents_FromFakeDataSource(t *testing.T) {
	id := uuid.New()
	ds := &fakeDataSource{
		workers: []orchdto.WorkerDTO{
			{ID: id.String(), ContainerID: "c-1", AgentType: "coder", Status: "working"},
		},
	}
	m := Model{dataSource: ds}

	cmd := m.loadAgents()
	require.NotNil(t, cmd)
	msg := cmd()
	loaded, ok := msg.(agentsLoadedMsg)
	require.True(t, ok, "expected agentsLoadedMsg, got %T", msg)
	require.Len(t, loaded.agents, 1)
	require.Equal(t, id, loaded.agents[0].ID)
	require.Equal(t, "coder", string(loaded.agents[0].AgentType))
	require.Equal(t, "working", string(loaded.agents[0].Status))
	require.Equal(t, 1, ds.workerCalls)
}

// TestLoadTasks_ErrorReturnsDataErrMsg asserts that a DataSource failure
// is surfaced as dataErrMsg so the Update handler can set the retry
// banner without clobbering the last good snapshot.
func TestLoadTasks_ErrorReturnsDataErrMsg(t *testing.T) {
	ds := &fakeDataSource{forceErr: errors.New("boom")}
	m := Model{dataSource: ds}

	msg := m.loadTasks()()
	errMsg, ok := msg.(dataErrMsg)
	require.True(t, ok, "expected dataErrMsg, got %T", msg)
	require.EqualError(t, errMsg.err, "boom")
}

// TestLoadAgents_ErrorReturnsDataErrMsg mirrors TestLoadTasks_ErrorReturnsDataErrMsg
// for the workers endpoint.
func TestLoadAgents_ErrorReturnsDataErrMsg(t *testing.T) {
	ds := &fakeDataSource{forceErr: errors.New("nope")}
	m := Model{dataSource: ds}

	msg := m.loadAgents()()
	errMsg, ok := msg.(dataErrMsg)
	require.True(t, ok, "expected dataErrMsg, got %T", msg)
	require.EqualError(t, errMsg.err, "nope")
}

// TestRefreshData_NoDataSource_ReturnsEmptySnapshot ensures the refresh
// Cmd is safe to call on a Model that was constructed without a data
// source (e.g. in existing unit tests that build a Model literal).
func TestRefreshData_NoDataSource_ReturnsEmptySnapshot(t *testing.T) {
	m := Model{}

	msg := m.refreshData()()
	refreshed, ok := msg.(dataRefreshedMsg)
	require.True(t, ok, "expected dataRefreshedMsg, got %T", msg)
	require.Empty(t, refreshed.tasks)
	require.Empty(t, refreshed.agents)
}

// TestDataErrMsg_SetsBannerAndKeepsSnapshot exercises the Update path:
// a dataErrMsg after a successful refresh must not clear the previous
// tasks/agents but must set dataErr + advance the backoff.
func TestDataErrMsg_SetsBannerAndKeepsSnapshot(t *testing.T) {
	id := uuid.New()
	ds := &fakeDataSource{
		tasks: []orchdto.TaskDTO{{ID: id.String(), Title: "keep-me", Status: "backlog"}},
	}
	m := Model{dataSource: ds, keys: defaultKeyMap(), focus: FocusBoard,
		board: NewBoardModel(), agents: NewAgentsModel(), detail: NewDetailModel(),
		create: NewCreateModel(), feedback: NewFeedbackModel(""),
		width: 120, height: 40}

	// Seed the model with a successful load.
	loaded := m.loadTasks()()
	result, _ := m.Update(loaded)
	m = result.(Model)
	require.Len(t, m.board.tasks, 1, "expected seeded task")

	// Now simulate a failure.
	result, cmd := m.Update(dataErrMsg{err: errors.New("down")})
	m = result.(Model)
	require.EqualError(t, m.dataErr, "down")
	require.Equal(t, 1*time.Second, m.dataBackoff, "first failure arms 1s backoff")
	require.Len(t, m.board.tasks, 1, "snapshot must persist across failures")
	// Post-retry-storm-fix (plans/tui-retry-storm-prevention.md): the
	// dataErrMsg branch deliberately does NOT schedule a replacement
	// tick — that was Site 2 of the retry storm (tick compounding).
	// The singleton periodic tick from Init drives the retry; the
	// nextAllowedRefresh hold window below gates dispatches during
	// backoff without spawning concurrent loops.
	require.Nil(t, cmd, "dataErrMsg must NOT schedule a replacement tick")
	require.False(t, m.nextAllowedRefresh.IsZero(),
		"dataErrMsg must seed nextAllowedRefresh so the next tick is gated")
}
