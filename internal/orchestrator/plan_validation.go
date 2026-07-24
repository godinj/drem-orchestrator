package orchestrator

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/constraints"
)

// maxTDDExceptionRatio is the proportion of implementation subtasks that can be
// exempted from TDD before a plan-level warning is raised.
const maxTDDExceptionRatio = 0.5

const maxImplementationOwnedFiles = 2

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
// The exceptions parameter allows specific subtasks to be exempt from TDD
// requirements; pass nil if no exceptions apply.
func ValidatePlan(subtasks []planEntry, exceptions []tddException) PlanValidationResult {
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
		if !hasDependencyPath(subtasks, overlap.SubtaskA, overlap.SubtaskB) {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Subtasks %d and %d overlap on [%s] but have no dependency — they will be serialized",
					overlap.SubtaskA, overlap.SubtaskB, strings.Join(overlap.Files, ", ")))
		}
	}

	// 4. Same-package test-phase overlap detection.
	warnSamePackageTestOverlap(subtasks, &result)

	// 5. Dependency cycle detection.
	if hasCycle(subtasks) {
		result.Errors = append(result.Errors,
			"Dependency cycle detected in subtask dependencies")
	}

	// 6. writable_files narrows only integration mutation scope. It cannot be
	// used by another phase to bypass that phase's exclusive file ownership.
	validateWritableFileScopes(subtasks, &result)

	// 7. Keep implementation work at one semantic ownership boundary. Legacy
	// plans without depth metadata remain readable, but any declared boundaries
	// must already satisfy the current one-boundary contract.
	validateImplementationGranularity(subtasks, &result)

	// 8. TDD validation (only if at least one subtask has a non-empty Phase).
	if hasTDDPhases(subtasks) {
		validateTDD(subtasks, exceptions, &result)
	} else {
		// Legacy: test subtask ordering check for old-format plans.
		validateLegacyTestOrdering(subtasks, &result)
	}

	// 9. Documentation coverage heuristic.
	if !planTouchesDocFiles(subtasks) {
		result.Warnings = append(result.Warnings,
			"No subtask lists documentation files (README, docs/) — if this feature "+
				"changes user-facing behavior, consider adding a documentation step")
	}

	result.Valid = len(result.Errors) == 0
	return result
}

func validateWritableFileScopes(subtasks []planEntry, result *PlanValidationResult) {
	for index, subtask := range subtasks {
		if len(subtask.WritableFiles) == 0 {
			continue // omitted: legacy fallback to Files
		}
		if subtask.Phase != "integration" {
			result.Errors = append(result.Errors,
				fmt.Sprintf("Subtask %d ('%s') sets writable_files outside integration", index, subtask.Title))
			continue
		}
		declared := make(map[string]struct{}, len(allFiles(subtask)))
		for _, file := range allFiles(subtask) {
			declared[file] = struct{}{}
		}
		seen := make(map[string]struct{}, len(subtask.WritableFiles))
		for _, file := range subtask.WritableFiles {
			if strings.TrimSpace(file) == "" {
				result.Errors = append(result.Errors,
					fmt.Sprintf("Integration subtask %d ('%s') has a blank writable_files entry", index, subtask.Title))
				continue
			}
			if _, ok := declared[file]; !ok {
				result.Errors = append(result.Errors,
					fmt.Sprintf("Integration subtask %d ('%s') writable file %q is not in files", index, subtask.Title, file))
			}
			if _, duplicate := seen[file]; duplicate {
				result.Errors = append(result.Errors,
					fmt.Sprintf("Integration subtask %d ('%s') duplicates writable file %q", index, subtask.Title, file))
			}
			seen[file] = struct{}{}
		}
	}
}

