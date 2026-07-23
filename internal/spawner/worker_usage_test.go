package spawner

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/container"
)

type imageEnsuringRuntime struct {
	*container.FakeRuntime
	images []string
	err    error
}

func (r *imageEnsuringRuntime) EnsureImage(_ context.Context, image string) error {
	r.images = append(r.images, image)
	return r.err
}

func TestInspectWorkerCapturesDirectHarnessUsageAfterExit(t *testing.T) {
	rt := container.NewFakeRuntime()
	svc := NewService(rt)
	spawned, err := svc.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project: "canvas", AgentType: "coder", WorkerID: "worker-1", Image: "worker:test",
	})
	require.NoError(t, err)
	rt.WriteLog(spawned.ContainerID, []byte("noise\ndrem-direct-agent: iterations=7 tokens_in=30114 tokens_out=812 duration=2m3s stop_reason=token_budget\n"))
	rt.SetInspectResult(spawned.ContainerID, container.State{Status: container.StatusExited})

	got, err := svc.InspectWorker(context.Background(), InspectWorkerParams{ContainerID: spawned.ContainerID})
	require.NoError(t, err)
	require.Equal(t, &WorkerUsage{Iterations: 7, TokensIn: 30114, TokensOut: 812, StopReason: "token_budget"}, got.Usage)
	calls := rt.Calls()
	require.Equal(t, container.LogOptions{TailLines: workerUsageTailLines}, calls[len(calls)-1].LogOptions)
}

func TestSpawnWorkerEnsuresImageBeforeContainerCreate(t *testing.T) {
	rt := &imageEnsuringRuntime{FakeRuntime: container.NewFakeRuntime()}
	svc := NewService(rt)
	_, err := svc.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project: "canvas", AgentType: "coder", WorkerID: "worker-1", Image: "localhost:5000/worker:latest",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"localhost:5000/worker:latest"}, rt.images)
	require.Equal(t, "Spawn", rt.Calls()[0].Op)
}

func TestSpawnWorkerStopsWhenImageCannotBePrepared(t *testing.T) {
	rt := &imageEnsuringRuntime{FakeRuntime: container.NewFakeRuntime(), err: errors.New("registry offline")}
	svc := NewService(rt)
	_, err := svc.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project: "canvas", AgentType: "coder", WorkerID: "worker-1", Image: "localhost:5000/missing:latest",
	})
	require.ErrorContains(t, err, "worker image unavailable")
	require.Empty(t, rt.Calls(), "ContainerCreate must not run after image preflight fails")
}

func TestParseWorkerUsageUsesLatestSummary(t *testing.T) {
	got := parseWorkerUsage([]byte(
		"drem-direct-agent: iterations=1 tokens_in=10 tokens_out=2 duration=1s stop_reason=max_tokens\n" +
			"drem-direct-agent: iterations=3 tokens_in=42 tokens_out=8 duration=2s stop_reason=success\n"))
	require.Equal(t, &WorkerUsage{Iterations: 3, TokensIn: 42, TokensOut: 8, StopReason: "success"}, got)
}

func TestParseWorkerUsageHandlesDockerMultiplexFrames(t *testing.T) {
	payload := []byte("drem-direct-agent: iterations=2 tokens_in=99 tokens_out=11 duration=3s stop_reason=\n")
	framed := make([]byte, 8+len(payload))
	framed[0] = 2
	binary.BigEndian.PutUint32(framed[4:8], uint32(len(payload)))
	copy(framed[8:], payload)
	got := parseWorkerUsage(demuxDockerLogPayload(framed))
	require.Equal(t, &WorkerUsage{Iterations: 2, TokensIn: 99, TokensOut: 11}, got)
}
