package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agentmon"
	"github.com/godinj/drem-orchestrator/internal/ctxmon"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// contextMonitorLoop polls the context usage file and compaction signal for
// a running agent, updating in-memory state and the DB Config field.
func (r *Runner) contextMonitorLoop(ctx context.Context, agentID uuid.UUID, worktreePath string) {
	usagePath := ctxmon.UsageFilePath(worktreePath)
	signalPath := ctxmon.CompactionSignalPath(worktreePath)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

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
				continue
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

		// Scan activity monitor (independent of context usage).
		r.mu.Lock()
		ra, ok := r.running[agentID]
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
	}
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
// down and sends a completion.
func (r *Runner) monitorAgent(ctx context.Context, agentID uuid.UUID, proc *AgentProcess, worktreePath string) {
	idleSignal := filepath.Join(worktreePath, ".claude", "agent-idle")

	// Poll for idle signal or process exit.
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

done:
	// If context was cancelled during exit, don't send completion.
	select {
	case <-ctx.Done():
		return
	default:
	}

	exitCode, _ := proc.Wait()

	// Send completion.
	r.completions <- Completion{AgentID: agentID, ReturnCode: exitCode}

	// Remove from running map.
	r.mu.Lock()
	delete(r.running, agentID)
	r.mu.Unlock()

	// Release semaphore.
	<-r.semaphore
}
