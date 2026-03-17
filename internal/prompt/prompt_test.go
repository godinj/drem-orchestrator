package prompt

import (
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestPlannerInstructionsTDDMandatory(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	requiredPhrases := []string{
		"MUST have exactly ONE",
		"test subtask",
		`phase: "test"`,
		"tests_for",
		"tdd_exceptions",
		"BEFORE implementation",
		"Tests should initially FAIL",
		"NEVER modify the pre-written tests",
	}

	for _, phrase := range requiredPhrases {
		if !strings.Contains(output, phrase) {
			t.Errorf("plannerInstructions() missing TDD phrase: %q", phrase)
		}
	}
}

func TestPlannerInstructionsNoOldTestOrdering(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	forbiddenPhrases := []string{
		"Depend on ALL implementation subtasks",
		"is_test",
	}

	for _, phrase := range forbiddenPhrases {
		if strings.Contains(output, phrase) {
			t.Errorf("plannerInstructions() still contains old test ordering phrase: %q", phrase)
		}
	}
}

func TestPlannerInstructionsContainsTDDSections(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	requiredHeaders := []string{
		"## Test-Driven Development (MANDATORY)",
		"### Test Subtask Requirements",
		"### Implementation Subtask Requirements",
		"### TDD Exceptions",
		"### Ordering",
		"### Example Structure",
		"## Coverage Verification",
		"## Integration Subtask",
		"## Decomposition Rules",
		"## File Overlap",
	}

	for _, header := range requiredHeaders {
		if !strings.Contains(output, header) {
			t.Errorf("plannerInstructions() missing section header: %q", header)
		}
	}
}

func TestPlannerInstructionsPreservesExistingContent(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	existingContent := []string{
		"You are a planner agent",
		"plan.json",
		"Each subtask should be independently implementable",
		"List specific files each subtask will create or modify",
		"Set dependencies between subtasks where order matters",
		`"coder" for implementation, "researcher" for investigation`,
	}

	for _, content := range existingContent {
		if !strings.Contains(output, content) {
			t.Errorf("plannerInstructions() missing existing content: %q", content)
		}
	}
}

func TestPlannerInstructionsDecompositionRulesContent(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	keyGuidance := []string{
		"Decompose along functional boundaries",
		"Decompose by code layer",
		"Plan more than 8 subtasks",
		"Omit the files list",
	}

	for _, g := range keyGuidance {
		if !strings.Contains(output, g) {
			t.Errorf("plannerInstructions() missing decomposition guidance: %q", g)
		}
	}
}

func TestPlannerInstructionsIntegrationSubtaskContent(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	keyContent := []string{
		"Wires together the components",
		"dependencies on ALL other implementation subtasks",
		"end-to-end functionality",
		`phase: "integration"`,
	}

	for _, c := range keyContent {
		if !strings.Contains(output, c) {
			t.Errorf("plannerInstructions() missing integration subtask guidance: %q", c)
		}
	}
}

func TestPlannerInstructionsTDDExceptions(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	validExceptions := []string{
		"Integration/wiring subtasks",
		"Research subtasks",
		"Infrastructure subtasks",
	}

	for _, exc := range validExceptions {
		if !strings.Contains(output, exc) {
			t.Errorf("plannerInstructions() missing valid exception: %q", exc)
		}
	}

	invalidExceptions := []string{
		"This is UI code",
		"This is a refactor",
		"Tests will be added later",
	}

	for _, exc := range invalidExceptions {
		if !strings.Contains(output, exc) {
			t.Errorf("plannerInstructions() missing invalid exception example: %q", exc)
		}
	}
}

func TestPlannerInstructionsExampleStructure(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	exampleContent := []string{
		"Write tests for X",
		"Implement X",
		"auto-depends on subtask 0 via tests_for",
		"Integration wiring",
		`phase: integration`,
	}

	for _, c := range exampleContent {
		if !strings.Contains(output, c) {
			t.Errorf("plannerInstructions() missing example structure content: %q", c)
		}
	}
}

func TestPlannerInstructionsPlanJSONSchema(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	schemaFields := []string{
		`"phase"`,
		`"tests_for"`,
		`"tdd_exceptions"`,
		`"subtask_index"`,
		`"justification"`,
	}

	for _, field := range schemaFields {
		if !strings.Contains(output, field) {
			t.Errorf("plannerInstructions() plan.json schema missing field: %q", field)
		}
	}
}

func TestPlanReviewerIncludesTDDAssessment(t *testing.T) {
	opts := Opts{
		ReviewMode: "plan",
		PlanJSON:   `{"subtasks": []}`,
	}
	sections := planReviewerInstructions(opts)
	output := strings.Join(sections, "\n")

	requiredPhrases := []string{
		"tdd_assessment",
		"TDD structure",
		"Test quality",
		"TDD exceptions",
		"exceptions_justified",
		"test_coverage_adequate",
	}

	for _, phrase := range requiredPhrases {
		if !strings.Contains(output, phrase) {
			t.Errorf("planReviewerInstructions() missing TDD assessment phrase: %q", phrase)
		}
	}
}

func TestPlanReviewerPreservesExistingCriteria(t *testing.T) {
	opts := Opts{
		ReviewMode: "plan",
		PlanJSON:   `{"subtasks": []}`,
	}
	sections := planReviewerInstructions(opts)
	output := strings.Join(sections, "\n")

	existingCriteria := []string{
		"Coverage",
		"File overlap",
		"Integration",
		"Decomposition quality",
		"Dependency correctness",
		"review.json",
		"recommendation",
	}

	for _, c := range existingCriteria {
		if !strings.Contains(output, c) {
			t.Errorf("planReviewerInstructions() missing existing criterion: %q", c)
		}
	}
}

// --- TDD Phase-Aware Coder Prompt Tests ---

func TestCoderInstructions_TestPhase(t *testing.T) {
	task := &model.Task{
		Phase: "test",
		Context: model.JSONField{
			"estimated_files": []any{"internal/foo/foo_test.go"},
		},
	}

	sections := coderInstructions(task)
	output := strings.Join(sections, "\n")

	mustContain := []string{
		"writing tests BEFORE implementation",
		"SHOULD fail",
		"test:",
		"FAIL when run",
		"existing test patterns",
	}
	for _, s := range mustContain {
		if !strings.Contains(output, s) {
			t.Errorf("test-phase coder prompt missing: %q", s)
		}
	}

	mustNotContain := []string{
		"Run the FULL test suite",
	}
	for _, s := range mustNotContain {
		if strings.Contains(output, s) {
			t.Errorf("test-phase coder prompt should NOT contain: %q", s)
		}
	}
}

func TestCoderInstructions_TestPhase_IncludesEstimatedFiles(t *testing.T) {
	task := &model.Task{
		Phase: "test",
		Context: model.JSONField{
			"estimated_files": []any{"internal/bar/bar_test.go", "internal/baz/baz_test.go"},
		},
	}

	sections := coderInstructions(task)
	output := strings.Join(sections, "\n")

	if !strings.Contains(output, "Files to create/modify:") {
		t.Error("test-phase coder prompt missing estimated files section")
	}
}

func TestCoderInstructions_ImplPhase_WithActualTestFiles(t *testing.T) {
	task := &model.Task{
		Phase: "implementation",
		Context: model.JSONField{
			"actual_test_files": []any{
				"internal/foo/foo_test.go",
				"internal/bar/bar_test.go",
			},
			"estimated_files": []any{
				"internal/old/old_test.go",
			},
		},
	}

	sections := coderInstructions(task)
	output := strings.Join(sections, "\n")

	mustContain := []string{
		"implementing code to pass pre-written tests",
		"pre-written tests",
		"Do NOT modify the pre-written tests",
		"internal/foo/foo_test.go",
		"internal/bar/bar_test.go",
		"FULL test suite",
		"ALL tests must pass",
		"NEVER modify pre-written TDD tests",
		"feat:",
	}
	for _, s := range mustContain {
		if !strings.Contains(output, s) {
			t.Errorf("impl-phase coder prompt missing: %q", s)
		}
	}

	// Should use actual_test_files, NOT estimated_files.
	if strings.Contains(output, "internal/old/old_test.go") {
		t.Error("impl-phase coder prompt should prefer actual_test_files over estimated_files")
	}
}

func TestCoderInstructions_ImplPhase_FallbackToEstimatedFiles(t *testing.T) {
	task := &model.Task{
		Phase: "implementation",
		Context: model.JSONField{
			"estimated_files": []any{
				"internal/fallback/fallback_test.go",
			},
		},
	}

	sections := coderInstructions(task)
	output := strings.Join(sections, "\n")

	if !strings.Contains(output, "internal/fallback/fallback_test.go") {
		t.Error("impl-phase coder prompt should fall back to estimated_files when actual_test_files is missing")
	}
	if !strings.Contains(output, "pre-written tests") {
		t.Error("impl-phase coder prompt missing pre-written tests reference")
	}
}

func TestCoderInstructions_ImplPhase_NoTestFiles(t *testing.T) {
	task := &model.Task{
		Phase: "implementation",
	}

	sections := coderInstructions(task)
	output := strings.Join(sections, "\n")

	// Should still produce valid instructions even without test files.
	if !strings.Contains(output, "implementing code to pass pre-written tests") {
		t.Error("impl-phase coder prompt missing core instruction text")
	}
	// Should NOT contain the "Pre-written tests exist at:" header since there are no files.
	if strings.Contains(output, "Pre-written tests exist at:") {
		t.Error("impl-phase coder prompt should not show test file list when none exist")
	}
}

func TestCoderInstructions_DefaultPhase_EmptyString(t *testing.T) {
	task := &model.Task{
		Phase: "",
		Context: model.JSONField{
			"estimated_files": []any{"internal/pkg/pkg.go"},
		},
	}

	sections := coderInstructions(task)
	output := strings.Join(sections, "\n")

	mustContain := []string{
		"You are a coder agent",
		"FULL test suite",
		"ALL tests must pass",
		"Do not commit if any test fails",
		"fix your implementation, not the test",
	}
	for _, s := range mustContain {
		if !strings.Contains(output, s) {
			t.Errorf("default coder prompt missing: %q", s)
		}
	}
}

func TestCoderInstructions_IntegrationPhase(t *testing.T) {
	task := &model.Task{
		Phase: "integration",
		Context: model.JSONField{
			"estimated_files": []any{"internal/wire/wire.go"},
		},
	}

	sections := coderInstructions(task)
	output := strings.Join(sections, "\n")

	// Integration phase uses the default coder instructions.
	mustContain := []string{
		"FULL test suite",
		"ALL tests must pass",
	}
	for _, s := range mustContain {
		if !strings.Contains(output, s) {
			t.Errorf("integration-phase coder prompt missing: %q", s)
		}
	}
}

func TestFeatureReviewerInstructions_IncludesTestResults(t *testing.T) {
	opts := Opts{
		Task:       &model.Task{},
		AgentType:  model.AgentReviewer,
		ReviewMode: "feature",
		GitDiff:    "diff --git a/foo.go b/foo.go\n+// new code",
	}

	sections := featureReviewerInstructions(opts)
	output := strings.Join(sections, "\n")

	mustContain := []string{
		"test_results",
		"Run the FULL test suite first",
		"output_summary",
	}
	for _, s := range mustContain {
		if !strings.Contains(output, s) {
			t.Errorf("feature reviewer prompt missing: %q", s)
		}
	}
}

func TestCoderInstructions_TestPhase_WithTestPlan(t *testing.T) {
	task := &model.Task{
		Phase:    "test",
		TestPlan: "Cover all edge cases for the parser",
	}

	sections := coderInstructions(task)
	output := strings.Join(sections, "\n")

	if !strings.Contains(output, "## Test Plan") {
		t.Error("test-phase coder prompt missing Test Plan section")
	}
	if !strings.Contains(output, "Cover all edge cases for the parser") {
		t.Error("test-phase coder prompt missing test plan content")
	}
}

func TestCoderInstructions_ImplPhase_WithTestPlan(t *testing.T) {
	task := &model.Task{
		Phase:    "implementation",
		TestPlan: "Ensure all parser tests pass",
	}

	sections := coderInstructions(task)
	output := strings.Join(sections, "\n")

	if !strings.Contains(output, "## Test Plan") {
		t.Error("impl-phase coder prompt missing Test Plan section")
	}
	if !strings.Contains(output, "Ensure all parser tests pass") {
		t.Error("impl-phase coder prompt missing test plan content")
	}
}

func TestCoderInstructions_DefaultPhase_WithTestPlan(t *testing.T) {
	task := &model.Task{
		Phase:    "",
		TestPlan: "Run all unit tests",
	}

	sections := coderInstructions(task)
	output := strings.Join(sections, "\n")

	if !strings.Contains(output, "## Test Plan") {
		t.Error("default coder prompt missing Test Plan section")
	}
	if !strings.Contains(output, "Run all unit tests") {
		t.Error("default coder prompt missing test plan content")
	}
}