func validateImplementationGranularity(subtasks []planEntry, result *PlanValidationResult) {
	owners := make(map[string]int)
	for index, subtask := range subtasks {
		if subtask.Phase != "implementation" {
			continue
		}
		files := allFiles(subtask)
		if len(files) > maxImplementationOwnedFiles {
			result.Errors = append(result.Errors,
				fmt.Sprintf("Implementation subtask %d ('%s') owns %d files; split it into semantic contracts of at most %d files",
					index, subtask.Title, len(files), maxImplementationOwnedFiles))
		}
		if len(subtask.ModuleBoundaries) > 1 {
			result.Errors = append(result.Errors,
				fmt.Sprintf("Implementation subtask %d ('%s') declares %d module boundaries; split it so each implementation owns one",
					index, subtask.Title, len(subtask.ModuleBoundaries)))
		}
		for _, file := range files {
			if owner, exists := owners[file]; exists {
				result.Errors = append(result.Errors,
					fmt.Sprintf("Implementation subtasks %d and %d both own file %q; use one semantic owner and integration for assembly",
						owner, index, file))
				continue
			}
			owners[file] = index
		}
	}
}

// hasTDDPhases returns true if at least one subtask has a non-empty Phase field.
func hasTDDPhases(subtasks []planEntry) bool {
	for _, s := range subtasks {
		if s.Phase != "" {
			return true
		}
	}
	return false
}

// validateTDD runs all TDD-specific validation rules on the plan.
func validateTDD(subtasks []planEntry, exceptions []tddException, result *PlanValidationResult) {
	n := len(subtasks)

	// Build exception set: subtask indices that are exempt from TDD.
	exceptionSet := make(map[int]bool, len(exceptions))
	for _, ex := range exceptions {
		exceptionSet[ex.SubtaskIndex] = true
	}

	// Validate TDD exceptions (§4.3.4).
	validateTDDExceptions(subtasks, exceptions, result)

	// Collect test and implementation subtasks.
	var testIndices []int
	var implIndices []int
	for i, s := range subtasks {
		switch s.Phase {
		case "test":
			testIndices = append(testIndices, i)
		case "implementation":
			implIndices = append(implIndices, i)
		}
	}

	// Rule: Require at least one test subtask if there are impl subtasks
	// and not all impl subtasks are exempted.
	if len(implIndices) > 0 && len(testIndices) == 0 {
		allExempted := true
		for _, idx := range implIndices {
			if !exceptionSet[idx] {
				allExempted = false
				break
			}
		}
		if !allExempted {
			result.Errors = append(result.Errors,
				"Plan has no test subtasks but has implementation subtasks without TDD exceptions")
		}
	}

	// Build implCoverage: maps impl subtask index -> covering test subtask index.
	implCoverage := make(map[int]int) // impl index -> test index

	// Validate each test subtask's tests_for entries.
	for _, ti := range testIndices {
		testEntry := subtasks[ti]

		// Rule: A test subtask's tests_for must reference exactly one impl subtask (1:1).
		if len(testEntry.TestsFor) != 1 {
			result.Errors = append(result.Errors,
				fmt.Sprintf("Test subtask %d ('%s') must reference exactly one implementation subtask in tests_for, got %d",
					ti, testEntry.Title, len(testEntry.TestsFor)))
			continue
		}

		implIdx := testEntry.TestsFor[0]

		// Rule: tests_for must not reference out-of-bounds index.
		if implIdx < 0 || implIdx >= n {
			result.Errors = append(result.Errors,
				fmt.Sprintf("Test subtask %d ('%s') tests_for references out-of-bounds subtask index %d",
					ti, testEntry.Title, implIdx))
			continue
		}

		// Rule: tests_for must reference an implementation-phase subtask.
		if subtasks[implIdx].Phase != "implementation" {
			result.Errors = append(result.Errors,
				fmt.Sprintf("Test subtask %d ('%s') tests_for references non-implementation subtask %d (phase: %q)",
					ti, testEntry.Title, implIdx, subtasks[implIdx].Phase))
			continue
		}

		// Rule: No duplicate test coverage — two test subtasks must not cover the same impl.
		if existingTest, exists := implCoverage[implIdx]; exists {
			result.Errors = append(result.Errors,
				fmt.Sprintf("Duplicate test coverage: test subtasks %d and %d both cover implementation subtask %d",
					existingTest, ti, implIdx))
			continue
		}

		implCoverage[implIdx] = ti

		// Warning: File overlap check between test and impl subtask.
		testFiles := allFiles(testEntry)
		implFiles := allFiles(subtasks[implIdx])
		if len(testFiles) > 0 && len(implFiles) > 0 {
			if !hasFileOverlap(testFiles, implFiles) {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("Test subtask %d and implementation subtask %d have no file overlap — verify test placement",
						ti, implIdx))
			}
		}
	}

	// Rule: Every implementation subtask must be covered by a test or have an exception.
	for _, implIdx := range implIndices {
		if _, covered := implCoverage[implIdx]; !covered && !exceptionSet[implIdx] {
			result.Errors = append(result.Errors,
				fmt.Sprintf("Implementation subtask %d ('%s') has no corresponding test subtask and no TDD exception",
					implIdx, subtasks[implIdx].Title))
		}
	}

	// Rule: Validate phase ordering — test must not depend on implementation (§4.3.3).
	validatePhaseOrdering(subtasks, result)
}

