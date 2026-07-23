package container

import (
	"context"
	"fmt"
	"io"
	"time"

	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	dockermount "github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
)

// DockerRuntime is the production Runtime backed by the Docker Engine API
// over the default Unix socket. It is safe for concurrent use: the Docker
// client is itself goroutine-safe and DockerRuntime holds no mutable state.
type DockerRuntime struct {
	cli *client.Client
}

// EnsureImage verifies that image is available to the Docker daemon and
// pulls it when absent. The pull stream is drained so Docker completes and
// verifies the transfer before the caller proceeds to ContainerCreate.
func (r *DockerRuntime) EnsureImage(ctx context.Context, image string) error {
	if image == "" {
		return fmt.Errorf("ensure image: image is empty")
	}
	if _, _, err := r.cli.ImageInspectWithRaw(ctx, image); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspect image %s: %w", image, err)
	}

	rc, err := r.cli.ImagePull(ctx, image, dockerimage.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %s: %w", image, err)
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("pull image %s: consume response: %w", image, err)
	}
	if _, _, err := r.cli.ImageInspectWithRaw(ctx, image); err != nil {
		return fmt.Errorf("inspect pulled image %s: %w", image, err)
	}
	return nil
}

// NewDockerRuntime constructs a Runtime using the Docker client's standard
// environment-based configuration (DOCKER_HOST, DOCKER_TLS_VERIFY, etc.) and
// negotiates the API version with the daemon at connect time.
func NewDockerRuntime() (*DockerRuntime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker runtime: new client: %w", err)
	}
	return &DockerRuntime{cli: cli}, nil
}

// Close releases the underlying Docker client connection. Callers that hold
// a DockerRuntime for the process lifetime can ignore this; it is provided
// for tests and for short-lived tools.
func (r *DockerRuntime) Close() error {
	if r.cli == nil {
		return nil
	}
	return r.cli.Close()
}

// Spawn creates the container then starts it. On start failure the newly
// created container is removed so callers never see orphaned created-state
// containers from failed Spawn calls.
func (r *DockerRuntime) Spawn(ctx context.Context, spec Spec) (Handle, error) {
	cfg := &dockercontainer.Config{
		Image:      spec.Image,
		Cmd:        spec.Cmd,
		Env:        envMapToSlice(spec.Env),
		Labels:     spec.Labels,
		WorkingDir: spec.Workdir,
	}
	hostCfg := &dockercontainer.HostConfig{
		AutoRemove:  spec.AutoRemove,
		NetworkMode: dockercontainer.NetworkMode(spec.Network),
		Mounts:      toDockerMounts(spec.Mounts),
	}
	created, err := r.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return Handle{}, fmt.Errorf("container create: %w", err)
	}
	if err := r.cli.ContainerStart(ctx, created.ID, dockercontainer.StartOptions{}); err != nil {
		// Unwind the created container so a failed Spawn leaves no trace.
		_ = r.cli.ContainerRemove(context.Background(), created.ID, dockercontainer.RemoveOptions{Force: true})
		return Handle{}, fmt.Errorf("container start: %w", err)
	}
	return Handle{ID: created.ID}, nil
}

// Inspect maps the Docker InspectResponse onto our State. RFC3339Nano is the
// format Docker writes for StartedAt and FinishedAt; a parse error on either
// field is non-fatal (the field stays zero) because a zero timestamp is the
// documented signal that the transition has not yet occurred.
func (r *DockerRuntime) Inspect(ctx context.Context, id string) (State, error) {
	resp, err := r.cli.ContainerInspect(ctx, id)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return State{}, fmt.Errorf("container inspect %s: %w", id, ErrNotFound)
		}
		return State{}, fmt.Errorf("container inspect: %w", err)
	}
	if resp.State == nil {
		return State{}, fmt.Errorf("container inspect: nil state for %s", id)
	}
	return State{
		Status:     mapDockerStatus(resp.State.Status),
		ExitCode:   resp.State.ExitCode,
		StartedAt:  parseDockerTime(resp.State.StartedAt),
		FinishedAt: parseDockerTime(resp.State.FinishedAt),
		OOMKilled:  resp.State.OOMKilled,
	}, nil
}

// StreamLogs returns a multiplexed stdout+stderr stream for the container.
// Demultiplexing (splitting stdout from stderr using Docker's 8-byte stream
// header) is the caller's responsibility — agentmon handles this downstream.
func (r *DockerRuntime) StreamLogs(ctx context.Context, id string, opts LogOptions) (io.ReadCloser, error) {
	dockerOpts := dockercontainer.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     opts.Follow,
	}
	if opts.TailLines > 0 {
		dockerOpts.Tail = fmt.Sprintf("%d", opts.TailLines)
	}
	if !opts.Since.IsZero() {
		dockerOpts.Since = opts.Since.UTC().Format(time.RFC3339Nano)
	}
	rc, err := r.cli.ContainerLogs(ctx, id, dockerOpts)
	if err != nil {
		return nil, fmt.Errorf("container logs: %w", err)
	}
	return rc, nil
}

