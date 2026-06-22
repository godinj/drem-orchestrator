package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
	"github.com/godinj/drem-orchestrator/internal/promptassets"
)

type renderedPromptInfo struct {
	Path          string
	Hash          string
	AssetVersions string
}

// workerCredsPathEnv is the environment variable the per-project compose
// template passes to orch carrying the host path of the operator's
// claude subscription credentials file. Set by `drem project register`;
// consumed by buildSpawnContext for every claude-backed worker.
const workerCredsPathEnv = "DREM_WORKER_CREDS_PATH"

// workerCodexAuthPathEnv is the host path of the operator's Codex auth file.
// It is set by generated compose for codex-backed workers and bind-mounted
// read-only at /home/drem/.codex/auth.json by the spawner.
const workerCodexAuthPathEnv = "DREM_WORKER_CODEX_AUTH_PATH"

// workerPromptRootEnv is the environment variable the per-project
// compose template passes to orch carrying the host directory under
// which per-task prompt files are written. The spawner later bind-mounts
// the individual prompt file from that dir into the worker. See
// plans/worker-prompt-delivery.md §§2, 4.
const workerPromptRootEnv = "DREM_PROMPT_ROOT_HOST"

// spawnPolicyReasonPromptMissing classifies worker_spawn_failed
// events emitted when orch cannot resolve a host prompt root OR the
// prompt render/write step fails. Lets audit queries filter by reason
// without parsing the free-form error string. Companion to
// spawnPolicyReasonAPIKey defined below.
const spawnPolicyReasonPromptMissing = "prompt_render_failed"

// credsMountRequired reports whether the given agent_type runs the
// claude CLI harness inside the container and therefore needs the
// subscription credentials file bind-mounted. Roles not in this set
// (notably merger, a Go binary) get an empty CredsMount and the
// spawner omits the mount entry.
//
// The default for an unknown agent type is fail-closed (false): a
// future type has to be added here deliberately alongside a test
// covering it, so auth coverage cannot silently lag behind a new role.
// See plans/worker-subscription-auth.md §5.
func credsMountRequired(agentType string) bool {
	switch agentType {
	case string(model.AgentCoder),
		string(model.AgentReviewer),
		string(model.AgentFixer),
		// Tester and supervisor are not model.AgentType constants yet;
		// they are carried as string literals elsewhere in orch and
		// remain literals here until the enum catches up.
		"tester",
		"supervisor":
		return true
	case "merger":
		return false
	}
	return false
}

// resolveWorkerCredsPath returns the host path of the subscription
// credentials file the spawner should bind-mount into a claude-backed
// worker. It reads DREM_WORKER_CREDS_PATH first (set by `drem project
// register` from the operator's host $HOME) and falls back to
// $HOME/.claude/.credentials.json only if the env var is unset — the
// fallback is there for ad-hoc local invocations; production always
// sets the env var explicitly. Returns the empty string when neither
// source yields a path; caller is responsible for fail-closing.
func resolveWorkerCredsPath() string {
	if p := os.Getenv(workerCredsPathEnv); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", ".credentials.json")
}

// resolveWorkerCodexAuthPath returns the host path of the Codex auth file for
// codex-backed workers.
func resolveWorkerCodexAuthPath() string {
	if p := os.Getenv(workerCodexAuthPathEnv); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".codex", "auth.json")
}

// promptRequired reports whether the agent_type runs the claude CLI
// in a mode that needs a prompt file on disk. Mirrors
// credsMountRequired — every claude-backed role gets a prompt; merger
// (a Go binary that takes argv flags) does not. The two tables are
// kept separate so a future role can need auth without a prompt (or
// vice versa) without dragging one policy along with the other. See
// plans/worker-prompt-delivery.md §5.
func promptRequired(agentType string) bool {
	switch agentType {
	case string(model.AgentCoder),
		string(model.AgentReviewer),
		string(model.AgentFixer),
		"tester",
		"supervisor":
		return true
	case "merger":
		return false
	}
	// Unknown agent types default to false: a new role must add an
	// explicit entry + test here before it can get a prompt, same
	// deny-by-default discipline as credsMountRequired.
	return false
}