// validateTDDExceptions validates the TDD exception entries themselves (§4.3.4).
func validateTDDExceptions(subtasks []planEntry, exceptions []tddException, result *PlanValidationResult) {
	n := len(subtasks)

	// Count implementation subtasks for the 50% threshold.
	implCount := 0
	for _, s := range subtasks {
		if s.Phase == "implementation" {
			implCount++
		}
	}

	exemptedImplCount := 0
	for _, ex := range exceptions {
		// Rule: Exception must not reference out-of-bounds index.
		if ex.SubtaskIndex < 0 || ex.SubtaskIndex >= n {
			result.Errors = append(result.Errors,
				fmt.Sprintf("TDD exception references out-of-bounds subtask index %d", ex.SubtaskIndex))
			continue
		}

		// Rule: Exception must not reference a test-phase subtask.
		if subtasks[ex.SubtaskIndex].Phase == "test" {
			result.Errors = append(result.Errors,
				fmt.Sprintf("TDD exception references test-phase subtask %d ('%s')",
					ex.SubtaskIndex, subtasks[ex.SubtaskIndex].Title))
			continue
		}

		if subtasks[ex.SubtaskIndex].Phase == "implementation" {
			exemptedImplCount++
		}
	}

	// Warning: More than 50% of impl subtasks exempted.
	if implCount > 0 && exemptedImplCount > 0 {
		ratio := float64(exemptedImplCount) / float64(implCount)
		if ratio > maxTDDExceptionRatio {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("More than 50%% of implementation subtasks (%d/%d) are exempted from TDD",
					exemptedImplCount, implCount))
		}
	}
}

// validatePhaseOrdering checks that test-phase subtasks do not depend on
// implementation-phase subtasks, enforcing TDD ordering (§4.3.3).
func validatePhaseOrdering(subtasks []planEntry, result *PlanValidationResult) {
	for i, s := range subtasks {
		if s.Phase != "test" {
			continue
		}
		for _, dep := range s.Dependencies {
			if dep < 0 || dep >= len(subtasks) {
				continue
			}
			if subtasks[dep].Phase == "implementation" {
				result.Errors = append(result.Errors,
					fmt.Sprintf("Test subtask %d ('%s') depends on implementation subtask %d ('%s') — tests must run before implementation",
						i, s.Title, dep, subtasks[dep].Title))
			}
		}
	}
}

// validateLegacyTestOrdering performs the old-style check for plans without
// TDD phases: warns if test subtasks don't depend on all implementation subtasks.
func validateLegacyTestOrdering(subtasks []planEntry, result *PlanValidationResult) {
	for i, s := range subtasks {
		if isTestSubtask(s) {
			missing := findMissingTestDependencies(subtasks, i)
			if len(missing) > 0 {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("Test subtask '%s' does not depend on all implementation subtasks", s.Title))
			}
		}
	}
}

// hasFileOverlap returns true if there is at least one common file between
// the two file lists.
func hasFileOverlap(filesA, filesB []string) bool {
	set := make(map[string]bool, len(filesA))
	for _, f := range filesA {
		set[f] = true
	}
	for _, f := range filesB {
		if set[f] {
			return true
		}
	}
	return false
}

