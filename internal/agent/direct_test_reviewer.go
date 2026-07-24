package agent

import (
	"bytes"
	"fmt"

	"github.com/google/uuid"
)

const testReviewerSystemPrompt = `You are the test-review gate for a software project orchestrator. Test-writing workers have completed before implementation begins. Review the supplied approved plan, completed test-task evidence, and bounded feature diff.

Approve only when the evidence is unambiguous and the tests establish the requested behavior before implementation. Check that every implementation behavior has test coverage, assertions are behavior-specific, negative and edge cases from the task are represented, existing tests are preserved unless a minimal change is necessary, test changes stay in scope, and the diff does not contain production implementation disguised as test setup. These are red-state tests: they are expected to fail before implementation. A real failing assertion is valid, but an expected-failure suppression such as Catch2 [!mayfail] or [!shouldfail] is a defect because it prevents the deterministic red gate from proving the behavior is missing. Judge only test code actually present in the supplied diff. When planned coverage is absent, report it as missing; do not describe or critique a nonexistent test as though it were in the diff.

Respond with ONLY one JSON object:
{
  "coverage": "full" | "partial" | "none",
  "issues": ["concrete issue"],
  "recommendation": "approve" | "revise" | "reject"
}

Use "revise" for incomplete or ambiguous evidence. Use "reject" only for a fundamental mismatch. The "issues" array contains only defects that require a change; never put passing checks or positive confirmations there. When recommendation is "approve", "issues" MUST be an empty array. Do not modify files and do not include markdown.`

func buildTestReviewerUserMessage(title, description, evidenceJSON string) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Task\n\nTitle: %s\n\nDescription:\n%s\n\n", title, description)
	buf.WriteString("## Test review evidence\n\n```json\n")
	buf.WriteString(evidenceJSON)
	if len(evidenceJSON) == 0 || evidenceJSON[len(evidenceJSON)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString("```\n")
	return buf.String()
}

// RunDirectTestReviewer performs a no-tools SGLang review of the completed
// test-writing phase and writes the same review.json envelope consumed by the
// orchestrator's reviewer completion handler.
func RunDirectTestReviewer(cfg DirectPlanReviewerConfig, taskID uuid.UUID, title, description, evidenceJSON, outputDir string) (*DirectPlanReviewerResult, error) {
	return runDirectStructuredReviewer(cfg, taskID, "tests", testReviewerSystemPrompt,
		buildTestReviewerUserMessage(title, description, evidenceJSON), outputDir)
}
