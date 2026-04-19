package orchestrator

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/constraints"
)

// hasLinesException returns true if the given path has a lines exception.
func hasLinesException(exceptions []constraints.LinesException, path string) bool {
	for _, exc := range exceptions {
		if exc.Path == path {
			return true
		}
	}
	return false
}

// matchesGlob checks if a file path matches a glob pattern. It uses
// filepath.Match on the full path and, for patterns without directory
// separators, also on just the base name.
func matchesGlob(pattern, path string) bool {
	matched, _ := filepath.Match(pattern, path)
	if matched {
		return true
	}
	if !strings.Contains(pattern, "/") {
		matched, _ = filepath.Match(pattern, filepath.Base(path))
		return matched
	}
	return false
}

// goPackages extracts unique Go package directory paths from a file list.
// For each .go file, the package is the directory portion of the path.
func goPackages(files []string) []string {
	seen := make(map[string]bool)
	var pkgs []string
	for _, f := range files {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		dir := filepath.Dir(f)
		if dir == "." {
			dir = ""
		}
		if dir != "" && !seen[dir] {
			seen[dir] = true
			pkgs = append(pkgs, dir)
		}
	}
	return pkgs
}

// warnSamePackageTestOverlap checks pairs of test-phase subtasks that share
// no dependency. If both target files in the same Go package, they likely need
// shared stubs and should either list those stubs in files or be sequenced.
func warnSamePackageTestOverlap(subtasks []planEntry, result *PlanValidationResult) {
	var testIndices []int
	for i, s := range subtasks {
		if s.Phase == "test" {
			testIndices = append(testIndices, i)
		}
	}

	for a := 0; a < len(testIndices); a++ {
		idxA := testIndices[a]
		pkgsA := goPackages(allFiles(subtasks[idxA]))
		if len(pkgsA) == 0 {
			continue
		}
		pkgSetA := make(map[string]bool, len(pkgsA))
		for _, p := range pkgsA {
			pkgSetA[p] = true
		}

		for b := a + 1; b < len(testIndices); b++ {
			idxB := testIndices[b]

			if hasDependency(subtasks, idxA, idxB) {
				continue
			}

			for _, pkg := range goPackages(allFiles(subtasks[idxB])) {
				if pkgSetA[pkg] {
					result.Warnings = append(result.Warnings,
						fmt.Sprintf("Test subtasks %d and %d both target same Go package %q — "+
							"they likely need shared stubs. List stub files in files or add a dependency.",
							idxA, idxB, pkg))
					break
				}
			}
		}
	}
}

// ValidatePlanFileExistence checks that estimated_files reference paths that
// actually exist in the worktree. New files (test files being created) get a
// weaker check: only the parent directory must exist. Existing source files
// must match real paths — a hallucinated file like "experiment_scheduling.go"
// when the real file is "scheduler_experiments.go" wastes agent iterations.
//
// Returns errors for source files that don't exist (blocks plan), warnings
// for test files whose parent directory doesn't exist.
func ValidatePlanFileExistence(subtasks []planEntry, worktreeRoot string) PlanValidationResult {
	var result PlanValidationResult

	if worktreeRoot == "" {
		result.Valid = true
		return result
	}

	for i, sub := range subtasks {
		files := allFiles(sub)
		if len(files) == 0 {
			continue
		}

		var missing []string
		for _, f := range files {
			absPath := filepath.Join(worktreeRoot, f)

			if isNewFileCandidate(f, sub) {
				dir := filepath.Dir(absPath)
				if _, err := os.Stat(dir); os.IsNotExist(err) {
					result.Warnings = append(result.Warnings,
						fmt.Sprintf("Subtask %d (%q): parent directory for new file %s does not exist",
							i, sub.Title, f))
				}
			} else {
				if _, err := os.Stat(absPath); os.IsNotExist(err) {
					missing = append(missing, f)
				}
			}
		}

		if len(missing) > 0 {
			result.Errors = append(result.Errors,
				fmt.Sprintf("Subtask %d (%q): estimated_files reference non-existent paths: [%s] — planner may have hallucinated filenames",
					i, sub.Title, strings.Join(missing, ", ")))
		}
	}

	result.Valid = len(result.Errors) == 0
	return result
}

// isNewFileCandidate returns true if the file is likely being created rather
// than modified. Test files in test-phase subtasks are always new. Files
// ending in _test.go are usually new. Files in subtasks with titles that
// suggest creation ("add", "create", "implement", "write") are likely new.
func isNewFileCandidate(path string, sub planEntry) bool {
	if sub.Phase == "test" {
		return true
	}
	if strings.HasSuffix(path, "_test.go") {
		return true
	}
	lower := strings.ToLower(sub.Title)
	for _, verb := range []string{"add ", "create ", "implement ", "write ", "scaffold "} {
		if strings.HasPrefix(lower, verb) {
			return true
		}
	}
	return false
}

// countFileLines counts the number of newline characters in a file.
func countFileLines(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, nil
	}
	count := bytes.Count(data, []byte{'\n'})
	if data[len(data)-1] != '\n' {
		count++
	}
	return count, nil
}
