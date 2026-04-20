// spawn.go routes agent spawning through the spawner service instead of
// running a direct subprocess on the host. It is the first cut of the
// migration described in
// docs/containerization/prompts/13-agent-spawn-routing.md and is
// intentionally additive: the legacy subprocess path in runner.go keeps
// working while callers opt in to Manager.Spawn on a per-agent-type
// basis.
//
// Scope limitations this file respects:
//   - No orchestrator changes.
//   - No tmux removal — tmux imports in other files stay until prompt 17.
//   - No new lifecycle states — the existing AgentStatus enum is reused.
//   - Prompt delivery writes to a host path and is bind-mounted into the
//     container; see the README for details.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// WorkerSpawnParams is the wire-shape this package cares about when
// asking the spawner service to start a worker. It mirrors the fields
// of spawner.SpawnWorkerParams so the concrete spawner.Client can be
// adapted by the caller without dragging the spawner package into every
// file in this one. A tiny adapter (three method calls) converts
// between the two shapes; see internal/agent/README.md for an example.
// CredsMount mirrors spawner.SpawnWorkerParams.CredsMount: when
// non-empty, it is a host path the spawner bind-mounts read-only at
// /home/drem/.claude/.credentials.json so a claude-harness worker can
// read the operator's subscription credentials.
type WorkerSpawnParams struct {
	Project       string
	AgentType     string
	WorkerID      string
	Branch        string
	Labels        map[string]string
	Image         string
	Env           map[string]string
	BareRepoMount string
	CredsMount    string
}

// WorkerSpawnResult mirrors spawner.SpawnWorkerResult. Keeping the mirror
// here lets the Spawner interface (and Manager, which depends on it)
// avoid importing the spawner package directly.
type WorkerSpawnResult struct {
	ContainerID string
	Endpoint    string
}

// Spawner is the subset of the spawner RPC surface that Manager needs.
// It uses the package-local mirror types so tests and alternate
// transports can satisfy the interface without importing
// internal/spawner. Production callers wire a spawner.Client through a
// tiny adapter (see README).
type Spawner interface {
	SpawnWorker(ctx context.Context, p WorkerSpawnParams) (WorkerSpawnResult, error)
	DestroyWorker(ctx context.Context, containerID string) error
}

// SpawnRequest is the high-level request Manager.Spawn accepts. It is
// decoupled from WorkerSpawnParams so callers do not have to know about
// label conventions or the bare-repo mount path. Manager translates it
// into WorkerSpawnParams using its ImageResolver and the defaults
// baked into this package.
type SpawnRequest struct {
	// Project is the project slug used for labels and for the
	// per-project prompt directory. Required.
	Project string
	// AgentType is the agent type used for labels and image
	// resolution (e.g. "coder", "merger", "reviewer"). Required.
	AgentType string
	// AgentID uniquely identifies this worker within the project. It is
	// stored in the drem.worker_id label and used as the prompt file
	// name on the host. Required.
	AgentID string
	// TaskID is an optional task identifier copied into labels so
	// operators can correlate a container to the task the worker is
	// executing. Empty values are not emitted.
	TaskID string
	// Branch is the git ref the worker clones. Copied into the
	// drem.branch label and into WorkerSpawnParams.Branch.
	Branch string
	// Env adds per-spawn environment variables on top of the spawner's
	// defaults. Keys here are not validated — the spawner rejects
	// reserved ones.
	Env map[string]string
	// PromptPath is the path to the prompt file on the host. Manager
	// does not read the file; it records the path so callers can
	// provide their own copy semantics. When empty, no prompt mount is
	// requested.
	PromptPath string
	// BareRepoMount is the host path of the bare repository bind-mounted
	// into the worker at /bare. When empty, no bare repo mount is
	// requested.
	BareRepoMount string
	// CredsMount is the host path of the Claude subscription credentials
	// file bind-mounted read-only into the worker at
	// /home/drem/.claude/.credentials.json. When empty, no creds mount
	// is requested — appropriate for workers that do not run the claude
	// CLI (merger). When populated, the spawner pre-checks the path
	// exists and fails the spawn fast if it does not.
	CredsMount string
}

// Handle is returned by Manager.Spawn. It carries the container ID (so
// the caller can later Teardown it), the agent ID (so the caller can
// correlate back to the DB row), the resolved image (for observability),
// and the start timestamp (for staleness checks).
type Handle struct {
	ContainerID string
	AgentID     string
	StartedAt   time.Time
	Image       string
}

// Manager routes agent spawns through the spawner service. It owns the
// ImageResolver so image policy stays in one place, and it writes agent
// lifecycle rows to the same database GORM instance the rest of the
// orchestrator uses. Manager is safe for concurrent use because the
// Spawner interface is assumed to be concurrency-safe (spawner.Client is)
// and the DB handle is a *gorm.DB which GORM explicitly supports for
// concurrent callers.
type Manager struct {
	db       *gorm.DB
	spawner  Spawner
	resolver *ImageResolver

	// probe is the optional AttachProbe used by Attach and
	// RespawnIfDashboardExited to verify container liveness. When nil,
	// those operations treat the probe as unavailable and fall back to
	// the behaviour documented on each method.
	probe AttachProbe

	// promptRoot is the host directory under which per-project prompt
	// files live. Manager bind-mounts <promptRoot>/<project>/prompts/
	// into the container when a SpawnRequest.PromptPath is inside it.
	promptRoot string

	// log is the logger used by lifecycle-extension methods (Attach,
	// RespawnIfDashboardExited). Defaults to slog.Default when nil.
	log *slog.Logger

	// now returns the current time. Overridable for deterministic tests.
	now func() time.Time
}

