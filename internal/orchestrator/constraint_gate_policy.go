package orchestrator

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/constraints"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// MaxConstraintGateRetries is the maximum number of times the orchestrator will
// retry a constraint gate failure before failing the task.
const MaxConstraintGateRetries = 5

// constraintGateBaseDelay is the base delay for exponential backoff between
// constraint gate retry attempts.
const constraintGateBaseDelay = 30 * time.Second

// ConstraintGatePolicy encapsulates backoff, retry cap, and early-termination
// logic for constraint gate retries. Delay grows exponentially with jitter:
// baseDelay * 2^(attempt-1) * (1 + rand[0,0.5)), capped at maxDelay.
type ConstraintGatePolicy struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
}

// ConstraintFailure describes a single constraint that failed evaluation.
type ConstraintFailure struct {
	Name    string
	Current string
	Limit   string
}

// DefaultConstraintGatePolicy returns the standard retry policy for constraint
// gate operations.
func DefaultConstraintGatePolicy() ConstraintGatePolicy {
	return ConstraintGatePolicy{
		MaxRetries: MaxConstraintGateRetries,
		BaseDelay:  constraintGateBaseDelay,
		MaxDelay:   10 * time.Minute,
	}
}

// Delay returns the backoff duration for the given attempt number (1-based),
// including random jitter.
func (p ConstraintGatePolicy) Delay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := p.BaseDelay
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	if p.MaxDelay > 0 && d > p.MaxDelay {
		d = p.MaxDelay
	}
	// Add jitter: multiply by (1 + rand[0, 0.5))
	jitter := time.Duration(rand.Float64() * 0.5 * float64(d))
	return d + jitter
}

// Exhausted returns true if the attempt count has reached or exceeded MaxRetries.
func (p ConstraintGatePolicy) Exhausted(attempt int) bool {
	return attempt >= p.MaxRetries
}

// ShouldTerminateEarly returns true when the current per-constraint comparison
// shows no improvement over the previous attempt, indicating retries are
// unlikely to help. Returns false when previous.Dominated is false, meaning
// this is the first blocked evaluation and there is no prior baseline.
func (p ConstraintGatePolicy) ShouldTerminateEarly(current, previous constraints.ComparisonResult) bool {
	// No prior dominated result — first blocked attempt, do not terminate.
	if !previous.Dominated {
		return false
	}
	// Terminate if the number of blocking constraints has not decreased.
	return current.Magnitude() >= previous.Magnitude()
}

// FormatConstraintFailure produces a structured failure message listing each
// failed constraint with its current value, limit, and the attempt context.
func FormatConstraintFailure(failures []ConstraintFailure, attempt, maxRetries int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "constraint gate failed (attempt %d/%d)", attempt, maxRetries)
	if len(failures) == 0 {
		return b.String()
	}
	b.WriteString(":\n")
	for _, f := range failures {
		fmt.Fprintf(&b, "  - %s: current=%s, limit=%s\n", f.Name, f.Current, f.Limit)
	}
	return b.String()
}

// constraintGateContextKeys are the context keys managed by the constraint gate.
var constraintGateContextKeys = []string{
	"constraint_gate_retries",
	"constraint_gate_next_check",
	"constraint_gate_previous_failed",
	"constraint_violations",
}