// resolveWorkerPromptRoot returns the host directory under which orch
// writes per-task prompt files. Reads DREM_PROMPT_ROOT_HOST first
// (set by `drem project register` on host), falls back to
// $HOME/.drem/projects/<project>/prompts — the same layout
// Manager.PromptDir already uses for the legacy host path (see
// internal/agent/spawn.go:207). Returns empty when neither source
// resolves; caller fail-closes on empty.
func resolveWorkerPromptRoot(project string) string {
	if p := os.Getenv(workerPromptRootEnv); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".drem", "projects", project, "prompts")
}

// renderAndWritePrompt renders the agent prompt via internal/prompt
// and writes it to <promptRoot>/<task-id>.md atomically (tmp file +
// rename). Returns the absolute host path of the written file.
//
// The rename pattern is load-bearing: SpawnWorker stats the file on
// the same goroutine immediately after this returns; any window where
// the file is half-written would race into a fail-closed spawn error.
// rename(2) within a single filesystem is atomic, so the file either
// does not exist or has the complete rendered content — never partial.
//
// Errors are returned as-is; buildSpawnContext turns them into a
// worker_spawn_failed event with reason=prompt_render_failed.
func (o *Orchestrator) renderAndWritePrompt(
	task *model.Task,
	project *model.Project,
	agentType string,
	promptRoot string,
) (renderedPromptInfo, error) {
	if task == nil {
		return renderedPromptInfo{}, fmt.Errorf("nil task")
	}
	if promptRoot == "" {
		return renderedPromptInfo{}, fmt.Errorf("prompt root is empty")
	}

	// WorktreePath: for container workers the worktree lives inside
	// the worker at /home/drem/work, but the prompt renderer reads it
	// as a logical handle (the generator doesn't actually open the
	// directory for most branches). Pass the host-identical bare repo
	// path when available so prompt.Generate has something truthful to
	// display in the rendered markdown. The legacy host path had
	// worktreePath point at a per-feature checkout on host; the
	// container path doesn't create one, so we use the bare repo path
	// here as a stable handle.
	var worktreePath string
	if o.worktree != nil {
		worktreePath = o.worktree.BareRepo()
	}

	var assets map[string]string
	versions := map[string]string{}
	if project != nil {
		var err error
		assets, versions, err = promptassets.Load(context.Background(), o.db, project.ID)
		if err != nil {
			return renderedPromptInfo{}, fmt.Errorf("load prompt assets: %w", err)
		}
	}
	versionsJSON := "{}"
	if len(versions) > 0 {
		if b, err := json.Marshal(versions); err == nil {
			versionsJSON = string(b)
		}
	}

	diagnosis, suggestedFix, affectedFiles := extractFixerContext(task)
	rendered := prompt.Generate(prompt.Opts{
		Task:          task,
		Project:       project,
		AgentType:     model.AgentType(agentType),
		WorktreePath:  worktreePath,
		PromptAssets:  assets,
		Diagnosis:     diagnosis,
		AffectedFiles: affectedFiles,
		SuggestedFix:  suggestedFix,
	})
	if strings.TrimSpace(rendered) == "" {
		return renderedPromptInfo{}, fmt.Errorf("prompt.Generate produced empty output for agent_type=%q task_id=%s",
			agentType, task.ID)
	}
	hash := sha256.Sum256([]byte(rendered))

	// Ensure the destination exists before writing. The per-project
	// compose template creates the dir at `drem project register`
	// time, but a fresh worktree or CI checkout may have raced ahead.
	if err := os.MkdirAll(promptRoot, 0o755); err != nil {
		return renderedPromptInfo{}, fmt.Errorf("mkdir prompt root %s: %w", promptRoot, err)
	}

	finalPath := filepath.Join(promptRoot, task.ID.String()+".md")
	// Write to a sibling tmp file first so the rename is atomic and
	// the worker's pre-stat never sees a partial write.
	tmpPath := finalPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(rendered), 0o644); err != nil {
		return renderedPromptInfo{}, fmt.Errorf("write prompt tmp %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		// Best-effort cleanup of the tmp file; ignore errors because
		// the caller is about to fail the spawn anyway.
		_ = os.Remove(tmpPath)
		return renderedPromptInfo{}, fmt.Errorf("rename prompt %s -> %s: %w", tmpPath, finalPath, err)
	}
	return renderedPromptInfo{Path: finalPath, Hash: hex.EncodeToString(hash[:]), AssetVersions: versionsJSON}, nil
}
