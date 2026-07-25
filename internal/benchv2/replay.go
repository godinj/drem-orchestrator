package benchv2

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// ReplayAdapter executes the actual ownership-aware production regression in
// the pinned orchestrator fixture. The seed patch adds 100 deterministic
// diagnostic-order variants in the orchestrator package so unexported routing,
// DB transitions, scopes, and dependencies are exercised without inference.
type ReplayAdapter struct{}

func (ReplayAdapter) Name() string { return "ownership_replay" }

func (ReplayAdapter) Run(ctx context.Context, request TrialRequest) (HarnessRun, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, "go", "test", "./internal/orchestrator", "-run", "^TestCanvasBenchOwnershipReworkDiagnosticPermutations$", "-count=1")
	cmd.Dir = request.WorkDir
	out, err := cmd.CombinedOutput()
	duration := time.Since(start)
	trajectory := ATIFTrajectory{
		SchemaVersion: ATIFVersion, SessionID: request.Task.ID,
		Agent:        ATIFAgent{Name: "ownership-replay", Version: request.Harness.Version},
		FinalMetrics: ATIFMetrics{DurationMs: duration.Milliseconds()},
		Extra:        map[string]any{"inference_calls": 0, "diagnostic_permutations": 100, "command": cmd.Args},
	}
	run := HarnessRun{
		Output: string(out), StopReason: "deterministic", Telemetry: Telemetry{DurationMs: duration.Milliseconds(), CheckpointObserved: err == nil},
		ServerUsage: ServerUsage{Source: "not_applicable", Complete: true}, Trajectory: trajectory,
	}
	if err != nil {
		return run, fmt.Errorf("ownership replay failed: %w: %s", err, out)
	}
	return run, nil
}
