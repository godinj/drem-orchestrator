package prompt

import (
	"strings"
	"testing"
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
