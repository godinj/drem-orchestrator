package orchestrator

import (
	"strings"
	"testing"
)

// TestSamePackageTestPhaseOverlap_Warning verifies that ValidatePlan produces
// a warning when two test-phase subtasks in the same wave group (no dependency)
// target files in the same Go package. Such subtasks likely need shared stubs
// and should either list those stubs in estimated_files or be sequenced.
func TestSamePackageTestPhaseOverlap_Warning(t *testing.T) {
	// Two test subtasks targeting files in the same Go package (internal/exitlog)
	// with no dependency between them. Both cover separate impl subtasks.
	subtasks := []planEntry{
		{
			Title:          "Implement exitlog hooks",
			Phase:          "implementation",
			EstimatedFiles: []string{"internal/exitlog/exitlog.go"},
		},
		{
			Title:          "Implement exitlog formatting",
			Phase:          "implementation",
			EstimatedFiles: []string{"internal/exitlog/format.go"},
		},
		{
			Title:          "Test exitlog hooks",
			Phase:          "test",
			TestsFor:       []int{0},
			EstimatedFiles: []string{"internal/exitlog/exitlog_test.go"},
		},
		{
			Title:          "Test exitlog formatting",
			Phase:          "test",
			TestsFor:       []int{1},
			EstimatedFiles: []string{"internal/exitlog/hooks_test.go"},
		},
	}

	result := ValidatePlan(subtasks, nil)

	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "same Go package") && strings.Contains(w, "shared stub") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about test subtasks in same Go package needing shared stubs, got warnings: %v", result.Warnings)
	}
}

// TestSamePackageTestPhaseOverlap_DifferentPackages verifies that no warning
// is produced when two test-phase subtasks target files in different Go packages.
func TestSamePackageTestPhaseOverlap_DifferentPackages(t *testing.T) {
	subtasks := []planEntry{
		{
			Title:          "Implement exitlog hooks",
			Phase:          "implementation",
			EstimatedFiles: []string{"internal/exitlog/exitlog.go"},
		},
		{
			Title:          "Implement merge logic",
			Phase:          "implementation",
			EstimatedFiles: []string{"internal/merge/merge.go"},
		},
		{
			Title:          "Test exitlog hooks",
			Phase:          "test",
			TestsFor:       []int{0},
			EstimatedFiles: []string{"internal/exitlog/exitlog_test.go"},
		},
		{
			Title:          "Test merge logic",
			Phase:          "test",
			TestsFor:       []int{1},
			EstimatedFiles: []string{"internal/merge/merge_test.go"},
		},
	}

	result := ValidatePlan(subtasks, nil)

	for _, w := range result.Warnings {
		if strings.Contains(w, "same Go package") && strings.Contains(w, "shared stub") {
			t.Errorf("unexpected same-package warning for subtasks in different packages: %s", w)
		}
	}
}

// TestSamePackageTestPhaseOverlap_WithDependency verifies that no warning
// is produced when two test-phase subtasks target the same Go package but
// have an explicit dependency between them (they are sequenced).
func TestSamePackageTestPhaseOverlap_WithDependency(t *testing.T) {
	subtasks := []planEntry{
		{
			Title:          "Implement exitlog hooks",
			Phase:          "implementation",
			EstimatedFiles: []string{"internal/exitlog/exitlog.go"},
		},
		{
			Title:          "Implement exitlog formatting",
			Phase:          "implementation",
			EstimatedFiles: []string{"internal/exitlog/format.go"},
		},
		{
			Title:          "Test exitlog hooks",
			Phase:          "test",
			TestsFor:       []int{0},
			EstimatedFiles: []string{"internal/exitlog/exitlog_test.go"},
		},
		{
			Title:          "Test exitlog formatting",
			Phase:          "test",
			TestsFor:       []int{1},
			Dependencies:   []int{2}, // depends on the first test subtask
			EstimatedFiles: []string{"internal/exitlog/hooks_test.go"},
		},
	}

	result := ValidatePlan(subtasks, nil)

	for _, w := range result.Warnings {
		if strings.Contains(w, "same Go package") && strings.Contains(w, "shared stub") {
			t.Errorf("unexpected same-package warning when dependency exists: %s", w)
		}
	}
}

// TestSamePackageImplPhase_NoWarning verifies that no warning is produced when
// two implementation-phase subtasks target files in the same Go package.
// The same-package warning rule is specific to test-phase subtasks.
func TestSamePackageImplPhase_NoWarning(t *testing.T) {
	subtasks := []planEntry{
		{
			Title:          "Implement exitlog hooks",
			Phase:          "implementation",
			EstimatedFiles: []string{"internal/exitlog/exitlog.go"},
		},
		{
			Title:          "Implement exitlog formatting",
			Phase:          "implementation",
			EstimatedFiles: []string{"internal/exitlog/format.go"},
		},
		{
			Title:          "Test exitlog hooks",
			Phase:          "test",
			TestsFor:       []int{0},
			EstimatedFiles: []string{"internal/exitlog/exitlog_test.go"},
		},
		{
			Title:          "Test exitlog formatting",
			Phase:          "test",
			TestsFor:       []int{1},
			EstimatedFiles: []string{"internal/merge/format_test.go"},
		},
	}

	result := ValidatePlan(subtasks, nil)

	for _, w := range result.Warnings {
		if strings.Contains(w, "same Go package") && strings.Contains(w, "shared stub") {
			t.Errorf("unexpected same-package warning for implementation-phase subtasks: %s", w)
		}
	}
}

// TestSamePackageTestPhase_DifferentWaveGroups_NoWarning verifies that no warning
// is produced when test subtasks target the same Go package but are in different
// wave groups (one depends on the other, so they won't run in parallel).
func TestSamePackageTestPhase_DifferentWaveGroups_NoWarning(t *testing.T) {
	subtasks := []planEntry{
		{
			Title:          "Implement exitlog hooks",
			Phase:          "implementation",
			EstimatedFiles: []string{"internal/exitlog/exitlog.go"},
		},
		{
			Title:          "Implement exitlog formatting",
			Phase:          "implementation",
			EstimatedFiles: []string{"internal/exitlog/format.go"},
		},
		{
			Title:          "Test exitlog hooks",
			Phase:          "test",
			TestsFor:       []int{0},
			EstimatedFiles: []string{"internal/exitlog/exitlog_test.go"},
		},
		{
			Title:          "Test exitlog formatting",
			Phase:          "test",
			TestsFor:       []int{1},
			Dependencies:   []int{2}, // depends on subtask 2, different wave group
			EstimatedFiles: []string{"internal/exitlog/hooks_test.go"},
		},
	}

	result := ValidatePlan(subtasks, nil)

	for _, w := range result.Warnings {
		if strings.Contains(w, "same Go package") && strings.Contains(w, "shared stub") {
			t.Errorf("unexpected same-package warning when subtasks are in different wave groups: %s", w)
		}
	}
}
