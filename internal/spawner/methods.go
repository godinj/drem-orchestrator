package spawner

import (
	"context"
	"errors"
	"fmt"
	"os"
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

// credsMountPath is where the host operator's Claude subscription
// credentials file is bind-mounted inside a claude-harness worker.
// Chosen to match the default $HOME/.claude/.credentials.json path so
// the claude CLI resolves it without CLAUDE_CONFIG_DIR gymnastics.
const credsMountPath = "/home/drem/.claude/.credentials.json"

// codexAuthMountPath is where the host operator's Codex auth file is
// bind-mounted inside a codex-harness worker.
const codexAuthMountPath = "/home/drem/.codex/auth.json"

// promptMountPath is where the orchestrator-rendered task prompt file
// is bind-mounted inside a claude-harness worker. The value is
// deterministic at the spawner boundary: the container-side path is
// owned by the spawner, not the caller, so callers cannot regress
// the contract by setting a conflicting DREM_PROMPT_PATH.
const promptMountPath = "/home/drem/.drem/prompt.md"

// workerPromptPathEnv is the env var the worker entrypoint
// (deploy/docker/context/worker-entrypoint.sh:132-160) consults to
// locate the prompt file. The spawner overwrites any caller-supplied
// value so the env always agrees with promptMountPath.
const workerPromptPathEnv = "DREM_PROMPT_PATH"

const (
	journalMountPath     = "/home/drem/.drem/state"
	workerJournalPathEnv = "DREM_DIRECT_JOURNAL_PATH"
)

// SpawnWorker builds a Spec, creates the container, and records it in the
// in-memory registry. The identifying labels (project, project_id, agent_type,
// worker_id) and the branch label are set here and are NOT overridable by
// the caller's Labels map — caller labels merge over a fresh base but we
// re-apply ours last so the registry invariants hold. drem.project is the
// human-readable name (matches agentmon's DREM_PROJECT env filter);
// drem.project_id is the stable UUID (used by every internal orch filter).
// See plans/dual-label-worker-spawn.md for the v13-v14 outage that drove
// the dual-label contract.
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
	if ensurer, ok := s.Runtime.(container.ImageEnsurer); ok {
		// Serialize image inspection/pulls at the Docker ownership boundary.
		// A missing shared worker image should cause one bounded pull rather
		// than several concurrent ContainerCreate failures and task retries.
		s.imageMu.Lock()
		err := ensurer.EnsureImage(ctx, image)
		s.imageMu.Unlock()
		if err != nil {
			return SpawnWorkerResult{}, fmt.Errorf("worker image unavailable: %w", err)
		}
	}

	labels := mergeLabels(p.Labels, map[string]string{
		"drem.project":    p.Project,
		"drem.project_id": p.ProjectID,
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
	if p.CredsMount != "" {
		// Fail-closed: subscription auth requires the credentials file
		// to exist on host. A missing file surfaces here as a clear
		// SpawnWorker error rather than an opaque claude CLI prompt
		// inside the container.
		if _, err := os.Stat(p.CredsMount); err != nil {
			return SpawnWorkerResult{}, fmt.Errorf(
				"creds file not found at %s: run `claude login` on host: %w",
				p.CredsMount, err)
		}
		// Read-only: workers must not overwrite the host operator's
		// creds. OAuth refresh happens on host via `claude login`.
		mounts = append(mounts, container.Mount{
			Source:   p.CredsMount,
			Target:   credsMountPath,
			ReadOnly: true,
		})
	}
	if p.CodexAuthMount != "" {
		if _, err := os.Stat(p.CodexAuthMount); err != nil {
			return SpawnWorkerResult{}, fmt.Errorf(
				"codex auth file not found at %s: run `codex login` on host: %w",
				p.CodexAuthMount, err)
		}
		mounts = append(mounts, container.Mount{
			Source:   p.CodexAuthMount,
			Target:   codexAuthMountPath,
			ReadOnly: true,
		})
	}

	// Defensive copy of the caller's env so we can inject
	// DREM_PROMPT_PATH deterministically without mutating the caller's
	// map. p.Env may be nil; the copy always produces a valid map when
	// we need one.
	env := p.Env
	if p.PromptMount != "" {
		// Fail-closed: the worker entrypoint reads the prompt once at
		// exec time. If orch didn't land the file on host yet, better
		// to refuse the spawn here than to fall into interactive-claude
		// mode inside the container (which silently exits).
		if _, err := os.Stat(p.PromptMount); err != nil {
			return SpawnWorkerResult{}, fmt.Errorf(
				"prompt file not found at %s: orch must write the prompt before SpawnWorker: %w",
				p.PromptMount, err)
		}
		// Read-only: workers must not overwrite their own prompt.
		// Scratch space lives under /home/drem/work (the clone).
		mounts = append(mounts, container.Mount{
			Source:   p.PromptMount,
			Target:   promptMountPath,
			ReadOnly: true,
		})
		// Copy + overwrite DREM_PROMPT_PATH deterministically. The
		// container-side path belongs to the spawner contract, not the
		// caller, so a caller that accidentally set this env key in
		// SpawnWorkerParams.Env cannot regress the mount target.
		copied := make(map[string]string, len(env)+1)
		for k, v := range env {
			copied[k] = v
		}
		copied[workerPromptPathEnv] = promptMountPath
		env = copied
	}
	if p.JournalMount != "" {
		if _, err := os.Stat(p.JournalMount); err != nil {
			return SpawnWorkerResult{}, fmt.Errorf("journal file not found at %s: orchestrator must pre-create it: %w", p.JournalMount, err)
		}
		mounts = append(mounts, container.Mount{Source: p.JournalMount, Target: journalMountPath, ReadOnly: false})
		copied := make(map[string]string, len(env)+1)
		for k, v := range env {
			copied[k] = v
		}
		copied[workerJournalPathEnv] = journalMountPath + "/journal.json"
		env = copied
	}

	spec := container.Spec{
		Image:   image,
		Cmd:     p.Cmd,
		Env:     env,
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
		ProjectID:   p.ProjectID,
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

// ListWorkers returns every registry entry whose project labels match the
// filter. Both filters are AND'd; empty fields are ignored so callers can
// pass either the name (Project) or the stable UUID (ProjectID). Internal
// orchestrator consumers pass ProjectID so the filter survives renames;
// operators calling over the RPC from the host may pass either.
// See plans/dual-label-worker-spawn.md.
// Status is refreshed from the runtime per-entry so callers see the current
// lifecycle state rather than the snapshot captured at spawn time; a failed
// Inspect falls back to the cached status so a transiently-removed
// container is still reported.
func (s *Service) ListWorkers(ctx context.Context, p ListWorkersParams) (ListWorkersResult, error) {
	s.mu.Lock()
	entries := make([]WorkerInfo, 0, len(s.registry))
	for _, e := range s.registry {
		if p.Project != "" && e.info.Project != p.Project {
			continue
		}
		if p.ProjectID != "" && e.info.ProjectID != p.ProjectID {
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
		if errors.Is(err, container.ErrNotFound) {
			return InspectWorkerResult{Status: string(container.StatusRemoved)}, nil
		}
		return InspectWorkerResult{}, fmt.Errorf("inspect: %w", err)
	}
	result := InspectWorkerResult{
		Status:     string(st.Status),
		ExitCode:   st.ExitCode,
		StartedAt:  st.StartedAt,
		FinishedAt: st.FinishedAt,
		OOMKilled:  st.OOMKilled,
	}
	if terminalWorkerStatus(st.Status) {
		result.Usage = s.readWorkerUsage(ctx, p.ContainerID)
	}
	return result, nil
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
