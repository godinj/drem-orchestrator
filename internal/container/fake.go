package container

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/google/uuid"
)

// FakeRuntime is an in-memory Runtime implementation used by spawner,
// orchestrator, and agentmon unit tests. Every mutating call is recorded so
// tests can assert on the sequence of operations without inspecting internal
// state. It is safe for concurrent use.
type FakeRuntime struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	calls      []Call
	subs       []*fakeSub
}

// Call records a single invocation of the Runtime interface. Op is the name
// of the method ("Spawn", "Inspect", "Destroy", "StreamLogs", "SubscribeEvents").
// Spec is populated for Spawn; ID is populated for any per-container call.
type Call struct {
	Op   string
	Spec *Spec
	ID   string
}

// fakeContainer is the in-memory bookkeeping entry for a spawned container.
type fakeContainer struct {
	spec  Spec
	state State
	logs  bytes.Buffer
}

// fakeSub is a live event subscription. Events matching the filter are
// delivered over ch until ctx is cancelled.
type fakeSub struct {
	filter EventFilter
	ch     chan Event
	ctx    context.Context
}

// NewFakeRuntime constructs a FakeRuntime ready for use.
func NewFakeRuntime() *FakeRuntime {
	return &FakeRuntime{
		containers: make(map[string]*fakeContainer),
	}
}

// Calls returns a copy of the recorded call log. The copy guards against
// race warnings when the test asserts while other goroutines are still
// driving the runtime.
func (f *FakeRuntime) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.calls))
	copy(out, f.calls)
	return out
}

// Spawn allocates a synthetic container ID and records the spec. Inspect
// against a freshly spawned container reports StatusRunning until a test
// overrides the state via SetInspectResult.
func (f *FakeRuntime) Spawn(_ context.Context, spec Spec) (Handle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "fake-" + uuid.New().String()
	specCopy := spec
	f.containers[id] = &fakeContainer{
		spec:  specCopy,
		state: State{Status: StatusRunning},
	}
	f.calls = append(f.calls, Call{Op: "Spawn", Spec: &specCopy, ID: id})
	return Handle{ID: id}, nil
}

// Inspect returns the last state installed by SetInspectResult (default:
// StatusRunning for a spawned container) or an error if the id is unknown.
func (f *FakeRuntime) Inspect(_ context.Context, id string) (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Op: "Inspect", ID: id})
	c, ok := f.containers[id]
	if !ok {
		return State{}, fmt.Errorf("fake runtime: inspect unknown id %q", id)
	}
	return c.state, nil
}

// StreamLogs returns a reader over the per-container byte buffer. Tests push
// bytes into the buffer via WriteLog. The returned reader is a snapshot
// copy — subsequent WriteLog calls do not appear in already-issued readers.
func (f *FakeRuntime) StreamLogs(_ context.Context, id string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Op: "StreamLogs", ID: id})
	c, ok := f.containers[id]
	if !ok {
		return nil, fmt.Errorf("fake runtime: stream logs unknown id %q", id)
	}
	// Snapshot so the returned reader is stable under concurrent writes.
	buf := bytes.NewReader(append([]byte(nil), c.logs.Bytes()...))
	return io.NopCloser(buf), nil
}

// SubscribeEvents registers a subscription. Events emitted via EmitEvent
// whose labels are a superset of filter.Labels are delivered on the
// returned channel. The channel is closed when ctx is cancelled.
func (f *FakeRuntime) SubscribeEvents(ctx context.Context, filter EventFilter) (<-chan Event, error) {
	f.mu.Lock()
	f.calls = append(f.calls, Call{Op: "SubscribeEvents"})
	sub := &fakeSub{
		filter: EventFilter{Labels: copyLabelMap(filter.Labels)},
		ch:     make(chan Event, 16),
		ctx:    ctx,
	}
	f.subs = append(f.subs, sub)
	f.mu.Unlock()

	go func() {
		<-ctx.Done()
		f.removeSub(sub)
	}()
	return sub.ch, nil
}

// Destroy removes the container. Destroy on an unknown id returns an error,
// matching the production Docker runtime's behavior.
func (f *FakeRuntime) Destroy(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Op: "Destroy", ID: id})
	if _, ok := f.containers[id]; !ok {
		return fmt.Errorf("fake runtime: destroy unknown id %q", id)
	}
	delete(f.containers, id)
	return nil
}

// EmitEvent pushes a synthetic event to every subscription whose filter
// matches the event's labels. Non-matching subscriptions are skipped.
// Delivery is non-blocking for the emitter: if a subscriber's channel is
// full, the event is dropped for that subscriber. Tests should size their
// consumer goroutines accordingly.
func (f *FakeRuntime) EmitEvent(ev Event) {
	f.mu.Lock()
	subs := make([]*fakeSub, len(f.subs))
	copy(subs, f.subs)
	f.mu.Unlock()
	for _, s := range subs {
		if !matches(s.filter, ev.Labels) {
			continue
		}
		select {
		case s.ch <- ev:
		case <-s.ctx.Done():
		}
	}
}

// SetInspectResult installs the State that subsequent Inspect calls for the
// given id will return. It is a test helper: production code never calls it.
// Unknown ids are accepted so tests can pre-stage state before Spawn if they
// wish — but the stored value is associated with the id, so if no container
// exists at that id Inspect will still return "unknown id".
func (f *FakeRuntime) SetInspectResult(id string, st State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.containers[id]; ok {
		c.state = st
		return
	}
	// Preserve the state against future Spawn calls that happen to reuse
	// this id (rare; FakeRuntime generates UUIDs). This keeps the helper
	// usable in either order.
	f.containers[id] = &fakeContainer{state: st}
}

// WriteLog appends bytes to the container's log buffer. Tests call this to
// simulate container stdout/stderr.
func (f *FakeRuntime) WriteLog(id string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return
	}
	c.logs.Write(data)
}

// removeSub detaches a subscription and closes its channel. Safe to call
// multiple times.
func (f *FakeRuntime) removeSub(target *fakeSub) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.subs {
		if s == target {
			f.subs = append(f.subs[:i], f.subs[i+1:]...)
			close(s.ch)
			return
		}
	}
}

// matches reports whether evLabels is a superset of filter.Labels. An empty
// filter matches every event.
func matches(filter EventFilter, evLabels map[string]string) bool {
	for k, v := range filter.Labels {
		if evLabels[k] != v {
			return false
		}
	}
	return true
}

// copyLabelMap makes a defensive copy of a label map; nil in, nil out.
func copyLabelMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
