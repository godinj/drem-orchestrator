package supervisor

import (
	"fmt"
	"strings"
)

// FailureDiagnosisPrompt builds a prompt for diagnosing why an agent failed.
func FailureDiagnosisPrompt(taskTitle, taskDesc, agentType, agentOutput, lastError string) string {
	return fmt.Sprintf(`You are a supervisor analyzing why a Claude Code agent failed.

## Task
- **Title**: %s
- **Description**: %s
- **Agent Type**: %s

## Agent Output (last portion)
%s

## Error
%s

## Instructions
Diagnose the failure and decide whether to retry. Return ONLY a JSON object:

{
  "root_cause": "brief description of what went wrong",
  "category": "transient|prompt_issue|code_error|environment|unknown",
  "should_retry": true/false,
  "retry_strategy": "same_prompt|modified_prompt|different_approach",
  "prompt_adjustment": "if retry_strategy is modified_prompt, describe what to change",
  "max_additional_retries": 1-3
}`,
		taskTitle,
		truncateForPrompt(taskDesc, 1000),
		agentType,
		truncateForPrompt(agentOutput, 3000),
		truncateForPrompt(lastError, 500),
	)
}

// FeedbackIntegrationPrompt builds a prompt for synthesizing user feedback
// into actionable guidance for a retried agent.
func FeedbackIntegrationPrompt(taskTitle, taskDesc, feedback, feedbackType string) string {
	return fmt.Sprintf(`You are a supervisor synthesizing user feedback for a Claude Code agent.

## Task
- **Title**: %s
- **Description**: %s

## User Feedback (%s)
%s

## Instructions
Synthesize this feedback into clear, actionable guidance. Return ONLY a JSON object:

{
  "summary": "concise synthesis of what the user wants changed",
  "key_issues": ["issue1", "issue2"],
  "suggested_approach": "specific guidance for the next agent attempt"
}`,
		taskTitle,
		truncateForPrompt(taskDesc, 1000),
		feedbackType,
		truncateForPrompt(feedback, 2000),
	)
}

// MergeConflictPrompt builds a prompt for analyzing merge conflicts.
func MergeConflictPrompt(sourceBranch, targetBranch string, conflicts []string, diffOutput string) string {
	return fmt.Sprintf(`You are a supervisor analyzing merge conflicts.

## Merge
- **Source**: %s
- **Target**: %s
- **Conflicting files**: %s

## Diff Output
%s

## Instructions
Analyze the conflicts and suggest a resolution strategy. Return ONLY a JSON object:

{
  "severity": "trivial|moderate|complex",
  "conflict_summaries": {"file1.go": "description of conflict", ...},
  "resolution_strategy": "auto_resolve|spawn_agent|manual",
  "resolution_hints": "specific guidance for resolving the conflicts"
}`,
		sourceBranch,
		targetBranch,
		strings.Join(conflicts, ", "),
		truncateForPrompt(diffOutput, 4000),
	)
}

// BuildFailurePrompt builds a prompt for diagnosing a build failure.
func BuildFailurePrompt(worktreePath, buildOutput string, changedFiles []string) string {
	return fmt.Sprintf(`You are a supervisor diagnosing a build failure after merge.

## Worktree
%s

## Changed Files
%s

## Build Output
%s

## Instructions
Diagnose the build failure and suggest a fix. Return ONLY a JSON object:

{
  "root_cause": "what caused the build to fail",
  "affected_files": ["file1.go", "file2.go"],
  "suggested_fix": "specific steps to fix the build",
  "can_auto_fix": true/false
}`,
		worktreePath,
		strings.Join(changedFiles, "\n"),
		truncateForPrompt(buildOutput, 4000),
	)
}
