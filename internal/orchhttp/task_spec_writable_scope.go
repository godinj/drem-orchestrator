package orchhttp

import (
	"fmt"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// validateWritableFileSerialization rejects a plan that would send two
// workers to mutate the same path without an explicit dependency path. An
// integration Files list is deliberately not its mutation list when
// writable_files is supplied: it is a read/merge/verify declaration needed by
// immutable seam admission.
func validateWritableFileSerialization(subtasks []orchdto.TaskExecutionSubtaskDTO) error {
	for i := range subtasks {
		filesI := executionWritableFiles(subtasks[i])
		if len(filesI) == 0 {
			continue
		}
		set := make(map[string]struct{}, len(filesI))
		for _, file := range filesI {
			set[file] = struct{}{}
		}
		for j := i + 1; j < len(subtasks); j++ {
			var shared string
			for _, file := range executionWritableFiles(subtasks[j]) {
				if _, ok := set[file]; ok {
					shared = file
					break
				}
			}
			if shared != "" && !executionDependencyPath(subtasks, i, j) && !executionDependencyPath(subtasks, j, i) {
				return fmt.Errorf("subtasks %d and %d both mutate file %q without an explicit dependency path", i, j, shared)
			}
		}
	}
	return nil
}

func executionWritableFiles(subtask orchdto.TaskExecutionSubtaskDTO) []string {
	if subtask.Phase == "integration" && len(subtask.WritableFiles) > 0 {
		return subtask.WritableFiles
	}
	return subtask.Files
}

// executionDependencyPath follows authored dependency edges plus the required
// TDD edge from an implementation to its test. The latter is deterministic
// serialization, while two tests sharing one TU still need an authored path.
func executionDependencyPath(subtasks []orchdto.TaskExecutionSubtaskDTO, from, target int) bool {
	seen := make(map[int]struct{}, len(subtasks))
	var visit func(int) bool
	visit = func(current int) bool {
		if current == target {
			return true
		}
		if current < 0 || current >= len(subtasks) {
			return false
		}
		if _, ok := seen[current]; ok {
			return false
		}
		seen[current] = struct{}{}
		for _, dependency := range subtasks[current].Dependencies {
			if visit(dependency) {
				return true
			}
		}
		for testIndex, subtask := range subtasks {
			if subtask.Phase == "test" && len(subtask.TestsFor) == 1 && subtask.TestsFor[0] == current && visit(testIndex) {
				return true
			}
		}
		return false
	}
	return visit(from)
}