// SetAttachProbe configures the AttachProbe used by Manager.Attach and
// RespawnIfDashboardExited. When a probe is not set, Attach returns the
// handle without liveness verification and RespawnIfDashboardExited is a
// no-op. The probe is separated from the Spawner interface so tests can
// supply one without implementing the full Spawner surface.
func (m *Manager) SetAttachProbe(p AttachProbe) {
	m.probe = p
}

// SetLogger configures the slog.Logger used for diagnostic messages from
// Attach / RespawnIfDashboardExited. When unset, slog.Default is used.
func (m *Manager) SetLogger(l *slog.Logger) {
	m.log = l
}

// logger returns the configured logger or slog.Default.
func (m *Manager) logger() *slog.Logger {
	if m.log != nil {
		return m.log
	}
	return slog.Default()
}

// NewManager constructs a Manager. When resolver is nil the Manager uses
// a zero-value ImageResolver, which resolves every language-agnostic
// agent type but returns an error for "coder" until a language is
// supplied. When promptRoot is empty, Manager derives it from $HOME as
// ~/.drem/projects, matching the host layout documented in the
// containerization PRD.
func NewManager(db *gorm.DB, sp Spawner, resolver *ImageResolver, promptRoot string) *Manager {
	if resolver == nil {
		resolver = &ImageResolver{}
	}
	if promptRoot == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			promptRoot = filepath.Join(home, ".drem", "projects")
		}
	}
	return &Manager{
		db:         db,
		spawner:    sp,
		resolver:   resolver,
		promptRoot: promptRoot,
		now:        time.Now,
	}
}

// PromptDir returns the per-project prompt directory for this Manager.
// It is exported so callers writing prompts on the host can put them in
// the same location Manager expects.
func (m *Manager) PromptDir(project string) string {
	return filepath.Join(m.promptRoot, project, "prompts")
}

// Spawn routes a SpawnRequest through the spawner. The sequence is:
//
//  1. Resolve the image via ImageResolver.
//  2. Build WorkerSpawnParams with identifying labels.
//  3. Call Spawner.SpawnWorker.
//  4. Record the container ID on the existing agent row if one exists.
//  5. Return a Handle carrying everything the caller needs to track or
//     tear down the worker.
//
// Spawn does not create a new agent row — that stays in the caller so
// the lifecycle stays in one place. When a row with the requested
// AgentID is present, Spawn writes the container_id and image fields
// into its Config map (an existing additive JSONField) so operators and
// the TUI can surface the container attribution without a schema
// migration.
func (m *Manager) Spawn(ctx context.Context, req SpawnRequest) (*Handle, error) {
	if req.Project == "" || req.AgentType == "" || req.AgentID == "" {
		return nil, fmt.Errorf("agent manager: spawn: project, agent_type, and agent_id are required")
	}
	if m.spawner == nil {
		return nil, fmt.Errorf("agent manager: spawn: spawner is not configured")
	}

	image, err := m.resolver.Resolve(req.AgentType)
	if err != nil {
		return nil, fmt.Errorf("agent manager: spawn: %w", err)
	}

	labels := map[string]string{
		"drem.language": m.resolver.Language,
	}
	if req.TaskID != "" {
		labels["drem.task_id"] = req.TaskID
	}

	params := WorkerSpawnParams{
		Project:       req.Project,
		AgentType:     req.AgentType,
		WorkerID:      req.AgentID,
		Branch:        req.Branch,
		Labels:        labels,
		Image:         image,
		Env:           req.Env,
		BareRepoMount: req.BareRepoMount,
		CredsMount:    req.CredsMount,
	}

	res, err := m.spawner.SpawnWorker(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("agent manager: spawn: %w", err)
	}

	h := &Handle{
		ContainerID: res.ContainerID,
		AgentID:     req.AgentID,
		StartedAt:   m.now(),
		Image:       image,
	}

	// Best-effort: record container attribution on the DB row if one
	// exists. A missing row is not an error — not every caller writes
	// the agent row through this package yet.
	m.recordSpawn(req.AgentID, h)

	return h, nil
}

// recordSpawn updates the existing agent row (if present) with the
// resolved container id and image. The container id and image are
// stashed in Agent.Config under well-known keys ("container_id",
// "container_image") so the addition is purely additive — no schema
// migration is required. Errors are swallowed so a missing row or a
// transient DB failure does not fail the spawn.
func (m *Manager) recordSpawn(agentID string, h *Handle) {
	if m.db == nil || agentID == "" {
		return
	}
	var ag model.Agent
	if err := m.db.Select("id", "config").Where("id = ?", agentID).First(&ag).Error; err != nil {
		return
	}
	cfg := ag.Config
	if cfg == nil {
		cfg = model.JSONField{}
	}
	cfg["container_id"] = h.ContainerID
	cfg["container_image"] = h.Image
	m.db.Model(&model.Agent{}).Where("id = ?", agentID).Update("config", cfg)
}
