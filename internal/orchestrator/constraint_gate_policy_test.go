package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/constraints"
)

func TestDefaultConstraintGatePolicy(t *testing.T) {
	p := DefaultConstraintGatePolicy()

	if p.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d, want 5", p.MaxRetries)
	}
	if p.BaseDelay != 30*time.Second {
		t.Errorf("BaseDelay = %v, want 30s", p.BaseDelay)
	}
	if p.MaxDelay != 10*time.Minute {
		t.Errorf("MaxDelay = %v, want 10m", p.MaxDelay)
	}
}

func TestConstraintGatePolicy_Delay_ExponentialBackoff(t *testing.T) {
	p := ConstraintGatePolicy{
		MaxRetries: 10,
		BaseDelay:  30 * time.Second,
		MaxDelay:   10 * time.Minute,
	}

	// Run each attempt multiple times to get a stable range with jitter.
	tests := []struct {
		attempt int
		minBase time.Duration // expected base without jitter
	}{
		{1, 30 * time.Second},
		{2, 60 * time.Second},
		{3, 120 * time.Second},
		{4, 240 * time.Second},
	}

	for _, tt := range tests {
		// Delay should be in [minBase, minBase*1.5] to account for jitter.
		// We allow some tolerance: at minimum the base value, at most 1.5x.
		d := p.Delay(tt.attempt)
		if d < tt.minBase {
			t.Errorf("Delay(%d) = %v, want >= %v (base without jitter)", tt.attempt, d, tt.minBase)
		}
		upperBound := tt.minBase + tt.minBase/2 // 1.5x base
		if d > upperBound {
			t.Errorf("Delay(%d) = %v, want <= %v (base * 1.5 with jitter)", tt.attempt, d, upperBound)
		}
	}
}

func TestConstraintGatePolicy_Delay_CappedAtMaxDelay(t *testing.T) {
	p := ConstraintGatePolicy{
		MaxRetries: 10,
		BaseDelay:  30 * time.Second,
		MaxDelay:   10 * time.Minute,
	}

	// Attempt 9: base would be 30s * 2^8 = 7680s = 128min, well over 10min cap.
	d := p.Delay(9)
	if d > p.MaxDelay+p.MaxDelay/2 {
		t.Errorf("Delay(9) = %v, want <= %v (MaxDelay with jitter)", d, p.MaxDelay+p.MaxDelay/2)
	}
}

func TestConstraintGatePolicy_Delay_IncludesJitter(t *testing.T) {
	p := ConstraintGatePolicy{
		MaxRetries: 10,
		BaseDelay:  30 * time.Second,
		MaxDelay:   10 * time.Minute,
	}

	// Call Delay many times for the same attempt; at least two results should differ.
	seen := make(map[time.Duration]bool)
	for i := 0; i < 50; i++ {
		seen[p.Delay(2)] = true
	}
	if len(seen) < 2 {
		t.Errorf("Delay(2) returned identical value across 50 calls; expected jitter")
	}
}

func TestConstraintGatePolicy_Exhausted(t *testing.T) {
	p := ConstraintGatePolicy{MaxRetries: 5}

	for attempt := 0; attempt < 5; attempt++ {
		if p.Exhausted(attempt) {
			t.Errorf("Exhausted(%d) = true, want false (< MaxRetries)", attempt)
		}
	}
	if !p.Exhausted(5) {
		t.Error("Exhausted(5) = false, want true (== MaxRetries)")
	}
	if !p.Exhausted(6) {
		t.Error("Exhausted(6) = false, want true (> MaxRetries)")
	}
}

func TestConstraintGatePolicy_ShouldTerminateEarly_Improvement(t *testing.T) {
	p := DefaultConstraintGatePolicy()

	// current magnitude < previous magnitude — improvement detected, must NOT terminate.
	current := constraints.ComparisonResult{
		Dominated:     true,
		NewViolations: []string{"a", "b"}, // magnitude=2
	}
	previous := constraints.ComparisonResult{
		Dominated:     true,
		NewViolations: []string{"a", "b", "c", "d", "e"}, // magnitude=5
	}
	if p.ShouldTerminateEarly(current, previous) {
		t.Error("ShouldTerminateEarly = true, want false (magnitude improved 5→2)")
	}
}

