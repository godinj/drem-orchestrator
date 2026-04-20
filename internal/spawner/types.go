// Package spawner implements the JSON-RPC 2.0 service that owns the Docker
// socket on behalf of the rest of the Drem Orchestrator stack. Per
// docs/prd-containerization.md §"Spawner service owns the Docker socket",
// every other process (orchestrator, agentmon) talks to this service over a
// Unix socket rather than touching Docker directly.
//
// This file defines the on-wire parameter and result shapes. They are the
// sole inputs and outputs of the RPC surface; keeping them in their own file
// makes it easy to audit the contract without reading service plumbing.
package spawner

import "time"

// SpawnWorkerParams is the payload of the SpawnWorker RPC. Project,
// AgentType, and WorkerID are mandatory — they populate the three
// identifying labels used for every subsequent list and inspect operation.
// Branch is the Git ref the worker will clone; Labels adds caller-supplied
// labels on top of the three identifying ones (most notably
// "drem.language", which steers coder image selection). Image is an
// explicit override; when empty the spawner resolves the image from the
// agent-type → image table in images.go. Env passes through to the
// container. BareRepoMount, when non-empty, is bind-mounted at /bare so
// the worker can clone from it. By default the mount is read-only; set
// BareRepoReadWrite to true for per-task mergers that must push. Cmd is
// the container's argv — empty leaves the image's ENTRYPOINT/CMD
// unchanged; populated it replaces CMD exactly, letting orchestrator
// callers pass per-invocation flags (for example the merger's
// --feature-branch / --task-id / --orch-url pairs). CredsMount, when
// non-empty, is a host path bind-mounted read-only at
// /home/drem/.claude/.credentials.json so the worker's claude CLI can
// read the host operator's subscription credentials; the spawner
// pre-checks the host path exists before creating the container and
// fails SpawnWorker fast if it does not. Leave empty for agent types
// that do not run claude (notably merger, which is a Go binary).
// PromptMount, when non-empty, is a host file path bind-mounted
// read-only into the worker at /home/drem/.drem/prompt.md; the
// spawner also sets DREM_PROMPT_PATH in the worker env to that
// container-side path deterministically so the entrypoint's claude
// invocation finds the prompt without per-caller env plumbing. The
// spawner pre-checks the host path exists before creating the
// container and fails SpawnWorker fast if it does not. Leave empty
// for agent types that do not need a prompt (notably merger). See
// plans/worker-prompt-delivery.md §3.
type SpawnWorkerParams struct {
	Project           string            `json:"project"`
	AgentType         string            `json:"agent_type"`
	WorkerID          string            `json:"worker_id"`
	Branch            string            `json:"branch"`
	Labels            map[string]string `json:"labels,omitempty"`
	Image             string            `json:"image,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	BareRepoMount     string            `json:"bare_repo_mount,omitempty"`
	BareRepoReadWrite bool              `json:"bare_repo_read_write,omitempty"`
	Cmd               []string          `json:"cmd,omitempty"`
	CredsMount        string            `json:"creds_mount,omitempty"`
	PromptMount       string            `json:"prompt_mount,omitempty"`
}

// SpawnWorkerResult is the return value of SpawnWorker. ContainerID is
// always populated on success; Endpoint is copied from the runtime Handle
// and is empty unless the runtime exposed a reachable port.
type SpawnWorkerResult struct {
	ContainerID string `json:"container_id"`
	Endpoint    string `json:"endpoint,omitempty"`
}

// DestroyWorkerParams identifies a container to remove. The spawner also
// drops the container from its in-memory registry on success.
type DestroyWorkerParams struct {
	ContainerID string `json:"container_id"`
}

// EmptyResult is the shared zero-payload return value for RPCs that have
// no meaningful response body. Kept as a named type so the JSON encoder
// always produces "{}" rather than "null".
type EmptyResult struct{}

// ListWorkersParams filters the returned registry. An empty Project string
// returns every tracked container; a non-empty string returns only
// containers whose "drem.project" label equals Project.
type ListWorkersParams struct {
	Project string `json:"project,omitempty"`
}

// WorkerInfo is the registry entry returned by ListWorkers. Status is
// resolved by a fresh Inspect against the runtime at list time; stale
// values are never returned from the in-memory registry alone.
type WorkerInfo struct {
	ContainerID string    `json:"container_id"`
	Project     string    `json:"project"`
	AgentType   string    `json:"agent_type"`
	WorkerID    string    `json:"worker_id"`
	Branch      string    `json:"branch"`
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at"`
}

// ListWorkersResult wraps the slice so the on-wire response shape is a
// JSON object ({"workers": [...]}), matching the rest of the RPC surface
// where every result is an object.
type ListWorkersResult struct {
	Workers []WorkerInfo `json:"workers"`
}

// InspectWorkerParams identifies a container to query. The caller passes
// the ContainerID returned from SpawnWorker.
type InspectWorkerParams struct {
	ContainerID string `json:"container_id"`
}

// InspectWorkerResult mirrors container.State with JSON-friendly field
// names. OOMKilled is authoritative when true per the runtime contract.
type InspectWorkerResult struct {
	Status     string    `json:"status"`
	ExitCode   int       `json:"exit_code"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	OOMKilled  bool      `json:"oom_killed"`
}
