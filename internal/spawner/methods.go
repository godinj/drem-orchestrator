package spawner

import (
	"context"
	"fmt"
	"time"

	"github.com/godinj/drem-orchestrator/internal/container"
)

// defaultNetwork is the Docker network every worker joins. Per
// docs/prd-containerization.md §"Networking and security" this is a single
// external network created by deploy/compose/network-setup.sh.
const defaultNetwork = "drem-net"

// bareRepoMountPath is where the host bare repository is mounted inside
// each worker. Workers clone from /bare into container-local storage;
// nothing ever writes back to the mount.
const bareRepoMountPath = "/bare"

// SpawnWorker builds a Spec, creates the container, and records it in the
// in-memory registry. The three identifying labels (project, agent_type,
// worker_id) and the branch label are set here and are NOT overridable by
// the caller's Labels map — caller labels merge over a fresh base but we
// re-apply ours last so the registry invariants hold.
func (s *Service) SpawnWorker(ctx context.Context, p SpawnWorkerParams) (SpawnWorkerResult, error) {
	if p.Project == "" || p.AgentType == "" || p.WorkerID == "" {
		return SpawnWorkerResult{}, fmt.Errorf("project, agent_type, and worker_id are required")
	}

	image := p.Image
	if image == "" {
		resolved, ok := resolveImage(p.AgentType, p.Labels)
		if !ok {
			return SpawnWorkerResult{}, fmt.Errorf("no image mapping for agent_type=%q", p.AgentType)
		}
		image = resolved
	}

	labels := mergeLabels(p.Labels, map[string]string{
		"drem.project":    p.Project,
		"drem.agent_type": p.AgentType,
		"drem.worker_id":  p.WorkerID,
		"drem.branch":     p.Branch,
	})

	var mounts []container.Mount
	if p.BareRepoMount != "" {
		mounts = append(mounts, container.Mount{
			Source:   p.BareRepoMount,
			Target:   bareRepoMountPath,
			ReadOnly: !p.BareRepoReadWrite,
		})
	}

	spec := container.Spec{
		Image:   image,
		Cmd:     p.Cmd,
		Env:     p.Env,
		Labels:  labels,
		Mounts:  mounts,
		Network: defaultNetwork,
	}

	h, err := s.Runtime.Spawn(ctx, spec)
	if err != nil {
		return SpawnWorkerResult{}, fmt.Errorf("spawn: %w", err)
	}

	info := WorkerInfo{
		ContainerID: h.ID,
		Project:     p.Project,
		AgentType:   p.AgentType,
		WorkerID:    p.WorkerID,
		Branch:      p.Branch,
		Status:      string(container.StatusRunning),
		StartedAt:   time.Now().UTC(),
	}
	s.mu.Lock()
	s.registry[h.ID] = &registryEntry{info: info}
	s.mu.Unlock()

	return SpawnWorkerResult{ContainerID: h.ID, Endpoint: h.Endpoint}, nil
}

// DestroyWorker delegates to the runtime and drops the container from the
// registry. Registry removal happens unconditionally on a successful
// runtime Destroy; failure leaves the entry in place so ListWorkers still
// surfaces the container until the operator intervenes.
func (s *Service) DestroyWorker(ctx context.Context, p DestroyWorkerParams) error {
	if p.ContainerID == "" {
		return fmt.Errorf("container_id is required")
	}
	if err := s.Runtime.Destroy(ctx, p.ContainerID); err != nil {
		return fmt.Errorf("destroy: %w", err)
	}
	s.mu.Lock()
	delete(s.registry, p.ContainerID)
	s.mu.Unlock()
	return nil
}

// ListWorkers returns every registry entry whose project label matches the
// filter. An empty Project returns every entry. Status is refreshed from
// the runtime per-entry so callers see the current lifecycle state rather
// than the snapshot captured at spawn time; a failed Inspect falls back to
// the cached status so a transiently-removed container is still reported.
func (s *Service) ListWorkers(ctx context.Context, p ListWorkersParams) (ListWorkersResult, error) {
	s.mu.Lock()
	entries := make([]WorkerInfo, 0, len(s.registry))
	for _, e := range s.registry {
		if p.Project != "" && e.info.Project != p.Project {
			continue
		}
		entries = append(entries, e.info)
	}
	s.mu.Unlock()

	for i := range entries {
		st, err := s.Runtime.Inspect(ctx, entries[i].ContainerID)
		if err != nil {
			continue
		}
		entries[i].Status = string(st.Status)
		if !st.StartedAt.IsZero() {
			entries[i].StartedAt = st.StartedAt
		}
	}
	return ListWorkersResult{Workers: entries}, nil
}

// InspectWorker returns the runtime's view of a single container. The
// in-memory registry is not consulted — the runtime is authoritative for
// every field in the result.
func (s *Service) InspectWorker(ctx context.Context, p InspectWorkerParams) (InspectWorkerResult, error) {
	if p.ContainerID == "" {
		return InspectWorkerResult{}, fmt.Errorf("container_id is required")
	}
	st, err := s.Runtime.Inspect(ctx, p.ContainerID)
	if err != nil {
		return InspectWorkerResult{}, fmt.Errorf("inspect: %w", err)
	}
	return InspectWorkerResult{
		Status:     string(st.Status),
		ExitCode:   st.ExitCode,
		StartedAt:  st.StartedAt,
		FinishedAt: st.FinishedAt,
		OOMKilled:  st.OOMKilled,
	}, nil
}

// mergeLabels combines caller-supplied labels with the service's own
// identifying labels. Service labels win on conflict so the registry
// invariants (drem.project, drem.agent_type, drem.worker_id, drem.branch)
// cannot be overridden by untrusted input.
func mergeLabels(caller, service map[string]string) map[string]string {
	out := make(map[string]string, len(caller)+len(service))
	for k, v := range caller {
		out[k] = v
	}
	for k, v := range service {
		out[k] = v
	}
	return out
}
