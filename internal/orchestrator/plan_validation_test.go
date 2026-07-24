package orchestrator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/pkg/score"
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

func TestValidatePlanWritableFilesOnlyNarrowIntegrationMutationScope(t *testing.T) {
	valid := ValidatePlan([]planEntry{
		{Title: "test", Phase: "test", Files: []string{"tests/audio_test.cpp"}, TestsFor: []int{1}},
		{Title: "implementation", Phase: "implementation", Files: []string{"src/audio.cpp"}, Dependencies: []int{0}},
		{Title: "integration", Phase: "integration", Files: []string{"src/audio.cpp", "cmake/audio.cmake"}, WritableFiles: []string{"cmake/audio.cmake"}, Dependencies: []int{1}},
	}, nil)
	require.Empty(t, valid.Errors)
	for _, warning := range valid.Warnings {
		require.NotContains(t, warning, "src/audio.cpp")
	}

	invalid := ValidatePlan([]planEntry{{Title: "test", Phase: "test", Files: []string{"tests/audio_test.cpp"}, WritableFiles: []string{"tests/audio_test.cpp"}}}, nil)
	require.Contains(t, invalid.Errors, "Subtask 0 ('test') sets writable_files outside integration")
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

func TestValidatePlanRejectsOversizedImplementationOwnership(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Test feature", Phase: "test", TestsFor: []int{1}, Files: []string{"feature_test.cpp"}},
		{Title: "Implement feature", Phase: "implementation", Files: []string{"Feature.h", "Feature.cpp", "FeatureRegistry.cpp"}},
	}

	result := ValidatePlan(subtasks, nil)
	if result.Valid || !strings.Contains(strings.Join(result.Errors, "\n"), "at most 2 files") {
		t.Fatalf("expected implementation file-limit error, got %v", result.Errors)
	}
}

func TestValidatePlanRejectsMultipleDeclaredImplementationBoundaries(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Test feature", Phase: "test", TestsFor: []int{1}, Files: []string{"feature_test.cpp"}},
		{
			Title: "Implement feature", Phase: "implementation", Files: []string{"Feature.cpp"},
			ModuleBoundaries: []score.ModuleBoundary{
				{Package: "model", Description: "Owns model behavior", Exports: 1},
				{Package: "ui", Description: "Owns UI behavior", Exports: 1},
			},
		},
	}

	result := ValidatePlan(subtasks, nil)
	if result.Valid || !strings.Contains(strings.Join(result.Errors, "\n"), "declares 2 module boundaries") {
		t.Fatalf("expected module-boundary granularity error, got %v", result.Errors)
	}
}

func TestValidatePlanRejectsSharedImplementationFileOwnership(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Test model", Phase: "test", TestsFor: []int{1}, Files: []string{"model_test.cpp"}},
		{Title: "Implement model", Phase: "implementation", Files: []string{"Shared.cpp"}},
		{Title: "Test UI", Phase: "test", TestsFor: []int{3}, Files: []string{"ui_test.cpp"}},
		{Title: "Implement UI", Phase: "implementation", Files: []string{"Shared.cpp"}},
	}

	result := ValidatePlan(subtasks, nil)
	if result.Valid || !strings.Contains(strings.Join(result.Errors, "\n"), "both own file") {
		t.Fatalf("expected exclusive implementation ownership error, got %v", result.Errors)
	}
}