// MergeTDDDependencies takes the parsed plan entries and returns a new slice
// where each implementation subtask's Dependencies includes its corresponding
// test subtask (auto-generated from tests_for). Explicit dependencies are
// preserved; auto-generated ones are added only if not already present.
func MergeTDDDependencies(subtasks []planEntry) []planEntry {
	// Make a deep copy of the subtask slice.
	result := make([]planEntry, len(subtasks))
	for i, s := range subtasks {
		result[i] = s
		// Deep copy slices to avoid mutating the original.
		if len(s.Dependencies) > 0 {
			result[i].Dependencies = make([]int, len(s.Dependencies))
			copy(result[i].Dependencies, s.Dependencies)
		}
		if len(s.Files) > 0 {
			result[i].Files = make([]string, len(s.Files))
			copy(result[i].Files, s.Files)
		}
		if len(s.EstimatedFiles) > 0 {
			result[i].EstimatedFiles = make([]string, len(s.EstimatedFiles))
			copy(result[i].EstimatedFiles, s.EstimatedFiles)
		}
		if len(s.TestsFor) > 0 {
			result[i].TestsFor = make([]int, len(s.TestsFor))
			copy(result[i].TestsFor, s.TestsFor)
		}
	}

	// For each test-phase subtask with TestsFor, add the test subtask as a
	// dependency of the implementation subtask it covers.
	for testIdx, s := range result {
		if s.Phase != "test" || len(s.TestsFor) != 1 {
			continue
		}
		implIdx := s.TestsFor[0]
		if implIdx < 0 || implIdx >= len(result) {
			continue
		}
		// Add testIdx to the impl subtask's dependencies if not already present.
		if !containsInt(result[implIdx].Dependencies, testIdx) {
			result[implIdx].Dependencies = append(result[implIdx].Dependencies, testIdx)
		}
	}

	// Phase ordering: integration-phase subtasks depend on all
	// implementation-phase subtasks so they run after implementation
	// completes.
	var implIndices []int
	for i, s := range result {
		if s.Phase == "implementation" {
			implIndices = append(implIndices, i)
		}
	}
	if len(implIndices) > 0 {
		for i, s := range result {
			if s.Phase != "integration" {
				continue
			}
			for _, implIdx := range implIndices {
				if !containsInt(result[i].Dependencies, implIdx) {
					result[i].Dependencies = append(result[i].Dependencies, implIdx)
				}
			}
		}
	}

	return result
}

// containsInt returns true if the slice contains the given value.
func containsInt(slice []int, val int) bool {
	for _, v := range slice {
		if v == val {
			return true
		}
	}
	return false
}

