package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildTestReviewerUserMessage(t *testing.T) {
	msg := buildTestReviewerUserMessage("Title", "Description", `{"completed_test_tasks":[]}`)
	require.Contains(t, msg, "Title")
	require.Contains(t, msg, "Description")
	require.Contains(t, msg, "completed_test_tasks")
}

func TestTestReviewerPromptIsFailClosed(t *testing.T) {
	for _, required := range []string{"Approve only", "ambiguous", "recommendation", "revise", "reject", "Do not modify"} {
		require.True(t, strings.Contains(testReviewerSystemPrompt, required), "missing %q", required)
	}
}
