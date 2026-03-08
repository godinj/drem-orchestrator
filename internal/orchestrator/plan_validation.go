package orchestrator

import (
	"fmt"
	"strings"
)

// PlanValidationResult contains the outcome of validating a plan.
type PlanValidationResult struct {
	Valid    bool     `json:"valid"`
	Warnings []string `json:"warnings,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

// fileOverlap records an overlap between two subtasks on shared files.
type fileOverlap struct {
	SubtaskA int
	SubtaskB int
	Files    []string
}

// ValidatePlan checks a parsed plan for structural issues.
// Returns warnings (surfaced at plan_review) and errors (block transition).
func ValidatePlan(subtasks []planEntry) PlanValidationResult {
	var result PlanValidationResult

	// 1. Subtask count bounds.
	if len(subtasks) > 8 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("Plan has %d subtasks (recommended max: 8)", len(subtasks)))
	}

	// 2. File lists present.
	emptyFiles := 0
	for _, s := range subtasks {
		if len(s.Files) == 0 && len(s.EstimatedFiles) == 0 {
			emptyFiles++
		}
	}
	if emptyFiles > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("%d subtask(s) have no files listed — scheduling will be degraded", emptyFiles))
	}

	// 3. File overlap detection.
	overlaps := computeFileOverlaps(subtasks)
	for _, overlap := range overlaps {
		if !hasDependency(subtasks, overlap.SubtaskA, overlap.SubtaskB) {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Subtasks %d and %d overlap on [%s] but have no dependency — they will be serialized",
					overlap.SubtaskA, overlap.SubtaskB, strings.Join(overlap.Files, ", ")))
		}
	}

	// 4. Dependency cycle detection.
	if hasCycle(subtasks) {
		result.Errors = append(result.Errors,
			"Dependency cycle detected in subtask dependencies")
	}

	// 5. Test subtask ordering.
	for i, s := range subtasks {
		if isTestSubtask(s) {
			missing := findMissingTestDependencies(subtasks, i)
			if len(missing) > 0 {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("Test subtask '%s' does not depend on all implementation subtasks", s.Title))
			}
		}
	}

	result.Valid = len(result.Errors) == 0
	return result
}

// computeFileOverlaps finds pairs of subtasks that share files.
func computeFileOverlaps(subtasks []planEntry) []fileOverlap {
	var overlaps []fileOverlap
	for i := 0; i < len(subtasks); i++ {
		filesI := allFiles(subtasks[i])
		if len(filesI) == 0 {
			continue
		}
		setI := make(map[string]bool, len(filesI))
		for _, f := range filesI {
			setI[f] = true
		}

		for j := i + 1; j < len(subtasks); j++ {
			filesJ := allFiles(subtasks[j])
			var shared []string
			for _, f := range filesJ {
				if setI[f] {
					shared = append(shared, f)
				}
			}
			if len(shared) > 0 {
				overlaps = append(overlaps, fileOverlap{
					SubtaskA: i,
					SubtaskB: j,
					Files:    shared,
				})
			}
		}
	}
	return overlaps
}

// allFiles returns the combined file list for a subtask, preferring Files
// and falling back to EstimatedFiles.
func allFiles(entry planEntry) []string {
	if len(entry.Files) > 0 {
		return entry.Files
	}
	return entry.EstimatedFiles
}

// hasDependency checks whether subtask a depends on b or b depends on a.
func hasDependency(subtasks []planEntry, a, b int) bool {
	for _, dep := range subtasks[a].Dependencies {
		if dep == b {
			return true
		}
	}
	for _, dep := range subtasks[b].Dependencies {
		if dep == a {
			return true
		}
	}
	return false
}

// hasCycle detects cycles in the dependency graph using iterative DFS.
func hasCycle(subtasks []planEntry) bool {
	n := len(subtasks)
	// 0 = unvisited, 1 = in progress, 2 = done
	state := make([]int, n)

	for i := 0; i < n; i++ {
		if state[i] != 0 {
			continue
		}
		// Iterative DFS using an explicit stack.
		type frame struct {
			node int
			idx  int // index into Dependencies we're processing
		}
		stack := []frame{{node: i, idx: 0}}
		state[i] = 1

		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			deps := subtasks[top.node].Dependencies

			if top.idx >= len(deps) {
				// Done with this node.
				state[top.node] = 2
				stack = stack[:len(stack)-1]
				continue
			}

			dep := deps[top.idx]
			top.idx++

			if dep < 0 || dep >= n {
				continue // out-of-range dependency, skip
			}

			switch state[dep] {
			case 1:
				return true // back edge — cycle found
			case 0:
				state[dep] = 1
				stack = append(stack, frame{node: dep, idx: 0})
			}
			// state[dep] == 2 means already fully explored, skip
		}
	}

	return false
}

// isTestSubtask checks if a subtask is a test subtask by looking for "test"
// in the title (case-insensitive).
func isTestSubtask(entry planEntry) bool {
	return strings.Contains(strings.ToLower(entry.Title), "test")
}

// findMissingTestDependencies returns the indices of non-test subtasks that
// the test subtask at index testIdx does not depend on.
func findMissingTestDependencies(subtasks []planEntry, testIdx int) []int {
	depSet := make(map[int]bool, len(subtasks[testIdx].Dependencies))
	for _, d := range subtasks[testIdx].Dependencies {
		depSet[d] = true
	}

	var missing []int
	for i, s := range subtasks {
		if i == testIdx {
			continue
		}
		if isTestSubtask(s) {
			continue
		}
		if !depSet[i] {
			missing = append(missing, i)
		}
	}
	return missing
}
