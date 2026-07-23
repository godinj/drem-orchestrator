// runner_start.go contains the provider-specific agent startup routines that
// write per-agent config, spawn the subprocess, and launch the background
// monitors. Split from runner.go to keep the lifecycle orchestration layer
// legible.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/ctxmon"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// startClaudeAgent writes the prompt file and settings, starts a Claude Code
// subprocess, stores the RunningAgent, and launches monitoring goroutines.
func (r *Runner) startClaudeAgent(agentID, taskID uuid.UUID, worktreePath, branch, sessionName, prompt string, agentType model.AgentType) error {
	claudeDir := filepath.Join(worktreePath, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return fmt.Errorf("mkdir .claude: %w", err)
	}

	promptPath := filepath.Join(claudeDir, "agent-prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return fmt.Errorf("write prompt: %w", err)
	}

	written, err := os.ReadFile(promptPath)
	if err != nil || len(written) != len(prompt) {
		return fmt.Errorf("prompt write verification failed: wrote %d of %d bytes",
			len(written), len(prompt))
	}

	statusScriptPath := filepath.Join(claudeDir, "context-status.sh")
	statusOutputPath := ctxmon.UsageFilePath(worktreePath)
	scriptContent := ctxmon.StatusScript(statusOutputPath)
	if err := os.WriteFile(statusScriptPath, []byte(scriptContent), 0o755); err != nil {
		return fmt.Errorf("write status script: %w", err)
	}

	if err := writeAgentMetadata(claudeDir, agentID, taskID); err != nil {
		return fmt.Errorf("write agent metadata: %w", err)
	}

	homeDir, _ := os.UserHomeDir()
	projectDir := filepath.Join(homeDir, ".claude", "projects", ctxmon.ProjectDirName(worktreePath))
	exitLogScript := generateExitLogScript(agentID, taskID, projectDir)
	exitLogScriptPath := filepath.Join(claudeDir, "exit-log.sh")
	if err := os.WriteFile(exitLogScriptPath, []byte(exitLogScript), 0o755); err != nil {
		return fmt.Errorf("write exit-log script: %w", err)
	}

	// Remove any stale idle signal file left by a previous agent that used
	// the same worktree so the legacy runner never observes a false idle
	// notification for the new process.
	idleSignal := filepath.Join(claudeDir, "agent-idle")
	os.Remove(idleSignal)

	compactionSignal := ctxmon.CompactionSignalPath(worktreePath)

	hooks := map[string]any{
		"Notification": []any{
			map[string]any{
				"matcher": "idle_prompt",
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": fmt.Sprintf("touch %s", idleSignal),
						"timeout": idleNotifyTimeoutSecs,
					},
				},
			},
		},
	}
	for k, v := range ctxmon.HooksJSON(compactionSignal) {
		hooks[k] = v
	}

	settings := buildSettingsJSON(claudeDir, worktreePath, exitLogScriptPath, hooks)

	settingsBytes, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal claude settings: %w", err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settingsPath, settingsBytes, 0o644); err != nil {
		return fmt.Errorf("write claude settings: %w", err)
	}

	cliConfig := r.agentConfigs(agentType)
	proc, err := r.startProcess(context.Background(), r.claudeBin, promptPath, worktreePath, cliConfig.CLIArgs())
	if err != nil {
		return fmt.Errorf("start subprocess: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	ra := &RunningAgent{
		AgentID:      agentID,
		TaskID:       taskID,
		WorktreePath: worktreePath,
		Branch:       branch,
		TmuxSession:  sessionName,
		Process:      proc,
		LogPath:      proc.LogPath(),
		Provider:     model.ProviderClaude,
		StartedAt:    time.Now(),
		cancel:       cancel,
	}

	r.mu.Lock()
	r.running[agentID] = ra
	r.mu.Unlock()

	go r.monitorAgent(ctx, agentID, proc, worktreePath)
	go r.heartbeatLoop(ctx, agentID)
	go r.contextMonitorLoop(ctx, agentID, worktreePath)

	return nil
}

