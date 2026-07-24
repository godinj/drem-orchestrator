package orchestrator

import (
	"context"

	"github.com/godinj/drem-orchestrator/internal/container"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"gorm.io/gorm"
)

// captureWorkerUsage copies the direct harness's terminal log summary into
// the durable attempt and public agent rows before completion processing.
// Missing or non-direct summaries are observability gaps, not task failures.
func (o *Orchestrator) captureWorkerUsage(ctx context.Context, attempt *model.WorkerAttempt, ev container.Event) *container.WorkerUsage {
	if o.Spawner == nil || attempt == nil || attempt.ContainerID == "" {
		return nil
	}
	usage := ev.Usage
	// A container death notification can race the final stderr flush. Even when
	// the event source attempted inspection, retry once if it captured no
	// summary; the stopped container remains the authoritative log source.
	if usage == nil {
		inspected, err := o.Spawner.InspectWorker(ctx, spawner.InspectWorkerParams{ContainerID: attempt.ContainerID})
		if err != nil {
			o.logger.Warn("capture worker usage: inspect", "attempt_id", attempt.ID, "error", err)
			return nil
		}
		usage = inspected.Usage
	}
	if usage == nil {
		return nil
	}
	if err := o.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.WorkerAttempt{}).Where("id = ?", attempt.ID).Updates(map[string]any{
			"tokens_in": usage.TokensIn, "tokens_out": usage.TokensOut, "final_context_pct": usage.ContextPct,
			"peak_request_input": usage.PeakRequestInput, "resumed_turns": usage.ResumedTurns, "folded_bytes": usage.FoldedBytes,
		}).Error; err != nil {
			return err
		}
		attempt.TokensIn = usage.TokensIn
		attempt.TokensOut = usage.TokensOut
		attempt.FinalContextPct = usage.ContextPct
		if attempt.AgentID == nil {
			return nil
		}

		var ag model.Agent
		if err := tx.First(&ag, "id = ?", *attempt.AgentID).Error; err != nil {
			return err
		}
		if ag.Config == nil {
			ag.Config = make(model.JSONField)
		}
		ag.Config["tokens_in"] = usage.TokensIn
		ag.Config["tokens_out"] = usage.TokensOut
		ag.Config["direct_iterations"] = usage.Iterations
		ag.Config["direct_stop_reason"] = usage.StopReason
		ag.Config["direct_context_pct"] = usage.ContextPct
		ag.Config["direct_peak_request_input"] = usage.PeakRequestInput
		ag.Config["direct_resumed_turns"] = usage.ResumedTurns
		ag.Config["direct_folded_bytes"] = usage.FoldedBytes
		return tx.Model(&model.Agent{}).Where("id = ?", ag.ID).Updates(map[string]any{
			"tokens_in": usage.TokensIn, "tokens_out": usage.TokensOut, "config": ag.Config,
		}).Error
	}); err != nil {
		o.logger.Warn("capture worker usage: persist", "attempt_id", attempt.ID, "error", err)
	}
	return usage
}
