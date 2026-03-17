package orchestrator

import (
	"fmt"
	"os/exec"
	"strings"
)

// stopWords is the set of common English words filtered out by extractKeywords.
var stopWords = map[string]bool{
	"the": true, "for": true, "and": true, "a": true, "to": true,
	"in": true, "of": true, "with": true, "is": true, "on": true,
	"at": true, "by": true, "from": true, "an": true, "or": true,
	"as": true, "be": true, "it": true, "that": true, "this": true,
	"are": true, "was": true, "were": true, "has": true, "have": true,
	"had": true, "do": true, "does": true, "did": true, "will": true,
	"would": true, "could": true, "should": true, "may": true, "might": true,
	"can": true, "shall": true, "must": true, "not": true, "no": true,
	"but": true,
}

// HasExistingWork determines whether a subtask's deliverables already exist on
// the integration branch by checking two conditions (both must be true):
//  1. At least one estimated file appears in the set of already-changed files
//  2. At least one commit message has >= 2 keyword overlap with the subtask title
func HasExistingWork(estimatedFiles []string, changedFiles []string, commitMessages []string, subtaskTitle string) bool {
	return hasExistingWork(estimatedFiles, changedFiles, commitMessages, subtaskTitle)
}

func hasExistingWork(estimatedFiles []string, changedFiles []string, commitMessages []string, subtaskTitle string) bool {
	if len(estimatedFiles) == 0 || len(changedFiles) == 0 {
		return false
	}

	// Condition 1: at least one estimated file appears in changedFiles.
	changedSet := make(map[string]bool, len(changedFiles))
	for _, f := range changedFiles {
		changedSet[f] = true
	}
	fileOverlap := false
	for _, f := range estimatedFiles {
		if changedSet[f] {
			fileOverlap = true
			break
		}
	}
	if !fileOverlap {
		return false
	}

	// Condition 2: at least one commit message has >= 2 keyword overlap with title.
	titleKeywords := extractKeywords(subtaskTitle)
	if len(titleKeywords) < 2 {
		// If the title itself has fewer than 2 keywords, check for exact match.
		for _, msg := range commitMessages {
			msgKeywords := extractKeywords(msg)
			if keywordOverlap(titleKeywords, msgKeywords) >= len(titleKeywords) && len(titleKeywords) > 0 {
				return true
			}
		}
		return false
	}
	for _, msg := range commitMessages {
		msgKeywords := extractKeywords(msg)
		if keywordOverlap(titleKeywords, msgKeywords) >= 2 {
			return true
		}
	}
	return false
}

// extractKeywords splits a title into lowercase words, filtering out stop
// words and words shorter than 3 characters. Returns a deduplicated sorted slice.
func extractKeywords(title string) []string {
	words := strings.Fields(strings.ToLower(title))
	seen := make(map[string]bool, len(words))
	var result []string
	for _, w := range words {
		if len(w) < 3 || stopWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		result = append(result, w)
	}
	if result == nil {
		return []string{}
	}
	return result
}

// keywordOverlap counts how many keywords appear in both slices.
// Both slices are assumed lowercase.
func keywordOverlap(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]bool, len(a))
	for _, kw := range a {
		set[kw] = true
	}
	count := 0
	for _, kw := range b {
		if set[kw] {
			count++
		}
	}
	return count
}

// getEstimatedFiles extracts the estimated_files list from a task context map.
// Returns nil if not present or not a []string-compatible value.
func getEstimatedFiles(ctx map[string]any) []string {
	if ctx == nil {
		return nil
	}
	raw, ok := ctx["estimated_files"]
	if !ok {
		return nil
	}
	// Context is deserialized from JSON, so the value will be []any.
	switch v := raw.(type) {
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		if len(result) == 0 {
			return nil
		}
		return result
	case []string:
		if len(v) == 0 {
			return nil
		}
		return v
	default:
		return nil
	}
}

// runGitInDir runs a git command in the given directory and returns trimmed stdout.
func runGitInDir(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// getChangedFiles returns the list of files changed between baseRef and HEAD
// in the given worktree.
func getChangedFiles(worktreePath, baseRef string) ([]string, error) {
	output, err := runGitInDir(worktreePath, "diff", "--name-only", baseRef+"..HEAD")
	if err != nil {
		return nil, fmt.Errorf("get changed files: %w", err)
	}
	if output == "" {
		return nil, nil
	}
	return strings.Split(output, "\n"), nil
}

// getCommitMessages runs `git log --format=%s baseRef..HEAD` in the worktree
// and returns commit subject lines.
func getCommitMessages(worktreePath, baseRef string) ([]string, error) {
	output, err := runGitInDir(worktreePath, "log", "--format=%s", baseRef+"..HEAD")
	if err != nil {
		return nil, fmt.Errorf("get commit messages: %w", err)
	}
	if output == "" {
		return nil, nil
	}
	return strings.Split(output, "\n"), nil
}