// computeFileOverlaps finds pairs of subtasks that share files.
func computeFileOverlaps(subtasks []planEntry) []fileOverlap {
	var overlaps []fileOverlap
	for i := 0; i < len(subtasks); i++ {
		filesI := writablePlanFiles(subtasks[i])
		if len(filesI) == 0 {
			continue
		}
		setI := make(map[string]bool, len(filesI))
		for _, f := range filesI {
			setI[f] = true
		}

		for j := i + 1; j < len(subtasks); j++ {
			filesJ := writablePlanFiles(subtasks[j])
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

// writablePlanFiles returns the worker mutation scope. Files remains the
// complete read/merge/verify declaration, while integration may opt into a
// smaller writable_files list. Parsed legacy plans deliberately fall back to
// Files so existing behavior is unchanged.
func writablePlanFiles(entry planEntry) []string {
	if len(entry.WritableFiles) > 0 {
		return entry.WritableFiles
	}
	return allFiles(entry)
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

// hasDependencyPath reports whether either subtask is explicitly sequenced
// after the other, including through intermediate subtasks. A direct edge is
// insufficient for a plan such as test A -> shared fixture -> test B.
func hasDependencyPath(subtasks []planEntry, a, b int) bool {
	return dependsOnPath(subtasks, a, b) || dependsOnPath(subtasks, b, a)
}

func dependsOnPath(subtasks []planEntry, from, target int) bool {
	seen := make(map[int]struct{}, len(subtasks))
	var visit func(int) bool
	visit = func(current int) bool {
		if current < 0 || current >= len(subtasks) || current == target {
			return current == target
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
		return false
	}
	return visit(from)
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

// isTestSubtask checks if a subtask is a test subtask. It first checks the
// Phase field — when Phase is set, it is authoritative and no fallback is used.
// Otherwise, it checks the explicit IsTest field, then falls back to checking
// if the title contains test-related words ("test", "tests", "testing") as
// whole words — avoiding false matches on unrelated words like "latest", "contest".
func isTestSubtask(entry planEntry) bool {
	if entry.Phase == "test" {
		return true
	}
	// If Phase is explicitly set to something other than "test", it takes precedence.
	if entry.Phase != "" {
		return false
	}
	if entry.IsTest {
		return true
	}
	lower := strings.ToLower(entry.Title)
	// Check for test-related keywords as whole words.
	for _, keyword := range []string{"test", "tests", "testing"} {
		if lower == keyword {
			return true
		}
		// keyword at start: "test integration", "testing suite"
		if strings.HasPrefix(lower, keyword+" ") || strings.HasPrefix(lower, keyword+":") {
			return true
		}
		// keyword at end: "add tests", "unit testing"
		if strings.HasSuffix(lower, " "+keyword) {
			return true
		}
		// keyword in middle: "add tests for feature", "unit testing suite"
		if strings.Contains(lower, " "+keyword+" ") || strings.Contains(lower, " "+keyword+":") {
			return true
		}
	}
	return false
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

// planTouchesDocFiles returns true if any subtask lists a documentation file
// (README*, docs/*, *.md in the project root) in its file list.
func planTouchesDocFiles(subtasks []planEntry) bool {
	for _, s := range subtasks {
		for _, f := range allFiles(s) {
			lower := strings.ToLower(f)
			if strings.HasPrefix(lower, "readme") ||
				strings.HasPrefix(lower, "docs/") ||
				strings.HasPrefix(lower, "doc/") {
				return true
			}
		}
	}
	return false
}

// ValidatePlanConstraints checks a plan's estimated_files against the
// constraint config. Returns warnings for files that target constrained
// or grandfathered paths. Returns an empty result if cfg is nil.
func ValidatePlanConstraints(subtasks []planEntry, cfg *constraints.Config, worktreeRoot string) PlanValidationResult {
	var result PlanValidationResult

	if cfg == nil {
		result.Valid = true
		return result
	}

	for i, sub := range subtasks {
		files := allFiles(sub)
		if len(files) == 0 {
			continue
		}

		fileSet := make(map[string]bool, len(files))
		for _, f := range files {
			fileSet[f] = true
		}

		// Check max_lines exceptions (shrink-only grandfathered files).
		for _, mlc := range cfg.MaxLines {
			for _, exc := range mlc.Exceptions {
				if exc.Rule != "shrink-only" {
					continue
				}
				if fileSet[exc.Path] {
					result.Warnings = append(result.Warnings,
						fmt.Sprintf("Subtask %d (%q) targets grandfathered file %s (shrink-only, baseline: %d lines) — changes must not increase line count",
							i, sub.Title, exc.Path, exc.BaselineLines))
				}
			}

			// Check files near limit (>90% of ceiling) for constraints
			// without a matching exception.
			if worktreeRoot == "" {
				continue
			}
			for _, f := range files {
				if hasLinesException(mlc.Exceptions, f) {
					continue
				}
				if !matchesGlob(mlc.Glob, f) {
					continue
				}
				absPath := filepath.Join(worktreeRoot, f)
				count, err := countFileLines(absPath)
				if err != nil {
					continue // file doesn't exist or unreadable, skip
				}
				if mlc.Limit > 0 && count > mlc.Limit*9/10 {
					result.Warnings = append(result.Warnings,
						fmt.Sprintf("Subtask %d (%q) targets file %s which is already at %d/%d lines (90%%+ of ceiling)",
							i, sub.Title, f, count, mlc.Limit))
				}
			}
		}

		// Check max_matches exceptions (shrink-only grandfathered files).
		for _, mmc := range cfg.MaxMatches {
			for _, exc := range mmc.Exceptions {
				if exc.Rule != "shrink-only" {
					continue
				}
				if fileSet[exc.Path] {
					result.Warnings = append(result.Warnings,
						fmt.Sprintf("Subtask %d (%q) targets grandfathered file %s (shrink-only, baseline: %d matches for %q) — changes must not increase count",
							i, sub.Title, exc.Path, exc.BaselineCount, mmc.Name))
				}
			}
		}
	}

	result.Valid = len(result.Errors) == 0
	return result
}
