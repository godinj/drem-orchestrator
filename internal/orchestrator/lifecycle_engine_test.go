package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

type recordingLifecycleEngine struct {
	ticks []TickScope
	err   error
}

func (e *recordingLifecycleEngine) Tick(_ context.Context, scope TickScope) (LifecycleOutcome, error) {
	e.ticks = append(e.ticks, scope)
	return LifecycleOutcome{}, e.err
}

func (e *recordingLifecycleEngine) Apply(context.Context, LifecycleCommand) (LifecycleOutcome, error) {
	return LifecycleOutcome{}, nil
}

func (e *recordingLifecycleEngine) Ingest(context.Context, LifecycleExternalEvent) (LifecycleOutcome, error) {
	return LifecycleOutcome{}, nil
}

func TestNewInstallsLifecycleEngine(t *testing.T) {
	o := New(nil, "", nil, nil, nil, nil, uuid.New(), "test-project", nil, time.Second, time.Minute, 0, 0, nil, "")

	if o.lifecycle == nil {
		t.Fatal("expected New to install lifecycle engine")
	}
}

func TestDoTickDelegatesToLifecycleEngine(t *testing.T) {
	projectID := uuid.New()
	engine := &recordingLifecycleEngine{}
	o := &Orchestrator{
		projectID: projectID,
		lifecycle: engine,
		logger:    testLogger(),
	}

	o.doTick(context.Background())

	if len(engine.ticks) != 1 {
		t.Fatalf("expected one lifecycle tick, got %d", len(engine.ticks))
	}
	if engine.ticks[0].ProjectID != projectID {
		t.Fatalf("expected project id %s, got %s", projectID, engine.ticks[0].ProjectID)
	}
	if engine.ticks[0].Now.IsZero() {
		t.Fatal("expected tick scope to include current time")
	}
}
