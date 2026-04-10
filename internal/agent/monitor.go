package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agentmon"
	"github.com/godinj/drem-orchestrator/internal/ctxmon"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// Metric names recorded by contextMonitorLoop each tick.
const (
	metricContextPct  = "context_pct"
	metricCostUSD     = "cost_usd"
	metricTokenInput  = "token_input"
	metricTokenOutput = "token_output"
	metricToolCount   = "tool_count"
)

// contextMonitorLoop polls the context usage file and compaction signal for
// a running agent, updating in-memory state and the DB Config field.
func (r *Runner) contextMonitorLoop(ctx context.Context, agentID uuid.UUID, worktreePath string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// Resolve provider for this agent.
		r.mu.Lock()
		ra, ok := r.running[agentID]
		var provider model.ProviderType
		if ok {
			provider = ra.Provider
		}
		r.mu.Unlock()
		if !ok {
			return
		}

		var usage *ctxmon.Usage
		if provider == model.ProviderOpenCode {
			usage = r.readOpenCodeUsage(worktreePath, agentID)
		} else {
			usage = r.readClaudeUsage(worktreePath, agentID)
		}

		// Scan activity monitor (independent of context usage — works for both providers).
		r.mu.Lock()
		ra, ok = r.running[agentID]
		var actMon *agentmon.Monitor
		if ok && ra.ActivityMon != nil {
			actMon = ra.ActivityMon
		}
		r.mu.Unlock()

		var activity *agentmon.Activity
		if actMon != nil {
			act, scanErr := actMon.Scan()
			if scanErr != nil {
				slog.Warn("context monitor: activity scan", "agent_id", agentID, "error", scanErr)
			} else if act != nil {
				activity = act
				r.mu.Lock()
				if ra, ok := r.running[agentID]; ok {
					ra.Activity = activity
				}
				r.mu.Unlock()
			}
		}

		// Update context usage in-memory state (may be nil if no data yet).
		if usage != nil {
			r.mu.Lock()
			if ra, ok := r.running[agentID]; ok {
				ra.ContextUsage = usage
			}
			r.mu.Unlock()
		}

		// Persist to DB Config field. Always write activity if available;
		// only write context fields when usage data exists.
		configUpdate := model.JSONField{}
		if usage != nil {
			configUpdate["context_used_pct"] = usage.UsedPercent
			configUpdate["context_window_size"] = usage.ContextWindowSize
			configUpdate["total_cost_usd"] = usage.TotalCostUSD
			configUpdate["tokens_in"] = usage.TotalInputTokens
			configUpdate["tokens_out"] = usage.TotalOutputTokens
		}
		if activity != nil {
			configUpdate["activity_tool"] = activity.LastTool
			configUpdate["activity_target"] = activity.LastTarget
			configUpdate["activity_files_touched"] = len(activity.FilesTouched)
			configUpdate["activity_committed"] = activity.HasCommit
			configUpdate["activity_phase"] = activity.Phase
			configUpdate["activity_file_progress"] = activity.FileProgress
			configUpdate["activity_stuck_score"] = activity.StuckScore
		}
		if len(configUpdate) == 0 {
			continue
		}
		r.db.Model(&model.Agent{}).Where("id = ?", agentID).Update("config", configUpdate)

		// Record time-series metrics (additive — does not affect Config write above).
		if r.metricsRecorder != nil && usage != nil {
			r.recordMetrics(agentID, usage, activity)
		}
	}
}

// readClaudeUsage reads context usage for a Claude agent from the usage file
// and transcript, including compaction signal detection.
func (r *Runner) readClaudeUsage(worktreePath string, agentID uuid.UUID) *ctxmon.Usage {
	usagePath := ctxmon.UsageFilePath(worktreePath)
	signalPath := ctxmon.CompactionSignalPath(worktreePath)

	usage, err := ctxmon.ReadUsageFile(usagePath)
	if err != nil {
		slog.Warn("context monitor: read usage file", "agent_id", agentID, "error", err)
	}

	// Fall back to reading the session transcript directly if the
	// status line script hasn't produced a usage file (e.g. in -p mode).
	if usage == nil {
		usage, err = ctxmon.ReadTranscriptUsage(worktreePath)
		if err != nil {
			slog.Warn("context monitor: read transcript", "agent_id", agentID, "error", err)
			return nil
		}
	}

	// Check compaction signal.
	compacted := false
	if _, err := os.Stat(signalPath); err == nil {
		compacted = true
		os.Remove(signalPath)
	}

	if usage == nil && compacted {
		usage = &ctxmon.Usage{
			UsedPercent:         100,
			CompactionTriggered: true,
			LastUpdated:         time.Now(),
		}
	} else if usage != nil && compacted {
		usage.CompactionTriggered = true
	}

	return usage
}

