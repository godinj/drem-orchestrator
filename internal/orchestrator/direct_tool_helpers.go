package orchestrator

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// directAgentActivity tracks live activity metrics for a direct tool agent.
// Access is serialised by the agent loop (one tool at a time), but the
// OnIteration callback reads the fields from the same goroutine so no mutex
// is needed between the two callbacks. The mutex protects against concurrent
// DB reads in OnIteration racing with OnToolCall writes.
type directAgentActivity struct {
	mu           sync.Mutex
	lastTool     string
	lastTarget   string
	lastPhase    string
	filesWritten map[string]struct{}
	hasCommitted bool
}

// derivePhase maps a tool name + arguments to a human-readable phase string
// for TUI display.
func derivePhase(toolName string, args map[string]any) string {
	switch toolName {
	case "read", "grep", "glob":
		return "exploring"
	case "write", "edit":
		return "implementing"
	case "bash":
		cmd := bashCommandArg(args)
		cmdLower := strings.ToLower(cmd)
		switch {
		case strings.Contains(cmdLower, "go test") || strings.Contains(cmdLower, "pytest") ||
			strings.Contains(cmdLower, "npm test") || strings.Contains(cmdLower, "make test"):
			return "testing"
		case strings.Contains(cmdLower, "go build") || strings.Contains(cmdLower, "go vet") ||
			strings.Contains(cmdLower, "make build") || strings.Contains(cmdLower, "npm run build"):
			return "building"
		case strings.Contains(cmdLower, "git commit"):
			return "committing"
		default:
			return "executing"
		}
	}
	return ""
}

// deriveTarget extracts the most relevant file/path from tool arguments.
func deriveTarget(toolName string, args map[string]any) string {
	switch toolName {
	case "read", "write", "edit":
		if p, ok := args["path"].(string); ok && p != "" {
			return filepath.Base(p)
		}
		if p, ok := args["file_path"].(string); ok && p != "" {
			return filepath.Base(p)
		}
	case "grep":
		if p, ok := args["pattern"].(string); ok && p != "" {
			return p
		}
	case "glob":
		if p, ok := args["pattern"].(string); ok && p != "" {
			return p
		}
	case "bash":
		if cmd := bashCommandArg(args); cmd != "" {
			if len(cmd) > 40 {
				cmd = cmd[:40] + "…"
			}
			return cmd
		}
	}
	return ""
}

func bashCommandArg(args map[string]any) string {
	if cmd, ok := args["cmd"].(string); ok {
		return cmd
	}
	if cmd, ok := args["command"].(string); ok {
		return cmd
	}
	return ""
}

// wireDirectAgentCallbacks sets up OnIteration and OnToolCall on the given
// toolCfg, using the Orchestrator's DB handle for persistence. Both callbacks
// persist live metrics to the agent DB record so the TUI can display activity,
// context usage, and cost info.
func (o *Orchestrator) wireDirectAgentCallbacks(
	toolCfg *agent.DirectToolAgentConfig,
	agentID uuid.UUID,
) *directAgentActivity {
	act := &directAgentActivity{filesWritten: make(map[string]struct{})}
	db := o.db

	toolCfg.OnIteration = func(iteration, tokensIn, tokensOut, contextPct int) {
		now := time.Now()
		updates := map[string]any{
			"heartbeat_at": &now,
			"tokens_in":    tokensIn,
			"tokens_out":   tokensOut,
		}

		// Build Config updates: always read-modify-write the JSON field.
		var ag model.Agent
		act.mu.Lock()
		localTool := act.lastTool
		localTarget := act.lastTarget
		localPhase := act.lastPhase
		localFilesCount := len(act.filesWritten)
		localCommitted := act.hasCommitted
		act.mu.Unlock()

		if err := db.Select("config").Where("id = ?", agentID).First(&ag).Error; err == nil {
			if ag.Config == nil {
				ag.Config = make(model.JSONField)
			}
			if contextPct > 0 {
				ag.Config["context_used_pct"] = float64(contextPct)
			}
			if toolCfg.ContextLimit > 0 {
				ag.Config["context_window_size"] = float64(toolCfg.ContextLimit)
			}
			// Persist activity metrics.
			if localTool != "" {
				ag.Config["activity_tool"] = localTool
				ag.Config["activity_target"] = localTarget
				ag.Config["activity_phase"] = localPhase
				ag.Config["activity_files_touched"] = float64(localFilesCount)
				ag.Config["activity_committed"] = localCommitted
			}
			ag.Config["total_cost_usd"] = float64(0)
			ag.Config["cost_label"] = "local"
			updates["config"] = ag.Config
		}
		db.Model(&model.Agent{}).Where("id = ?", agentID).Updates(updates)
	}

	toolCfg.OnToolCall = func(toolName string, args map[string]any, resultLen int) {
		act.mu.Lock()
		defer act.mu.Unlock()

		act.lastTool = toolName
		act.lastTarget = deriveTarget(toolName, args)
		act.lastPhase = derivePhase(toolName, args)

		// Track written files.
		if toolName == "write" || toolName == "edit" {
			if p, ok := args["path"].(string); ok && p != "" {
				act.filesWritten[p] = struct{}{}
			}
			if p, ok := args["file_path"].(string); ok && p != "" {
				act.filesWritten[p] = struct{}{}
			}
		}

		// Detect git commits in bash commands.
		if toolName == "bash" {
			if cmd := bashCommandArg(args); strings.Contains(cmd, "git commit") {
				act.hasCommitted = true
			}
		}
	}

	return act
}