func TestConstraintGatePolicy_ShouldTerminateEarly_NoImprovement(t *testing.T) {
	p := DefaultConstraintGatePolicy()

	same := constraints.ComparisonResult{
		Dominated:     true,
		NewViolations: []string{"a", "b", "c", "d", "e"}, // magnitude=5
	}
	previous := constraints.ComparisonResult{
		Dominated:     true,
		NewViolations: []string{"x", "y", "z", "w", "v"}, // magnitude=5
	}

	// Same magnitude — no improvement, should terminate.
	if !p.ShouldTerminateEarly(same, previous) {
		t.Error("ShouldTerminateEarly = false, want true (magnitude unchanged 5→5)")
	}

	// Regression — magnitude grew, should also terminate.
	worse := constraints.ComparisonResult{
		Dominated:     true,
		NewViolations: []string{"a", "b", "c", "d", "e", "f", "g"}, // magnitude=7
	}
	if !p.ShouldTerminateEarly(worse, previous) {
		t.Error("ShouldTerminateEarly = false, want true (regression 5→7)")
	}
}

func TestConstraintGatePolicy_ShouldTerminateEarly_FirstAttempt(t *testing.T) {
	p := DefaultConstraintGatePolicy()

	// previous.Dominated=false means no prior blocked baseline — must NOT terminate.
	current := constraints.ComparisonResult{
		Dominated:     true,
		NewViolations: []string{"a", "b", "c"}, // magnitude=3
	}
	previous := constraints.ComparisonResult{} // Dominated=false, Magnitude()=0
	if p.ShouldTerminateEarly(current, previous) {
		t.Error("ShouldTerminateEarly = true, want false (first blocked attempt, no prior baseline)")
	}
}

func TestConstraintGatePolicy_ShouldTerminateEarly_WorsensVsNewViolations(t *testing.T) {
	p := DefaultConstraintGatePolicy()

	// Worsened constraints also count toward magnitude; combined with new violations
	// the total must be compared correctly.
	current := constraints.ComparisonResult{
		Dominated:     true,
		NewViolations: []string{"gofmt"},   // magnitude 1
		Worsened:      []string{"imports"}, // magnitude 1; total=2
	}
	previous := constraints.ComparisonResult{
		Dominated: true,
		Worsened:  []string{"imports", "line-limit", "depth"}, // magnitude=3
	}
	// current magnitude=2 < previous magnitude=3 → improvement → should NOT terminate.
	if p.ShouldTerminateEarly(current, previous) {
		t.Error("ShouldTerminateEarly = true, want false (combined magnitude improved 3→2)")
	}
}

func TestFormatConstraintFailure(t *testing.T) {
	failures := []ConstraintFailure{
		{Name: "file_length", Current: "850", Limit: "800"},
		{Name: "import_ceiling", Current: "7", Limit: "6"},
	}

	result := FormatConstraintFailure(failures, 3, 5)

	// Should contain attempt info.
	if !strings.Contains(result, "3") || !strings.Contains(result, "5") {
		t.Errorf("output missing attempt/max info: %s", result)
	}
	// Should list each failed constraint.
	if !strings.Contains(result, "file_length") {
		t.Errorf("output missing constraint name 'file_length': %s", result)
	}
	if !strings.Contains(result, "import_ceiling") {
		t.Errorf("output missing constraint name 'import_ceiling': %s", result)
	}
	// Should contain current and limit values.
	if !strings.Contains(result, "850") || !strings.Contains(result, "800") {
		t.Errorf("output missing current/limit for file_length: %s", result)
	}
	if !strings.Contains(result, "7") || !strings.Contains(result, "6") {
		t.Errorf("output missing current/limit for import_ceiling: %s", result)
	}
}

func TestFormatConstraintFailure_Empty(t *testing.T) {
	result := FormatConstraintFailure(nil, 1, 5)

	// Should still contain attempt info even with no failures.
	if !strings.Contains(result, "1") || !strings.Contains(result, "5") {
		t.Errorf("output missing attempt/max info for empty failures: %s", result)
	}
}