// readOpenCodeUsage reads context usage for an OpenCode agent by scanning the
// JSONL output log for step_finish events and accumulating token counts.
func (r *Runner) readOpenCodeUsage(worktreePath string, agentID uuid.UUID) *ctxmon.Usage {
	logPath := filepath.Join(worktreePath, ".opencode", "agent-output.jsonl")
	f, err := os.Open(logPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var totalIn, totalOut int
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var evt openCodeEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}
		if evt.Type == "step_finish" {
			totalIn += evt.Part.Tokens.Input
			totalOut += evt.Part.Tokens.Output
		}
	}

	if totalIn == 0 && totalOut == 0 {
		return nil
	}

	return &ctxmon.Usage{
		TotalInputTokens:  totalIn,
		TotalOutputTokens: totalOut,
		UsedPercent:       0, // no context window tracking for OpenCode yet
		TotalCostUSD:      0, // local model, no cost
		LastUpdated:       time.Now(),
	}
}

// recordMetrics appends five metric rows per tick: context_pct, cost_usd,
// token_input, token_output, and tool_count. Errors are logged but do not
// interrupt the monitoring loop.
func (r *Runner) recordMetrics(agentID uuid.UUID, usage *ctxmon.Usage, activity *agentmon.Activity) {
	record := func(name string, value float64) {
		if err := r.metricsRecorder.Record(agentID, name, value, nil); err != nil {
			slog.Warn("context monitor: record metric", "agent_id", agentID, "metric", name, "error", err)
		}
	}

	record(metricContextPct, float64(usage.UsedPercent))
	record(metricCostUSD, usage.TotalCostUSD)
	record(metricTokenInput, float64(usage.TotalInputTokens))
	record(metricTokenOutput, float64(usage.TotalOutputTokens))

	var toolCount int
	if activity != nil {
		for _, count := range activity.ToolCounts {
			toolCount += count
		}
	}
	record(metricToolCount, float64(toolCount))
}

// GetContextUsage returns the latest context window usage for a running agent.
// Returns nil if the agent is not running or has no usage data yet.
func (r *Runner) GetContextUsage(agentID uuid.UUID) *ctxmon.Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	ra, ok := r.running[agentID]
	if !ok {
		return nil
	}
	return ra.ContextUsage
}

// monitorAgent is a background goroutine that waits for the agent subprocess
// to become idle (via the idle signal file) or exit, then gracefully shuts it
// down and sends a completion. For OpenCode agents, idle signal polling is
// skipped — only process exit is monitored.
func (r *Runner) monitorAgent(ctx context.Context, agentID uuid.UUID, proc *AgentProcess, worktreePath string) {
	// Resolve provider for this agent.
	r.mu.Lock()
	ra, raOk := r.running[agentID]
	var provider model.ProviderType
	if raOk {
		provider = ra.Provider
	}
	r.mu.Unlock()

	if provider == model.ProviderOpenCode {
		// OpenCode agents: no idle detection — just wait for process exit.
		select {
		case <-ctx.Done():
			return
		case <-proc.done:
			goto done
		}
	} else {
		// Claude agents: poll for idle signal or process exit.
		idleSignal := filepath.Join(worktreePath, ".claude", "agent-idle")
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return // StopAgent was called
			case <-proc.done:
				// Process exited on its own.
				goto done
			case <-ticker.C:
				if _, err := os.Stat(idleSignal); err == nil {
					// Idle signal detected — send /exit and wait.
					proc.SendExit()
					timer := time.NewTimer(sendExitGracePeriod)
					select {
					case <-proc.done:
						timer.Stop()
					case <-timer.C:
						proc.Kill()
					case <-ctx.Done():
						timer.Stop()
						return
					}
					goto done
				}
			}
		}
	}

done:
	// If context was cancelled during exit, don't send completion.
	select {
	case <-ctx.Done():
		return
	default:
	}

	exitCode, _ := proc.Wait()

	// Build completion and enrich with exit info by provider.
	comp := Completion{AgentID: agentID, ReturnCode: exitCode}
	if provider == model.ProviderOpenCode {
		logPath := filepath.Join(worktreePath, ".opencode", "agent-output.jsonl")
		enrichOpenCodeCompletion(&comp, logPath)
	} else {
		homeDir, _ := os.UserHomeDir()
		if homeDir != "" {
			projectDir := filepath.Join(homeDir, ".claude", "projects", ctxmon.ProjectDirName(worktreePath))
			enrichCompletion(&comp, projectDir)
		}
	}

	// Send completion.
	r.completions <- comp

	// Remove from running map.
	r.mu.Lock()
	delete(r.running, agentID)
	r.mu.Unlock()

	// Release semaphore.
	<-r.semaphore
}