// Destroy force-removes the container, ignoring any running state. Callers
// that need a graceful stop should call ContainerStop themselves before
// Destroy; the Runtime interface is intentionally a narrow removal path.
func (r *DockerRuntime) Destroy(ctx context.Context, id string) error {
	if err := r.cli.ContainerRemove(ctx, id, dockercontainer.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("container remove: %w", err)
	}
	return nil
}

// SubscribeEvents opens a Docker events stream filtered to container-typed
// events matching the given labels. Events are mapped onto our Event type
// and delivered on the returned channel. When ctx is cancelled, the channel
// is closed and the underlying stream is torn down. If Docker's error
// channel fires, the events channel is closed — reconnection policy is the
// caller's concern, per the package contract.
func (r *DockerRuntime) SubscribeEvents(ctx context.Context, filter EventFilter) (<-chan Event, error) {
	args := filters.NewArgs()
	args.Add("type", string(events.ContainerEventType))
	for k, v := range filter.Labels {
		args.Add("label", k+"="+v)
	}
	msgs, errs := r.cli.Events(ctx, events.ListOptions{Filters: args})
	out := make(chan Event, 16)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-errs:
				// Surface errors at the source and exit; reconnection is
				// the caller's policy per the runtime contract.
				if !ok {
					return
				}
				if err != nil {
					return
				}
			case msg, ok := <-msgs:
				if !ok {
					return
				}
				ev, emit := mapDockerEvent(msg)
				if !emit {
					continue
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// envMapToSlice converts our map-of-strings env representation to Docker's
// slice-of-KEY=VALUE form. The iteration order is non-deterministic, which
// is fine for environment variables but means tests assert on set membership
// rather than slice equality.
func envMapToSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// toDockerMounts converts our Mount type to Docker's bind-mount descriptor.
// Only bind mounts are supported in the first cut; named volumes are a
// follow-up if an agent role needs persistent state across container
// lifetimes.
func toDockerMounts(mounts []Mount) []dockermount.Mount {
	if len(mounts) == 0 {
		return nil
	}
	out := make([]dockermount.Mount, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, dockermount.Mount{
			Type:     dockermount.TypeBind,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}
	return out
}

// mapDockerStatus collapses Docker's expanded state set onto our smaller
// set. Transient states (paused, restarting, removing) map to their closest
// steady-state neighbor — callers that need the finer distinction should
// inspect the container directly via the Docker client instead. The input
// is Docker's raw string (one of "created", "running", "paused",
// "restarting", "removing", "exited", "dead").
func mapDockerStatus(s string) Status {
	switch s {
	case "created":
		return StatusCreated
	case "running", "paused", "restarting":
		return StatusRunning
	case "exited", "removing":
		return StatusExited
	case "dead":
		return StatusDead
	default:
		return Status(s)
	}
}

// parseDockerTime parses Docker's RFC3339Nano timestamp, returning the zero
// time on parse error or on Docker's zero-value sentinel "0001-01-01...".
func parseDockerTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	if t.Year() <= 1 {
		return time.Time{}
	}
	return t
}

// mapDockerEvent translates a Docker events.Message into our Event value.
// The second return reports whether the message should be emitted: actions
// the runtime interface does not model (pause, rename, exec_*) are dropped.
func mapDockerEvent(msg events.Message) (Event, bool) {
	var t EventType
	switch msg.Action {
	case events.ActionStart:
		t = EventStart
	case events.ActionDie:
		t = EventDie
	case events.ActionOOM:
		t = EventOOM
	case events.ActionDestroy:
		t = EventDestroy
	default:
		return Event{}, false
	}
	ev := Event{
		Type:        t,
		ContainerID: msg.Actor.ID,
		Labels:      copyAttrs(msg.Actor.Attributes),
		Timestamp:   eventTime(msg),
	}
	if exitStr, ok := msg.Actor.Attributes["exitCode"]; ok {
		fmt.Sscanf(exitStr, "%d", &ev.ExitCode)
	}
	if t == EventOOM {
		ev.OOMKilled = true
	}
	return ev, true
}

// eventTime prefers the nano-precision field when present; Docker's older
// event stream fills only the seconds-precision Time.
func eventTime(msg events.Message) time.Time {
	if msg.TimeNano > 0 {
		return time.Unix(0, msg.TimeNano)
	}
	if msg.Time > 0 {
		return time.Unix(msg.Time, 0)
	}
	return time.Time{}
}

// copyAttrs makes a defensive copy of the label map so downstream
// consumers cannot observe mutations to the Docker client's internal map.
func copyAttrs(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