// applyContextThresholds copies the orchestrator's contextWarnPct/contextStopPct
// onto the per-run tool config so the in-loop monitor in RunDirectToolAgent
// uses the same thresholds as the subprocess-agent monitor in context_monitor.go.
// The model's context window size (ContextLimit) is left to the cfg author —
// it's a model attribute, not an orchestrator policy. When ContextLimit is 0
// these thresholds have no effect (monitor is disabled).
func (o *Orchestrator) applyContextThresholds(toolCfg *agent.DirectToolAgentConfig) {
	if toolCfg == nil {
		return
	}
	if o.contextWarnPct > 0 {
		toolCfg.ContextWarnPct = o.contextWarnPct
	}
	if o.contextStopPct > 0 {
		toolCfg.ContextStopPct = o.contextStopPct
	}
}

// persistDirectAgentContext writes context-monitor results, cost, and exit
// reason onto the agent record's Config so downstream consumers (TUI,
// monitoring, escalation logic in context_monitor.go) can see the
// direct-tool agent's final state just like they do for subprocess agents.
// maxIterations is the configured iteration budget so we can detect
// max_iterations exits.
func persistDirectAgentContext(ag *model.Agent, result *agent.DirectToolAgentResult, maxIterations int) {
	if ag == nil || result == nil {
		return
	}
	if ag.Config == nil {
		ag.Config = make(model.JSONField)
	}

	// Context usage.
	if result.FinalContextPct > 0 || result.StopReason != "" {
		ag.Config["context_used_pct"] = float64(result.FinalContextPct)
		ag.FinalContextPct = result.FinalContextPct
	}
	if result.StopReason != "" {
		ag.Config["stop_reason"] = result.StopReason
	}

	// Cost: direct agents use local inference, no API cost.
	ag.Config["total_cost_usd"] = float64(0)
	ag.Config["cost_label"] = "local"

	// Exit reason: derive from result fields.
	switch {
	case result.StopReason == "context_limit":
		ag.ExitReason = model.ExitReasonContextLimit
		ag.Config["exit_reason"] = model.ExitReasonContextLimit
	case result.StopReason == "no_progress":
		ag.ExitReason = model.ExitReasonNoProgress
		ag.Config["exit_reason"] = model.ExitReasonNoProgress
	case result.StopReason == agent.DirectToolStopReasonAnchorMismatch:
		ag.ExitReason = model.ExitReasonAnchorMismatch
		ag.Config["exit_reason"] = model.ExitReasonAnchorMismatch
	case result.StopReason == "token_budget":
		ag.ExitReason = model.ExitReasonTokenBudget
		ag.Config["exit_reason"] = model.ExitReasonTokenBudget
	case result.StopReason == "max_iterations":
		ag.ExitReason = model.ExitReasonMaxIterations
		ag.Config["exit_reason"] = model.ExitReasonMaxIterations
	case maxIterations > 0 && result.Iterations >= maxIterations:
		ag.ExitReason = model.ExitReasonMaxIterations
		ag.Config["exit_reason"] = model.ExitReasonMaxIterations
	case result.Output != "":
		ag.ExitReason = model.ExitReasonSuccess
		ag.Config["exit_reason"] = model.ExitReasonSuccess
	default:
		ag.ExitReason = model.ExitReasonEmptyOutput
		ag.Config["exit_reason"] = model.ExitReasonEmptyOutput
	}
}

// extractFileList converts a task.Context["estimated_files"] value into a
// string slice. Accepts []any (JSON round-trip) or []string.
func extractFileList(v any) []string {
	if v == nil {
		return nil
	}
	switch files := v.(type) {
	case []any:
		out := make([]string, 0, len(files))
		for _, f := range files {
			if s, ok := f.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return files
	}
	return nil
}
