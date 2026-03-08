package prompt

import (
	"strings"
	"testing"
)

func TestPlannerInstructionsContainsNewSections(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	requiredHeaders := []string{
		"## Coverage Verification",
		"## Integration Subtask",
		"## Decomposition Rules",
		"## File Overlap",
		"## Test Subtasks",
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

	// Verify existing content is still present.
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

	// Verify key decomposition guidance is present.
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
	}

	for _, c := range keyContent {
		if !strings.Contains(output, c) {
			t.Errorf("plannerInstructions() missing integration subtask guidance: %q", c)
		}
	}
}

func TestPlannerInstructionsTestSubtasksContent(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	keyContent := []string{
		"Depend on ALL implementation subtasks",
		"implementation subtasks -> test subtask -> integration subtask",
	}

	for _, c := range keyContent {
		if !strings.Contains(output, c) {
			t.Errorf("plannerInstructions() missing test subtask guidance: %q", c)
		}
	}
}