// evaluateConstraintGate runs constraint evaluation with backoff, retry counting,
// early termination, and context cleanup. Returns (blocked, error) where blocked
// is true if the task should not transition (constraints failed or backoff active).
func (o *Orchestrator) evaluateConstraintGate(task *model.Task) (bool, error) {
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}

	policy := DefaultConstraintGatePolicy()

	// Check backoff: if next_check is in the future, skip evaluation.
	if nextCheckStr, ok := task.Context["constraint_gate_next_check"].(string); ok {
		nextCheck, err := time.Parse(time.RFC3339, nextCheckStr)
		if err == nil && time.Now().Before(nextCheck) {
			return true, nil
		}
	}

	fn := strings.TrimPrefix(task.WorktreeBranch, "feature/")
	featureDir := o.worktree.FeatureWorktreePath(fn)

	constraintCfg, cfgErr := constraints.LoadConfig(featureDir)
	if cfgErr != nil {
		o.logger.Warn("constraint config load failed at integration gate",
			"task_id", task.ID, "error", cfgErr)
		return false, nil
	}
	if constraintCfg == nil {
		return false, nil
	}

	featureReport, evalErr := constraints.Evaluate(constraintCfg, featureDir)
	if evalErr != nil {
		o.logger.Warn("constraint evaluation failed at integration gate",
			"task_id", task.ID, "error", evalErr)
		return false, nil
	}

	// Get baseline report from the master worktree for per-constraint delta comparison.
	masterReport := &constraints.Report{}
	if masterDir, err := o.worktree.MainWorktreePath(); err == nil {
		if masterCfg, err := constraints.LoadConfig(masterDir); err == nil && masterCfg != nil {
			if r, err := constraints.Evaluate(masterCfg, masterDir); err == nil {
				masterReport = r
			}
		}
	}

	comparison := constraints.CompareReports(masterReport, featureReport)
	if !comparison.Dominated {
		// No regressions relative to master — clear all gate context.
		for _, key := range constraintGateContextKeys {
			delete(task.Context, key)
		}
		return false, nil
	}

	// Constraints regressed relative to master — apply retry logic.
	o.logger.Warn("constraint violations at integration gate",
		"task_id", task.ID, "failed", featureReport.Failed)

	// Depth-specific diagnosis (advisory).
	for _, r := range featureReport.Results {
		if !r.Passed && r.Type == "depth" {
			o.checkDepthConstraintFailures(task, featureReport, featureDir)
			break
		}
	}

	retries := 0
	if v, ok := task.Context["constraint_gate_retries"].(float64); ok {
		retries = int(v)
	}
	previousMagnitude := 0
	if v, ok := task.Context["constraint_gate_previous_failed"].(float64); ok {
		previousMagnitude = int(v)
	}

	retries++

	// Check max retries exhaustion.
	if policy.Exhausted(retries) {
		reason := fmt.Sprintf("constraint gate exhausted after %d retries: %s",
			retries, constraints.FormatReport(featureReport))
		return true, o.failTask(task, reason)
	}

	// Check early termination (no improvement in per-constraint magnitude).
	previousResult := constraints.ComparisonResult{}
	if previousMagnitude > 0 {
		previousResult.Dominated = true
		previousResult.NewViolations = make([]string, previousMagnitude)
	}
	if policy.ShouldTerminateEarly(comparison, previousResult) {
		reason := fmt.Sprintf("constraint gate: no improvement in constraint failures (current=%d, previous=%d): %s",
			comparison.Magnitude(), previousMagnitude, constraints.FormatReport(featureReport))
		return true, o.failTask(task, reason)
	}

	// Schedule next check with backoff.
	delay := policy.Delay(retries)
	task.Context["constraint_gate_retries"] = float64(retries)
	task.Context["constraint_gate_previous_failed"] = float64(comparison.Magnitude())
	task.Context["constraint_gate_next_check"] = time.Now().Add(delay).Format(time.RFC3339)
	task.Context["constraint_violations"] = constraints.FormatReport(featureReport)

	// Add constraint violation counting
	violationCount := comparison.Magnitude()
	if task.AssignedAgentID != nil {
		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", *task.AssignedAgentID).Error; err == nil {
			ag.ConstraintViolations += violationCount
			o.db.Save(&ag)
		}
	}

	if err := o.db.Save(task).Error; err != nil {
		return true, fmt.Errorf("save constraint gate state: %w", err)
	}

	o.emit("constraint_violations", map[string]any{
		"task_id":    task.ID,
		"failed":     featureReport.Failed,
		"violations": constraints.FormatReport(featureReport),
	})

	return true, nil
}
