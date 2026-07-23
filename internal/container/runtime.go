// Package container defines the container runtime abstraction used by the
// spawner service, agentmon, and orchestrator event subscription paths. The
// abstraction hides the concrete container engine (Docker) behind a small
// interface so callers can be unit-tested against an in-memory fake.
//
// This package is a pure library: it has no HTTP or RPC surface of its own
// and does not map agent types to image names. Those responsibilities live
// in the spawner service and the agent package respectively.
package container

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound is returned when the runtime can authoritatively determine
// that a requested container no longer exists. Callers must not translate
// transport errors or incomplete inventories into this sentinel.
var ErrNotFound = errors.New("container not found")

// Status is the lifecycle state of a container as reported by Inspect.
type Status string

// Status constants mirror the Docker container lifecycle states the
// orchestrator needs to distinguish. Paused, restarting, and removing are
// intentionally collapsed into existing values by the concrete runtime;
// the abstraction exposes only the states callers actually branch on.
const (
	StatusCreated Status = "created"
	StatusRunning Status = "running"
	StatusExited  Status = "exited"
	StatusDead    Status = "dead"
	StatusRemoved Status = "removed"
)

// EventType enumerates the lifecycle transitions emitted by SubscribeEvents.
type EventType string

// EventType constants. EventOOM is emitted in addition to (not instead of)
// EventDie when the kernel OOM-killed the container — callers observing both
// should treat the OOM event as the authoritative crash cause.
const (
	EventStart   EventType = "start"
	EventDie     EventType = "die"
	EventOOM     EventType = "oom"
	EventDestroy EventType = "destroy"
)

// Mount describes a bind mount from the host into the container. For the
// worker-clone pattern in the containerization PRD the only read-only mount
// a worker needs is the bare repository; all working copies live inside the
// container's own filesystem.
type Mount struct {
	Source   string // host path
	Target   string // container path
	ReadOnly bool
}

// Spec is the declarative description of a container to spawn. It is the
// only input to Runtime.Spawn; implementations translate it into whatever
// their engine requires. No field performs I/O on access.
type Spec struct {
	Image      string
	Cmd        []string
	Env        map[string]string
	Labels     map[string]string
	Mounts     []Mount
	Network    string
	Workdir    string
	AutoRemove bool
}

// Handle is returned by Spawn. ID is always populated. Endpoint is set only
// when the container exposes a single well-known port and the runtime can
// report a reachable address for it; callers that do not need Endpoint can
// ignore it. Keeping Endpoint optional lets the first cut of the abstraction
// stay minimal — richer port-mapping support can be added without breaking
// existing callers.
type Handle struct {
	ID       string
	Endpoint string
}

// State is the snapshot returned by Inspect. StartedAt and FinishedAt are
// zero for containers that have not yet reached the corresponding lifecycle
// transition. OOMKilled is authoritative when true.
type State struct {
	Status     Status
	ExitCode   int
	StartedAt  time.Time
	FinishedAt time.Time
	OOMKilled  bool
}

// LogOptions controls the bounded/tailing behavior of StreamLogs.
// A zero value returns the container's currently available logs and closes.
type LogOptions struct {
	Since     time.Time
	Follow    bool
	TailLines int
}

// EventFilter restricts SubscribeEvents to containers whose labels are a
// superset of the filter's labels. An empty filter matches every container.
type EventFilter struct {
	Labels map[string]string
}

// Event is a lifecycle message produced by SubscribeEvents. Labels carry the
// container's labels at event time so callers can attribute events without a
// separate Inspect call. ExitCode and OOMKilled are populated on EventDie
// and EventOOM; zero values otherwise.
type Event struct {
	Type        EventType
	ContainerID string
	Labels      map[string]string
	Timestamp   time.Time
	ExitCode    int
	OOMKilled   bool
	Usage       *WorkerUsage
	// UsageInspected distinguishes a terminal inspection with no direct
	// harness summary from an event that has not queried logs yet.
	UsageInspected bool
}

// WorkerUsage is the terminal token summary emitted by a direct worker.
type WorkerUsage struct {
	Iterations int    `json:"iterations"`
	TokensIn   int    `json:"tokens_in"`
	TokensOut  int    `json:"tokens_out"`
	ContextPct int    `json:"context_pct,omitempty"`
	StopReason string `json:"stop_reason"`
}

// Runtime is the single I/O surface of this package. Every operation is
// explicit and context-scoped so callers control timeouts and cancellation.
// Implementations must be safe for concurrent use from multiple goroutines.
type Runtime interface {
	// Spawn creates and starts a container described by spec. A successful
	// return means the container was created AND started; partial failures
	// (created but not started) are unwound by the implementation before
	// returning an error.
	Spawn(ctx context.Context, spec Spec) (Handle, error)

	// Inspect returns the current state of the container with the given id.
	// Callers poll Inspect when they need a consistent snapshot; for
	// push-style notifications use SubscribeEvents instead.
	Inspect(ctx context.Context, id string) (State, error)

	// StreamLogs returns a reader over the container's multiplexed stdout
	// and stderr stream. When opts.Follow is true, it follows as new output
	// arrives; otherwise it returns currently available logs and closes. Callers are
	// responsible for demultiplexing the Docker stream header if they need
	// split stdout and stderr; the agentmon consumer does this downstream.
	// The returned ReadCloser must be closed by the caller.
	StreamLogs(ctx context.Context, id string, opts LogOptions) (io.ReadCloser, error)

	// SubscribeEvents returns a channel that receives lifecycle events for
	// containers whose labels are a superset of filter.Labels. The channel
	// is closed when ctx is cancelled or when the underlying event source
	// terminates. Event delivery is best-effort: dropped events on the
	// underlying source surface as a channel close and an error in the
	// engine log, not as a silent reconnect.
	SubscribeEvents(ctx context.Context, filter EventFilter) (<-chan Event, error)

	// Destroy force-removes the container with the given id. Destroy on an
	// unknown id returns an error. Destroy is idempotent only in the sense
	// that a successful Destroy followed by a second Destroy on the same id
	// will fail with a not-found error — callers that need idempotency wrap
	// the error check themselves.
	Destroy(ctx context.Context, id string) error
}

// ImageEnsurer is an optional runtime capability used by the spawner before
// it creates a worker container. Keeping it separate from Runtime preserves
// compatibility with read-only/test runtimes while production Docker can
// inspect and pull a missing image exactly once at the spawn boundary.
type ImageEnsurer interface {
	EnsureImage(ctx context.Context, image string) error
}