// startOpenCodeAgent sets up and starts an OpenCode agent subprocess. OpenCode
// agents skip Claude-specific settings (settings.json, hooks, status scripts,
// exit-log script) and instead write minimal metadata to a per-agent
// subdirectory under .opencode/agents/<agentID>/ to avoid file collisions
// when multiple agents share the same worktree.
func (r *Runner) startOpenCodeAgent(agentID, taskID uuid.UUID, worktreePath, branch, sessionName, prompt string, agentType model.AgentType) error {
	ocDir := filepath.Join(worktreePath, ".opencode", "agents", agentID.String())
	if err := os.MkdirAll(ocDir, 0o755); err != nil {
		return fmt.Errorf("start opencode agent: mkdir .opencode/agents: %w", err)
	}

	// Copy global opencode config into the shared .opencode/ root where
	// OpenCode discovers it (relative to --dir), not the per-agent subdir.
	// Ensure external_directory permission is always set to "allow" so
	// headless agents can access files outside their worktree root (e.g.
	// sibling worktrees, bare repo). Without this, OpenCode's headless mode
	// auto-rejects any "ask" permission, causing "external_directory
	// auto-rejected" failures.
	ocRoot := filepath.Join(worktreePath, ".opencode")
	homeDir, _ := os.UserHomeDir()
	globalCfg := filepath.Join(homeDir, ".config", "opencode", "opencode.json")
	if data, err := os.ReadFile(globalCfg); err == nil {
		var cfg map[string]any
		if json.Unmarshal(data, &cfg) == nil {
			perm, _ := cfg["permission"].(map[string]any)
			if perm == nil {
				perm = make(map[string]any)
			}
			perm["external_directory"] = "allow"
			cfg["permission"] = perm
			if patched, err := json.MarshalIndent(cfg, "", "  "); err == nil {
				data = patched
			}
		}
		_ = os.WriteFile(filepath.Join(ocRoot, "opencode.json"), data, 0o644)
	}

	promptPath := filepath.Join(ocDir, "agent-prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return fmt.Errorf("start opencode agent: write prompt: %w", err)
	}

	if err := writeAgentMetadata(ocDir, agentID, taskID); err != nil {
		return fmt.Errorf("start opencode agent: write agent metadata: %w", err)
	}

	os.Remove(filepath.Join(ocDir, "agent-idle"))

	cliConfig := r.agentConfigs(agentType)
	proc, err := StartOpenCodeProcess(context.Background(), r.openCodeBin, promptPath, worktreePath, cliConfig.CLIArgs(), ocDir)
	if err != nil {
		return fmt.Errorf("start opencode agent: start subprocess: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	ra := &RunningAgent{
		AgentID:      agentID,
		TaskID:       taskID,
		WorktreePath: worktreePath,
		Branch:       branch,
		TmuxSession:  sessionName,
		Process:      proc,
		LogPath:      proc.LogPath(),
		Provider:     model.ProviderOpenCode,
		StartedAt:    time.Now(),
		cancel:       cancel,
	}

	r.mu.Lock()
	r.running[agentID] = ra
	r.mu.Unlock()

	go r.monitorAgent(ctx, agentID, proc, worktreePath)
	go r.heartbeatLoop(ctx, agentID)
	go r.contextMonitorLoop(ctx, agentID, worktreePath)

	return nil
}

// startCodexAgent sets up and starts a Codex CLI agent subprocess. Codex
// agents use their own per-agent .codex directory and skip Claude hooks,
// status-line scripts, and compaction signals.
func (r *Runner) startCodexAgent(agentID, taskID uuid.UUID, worktreePath, branch, sessionName, prompt string, agentType model.AgentType) error {
	codexDir := filepath.Join(worktreePath, ".codex", "agents", agentID.String())
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		return fmt.Errorf("start codex agent: mkdir .codex/agents: %w", err)
	}

	promptPath := filepath.Join(codexDir, "agent-prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return fmt.Errorf("start codex agent: write prompt: %w", err)
	}

	if err := writeAgentMetadata(codexDir, agentID, taskID); err != nil {
		return fmt.Errorf("start codex agent: write agent metadata: %w", err)
	}

	cliConfig := r.agentConfigs(agentType)
	proc, err := StartCodexProcess(context.Background(), r.codexBin, promptPath, worktreePath, cliConfig.CLIArgs(), codexDir)
	if err != nil {
		return fmt.Errorf("start codex agent: start subprocess: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	ra := &RunningAgent{
		AgentID:      agentID,
		TaskID:       taskID,
		WorktreePath: worktreePath,
		Branch:       branch,
		TmuxSession:  sessionName,
		Process:      proc,
		LogPath:      proc.LogPath(),
		Provider:     model.ProviderCodex,
		StartedAt:    time.Now(),
		cancel:       cancel,
	}

	r.mu.Lock()
	r.running[agentID] = ra
	r.mu.Unlock()

	go r.monitorAgent(ctx, agentID, proc, worktreePath)
	go r.heartbeatLoop(ctx, agentID)
	go r.contextMonitorLoop(ctx, agentID, worktreePath)

	return nil
}
