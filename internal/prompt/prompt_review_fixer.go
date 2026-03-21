package prompt

import "fmt"

// reviewerInstructions returns prompt sections for reviewer agents.
// The review mode (plan vs feature) determines the specific instructions.
func reviewerInstructions(opts Opts) []string {
	if opts.ReviewMode == "plan" {
		return planReviewerInstructions(opts)
	}
	return featureReviewerInstructions(opts)
}

// planReviewerInstructions generates instructions for plan review.
func planReviewerInstructions(opts Opts) []string {
	var sections []string
	sections = append(sections, "## Instructions", "")
	sections = append(sections,
		"You are a plan reviewer agent. A planner has produced the following plan. "+
			"Evaluate it against the task's acceptance criteria.",
		"",
	)

	if opts.PlanJSON != "" {
		sections = append(sections, "## Plan", "")
		sections = append(sections, "```json")
		sections = append(sections, opts.PlanJSON)
		sections = append(sections, "```", "")
	}

	sections = append(sections,
		"## Review Criteria",
		"",
		"Evaluate the plan for:",
		"1. **Coverage**: Does every acceptance criterion from the task description have at least one subtask addressing it?",
		"2. **File overlap**: Do subtasks share files? High overlap means merge conflicts and serialized execution.",
		"3. **Integration**: Is there a final integration subtask that wires pieces together?",
		"4. **Decomposition quality**: Are subtasks sized appropriately? (3-6 is typical)",
		"5. **Dependency correctness**: Are dependencies between subtasks correct?",
		"6. **TDD structure**: Does every implementation subtask have a corresponding test subtask with `tests_for`?",
		"7. **Test quality**: Are test subtask descriptions specific about what behavior they verify?",
		`8. **TDD exceptions**: Are exceptions justified? (integration wiring and research are valid; "too hard to test" is not)`,
		"9. **Documentation**: If the feature changes user-facing behavior (CLI, config, TUI, new capabilities), "+
			"does at least one subtask update the README or add a walkthrough? Flag if missing.",
		"",
		"## Output",
		"",
		"Produce a `review.json` file in the working directory root:",
		"",
		"```json",
		"{",
		`  "coverage": "full|partial|none",`,
		`  "uncovered_criteria": ["criterion not addressed by any subtask"],`,
		`  "file_overlap_risk": "low|medium|high",`,
		`  "overlapping_pairs": [{"a": 0, "b": 2, "files": ["shared.go"]}],`,
		`  "integration_gap": true,`,
		`  "tdd_assessment": {`,
		`    "test_coverage_adequate": true,`,
		`    "exceptions_justified": true,`,
		`    "issues": ["Test subtask 0 only tests happy path, missing edge cases for..."]`,
		"  },",
		`  "issues": ["issue description"],`,
		`  "recommendation": "approve|revise|reject"`,
		"}",
		"```",
		"",
		"Do NOT modify any code. Do NOT commit anything. Only produce review.json.",
		"",
	)

	return sections
}

// featureReviewerInstructions generates instructions for feature review.
func featureReviewerInstructions(opts Opts) []string {
	var sections []string
	sections = append(sections, "## Instructions", "")
	sections = append(sections,
		"You are a feature reviewer agent. All subtasks have been merged into "+
			"the integration branch. Review the code changes against the acceptance criteria.",
		"",
	)

	if opts.GitDiff != "" {
		// Truncate very large diffs to avoid overwhelming the prompt.
		diff := opts.GitDiff
		if len(diff) > maxDiffLen {
			diff = diff[:maxDiffLen] + "\n... (truncated)"
		}
		sections = append(sections, "## Changes (git diff)", "")
		sections = append(sections, "```diff")
		sections = append(sections, diff)
		sections = append(sections, "```", "")
	}

	sections = append(sections,
		"## Review Process",
		"",
		"1. Run the FULL test suite first and record results",
		"2. Read the acceptance criteria from the task description carefully",
		"3. Examine the code changes shown above",
		"4. Run the build command to verify compilation",
		"5. For each acceptance criterion, verify it is addressed by the code",
		"6. If the feature changes user-facing behavior, verify that README or "+
			"documentation has been updated to reflect the changes",
		"",
		"## Output",
		"",
		"Produce a `review.json` file in the working directory root:",
		"",
		"```json",
		"{",
		`  "test_results": {`,
		`    "passed": true,`,
		`    "total": 42,`,
		`    "failed": 0,`,
		`    "output_summary": "..."`,
		"  },",
		`  "build_passes": true,`,
		`  "tests_pass": true,`,
		`  "criteria_results": [`,
		`    {"criterion": "...", "met": true, "evidence": "file:line"}`,
		"  ],",
		`  "issues": ["missing wiring between X and Y"],`,
		`  "recommendation": "approve|needs_work"`,
		"}",
		"```",
		"",
		"Do NOT modify any code. Do NOT commit anything. Only produce review.json.",
		"",
	)

	return sections
}

// fixerInstructions returns prompt sections for fixer agents.
func fixerInstructions(opts Opts) []string {
	var sections []string
	sections = append(sections, "## Instructions", "")
	sections = append(sections,
		"You are a fixer agent. The integration branch has a specific issue "+
			"that needs a targeted fix. Apply ONLY the fix described below.",
		"",
	)

	if opts.Diagnosis != "" {
		sections = append(sections, "## Diagnosis", "")
		sections = append(sections, opts.Diagnosis, "")
	}

	if len(opts.AffectedFiles) > 0 {
		sections = append(sections, "## Affected Files", "")
		for _, f := range opts.AffectedFiles {
			sections = append(sections, fmt.Sprintf("- `%s`", f))
		}
		sections = append(sections, "")
	}

	if opts.SuggestedFix != "" {
		sections = append(sections, "## Suggested Fix", "")
		sections = append(sections, opts.SuggestedFix, "")
	}

	sections = append(sections,
		"## Rules",
		"",
		"1. Apply ONLY the fix described above — do not refactor or change anything else",
		"2. Run the build command to verify the fix works",
		"3. Run tests if applicable",
		"4. Commit with a message describing the fix",
		"5. The fix should be minimal — the smallest change that resolves the issue",
		"",
	)

	return sections
}

// defaultInstructions returns generic prompt sections for unknown agent types.
func defaultInstructions() []string {
	return []string{
		"## Instructions",
		"",
		"Complete the task as described above. Commit your changes when done.",
		"",
	}
}
