package orchestrator

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Score computation for plan and implementation review gates.
//
// These functions inline the scoring math rather than importing internal/score
// to stay within the orchestrator package's internal import ceiling (baseline 31).
// The score package defines the canonical algorithm; the test file
// (score_wiring_test.go) verifies that both implementations agree.

// scorePlanGate computes quality scores for a plan at plan_review time.
// Returns a map suitable for storing in task.Context["scores"].
func scorePlanGate(subtasks []planEntry, exceptions []tddException, validation PlanValidationResult) map[string]any {
	tdd := planTDDScore(subtasks, exceptions)
	constitution := planConstitutionScore(validation)
	doc := planDocScore(subtasks)
	depth := planDepthScore(subtasks)

	return map[string]any{
		"tdd":           tdd,
		"constitution":  constitution,
		"documentation": doc,
		"depth":         depth,
		"formatted":     formatScoreLine(tdd, constitution, doc, depth),
	}
}

// scoreImplGate computes quality scores for an implementation at testing_ready time.
// constraintsPassed/Failed come from the constraints.Report.
// changedFiles lists files modified by the agent.
// coverageOutput is the raw output from go test -cover.
func scoreImplGate(constraintsPassed, constraintsFailed int, changedFiles []string, coverageOutput string) map[string]any {
	tdd := implTDDScore(coverageOutput)
	constitution := implConstitutionScore(constraintsPassed, constraintsFailed)
	doc := implDocScore(changedFiles)
	// Implementation gate does not have plan-level depth info; default to 1.0.
	depth := 1.0

	return map[string]any{
		"tdd":           tdd,
		"constitution":  constitution,
		"documentation": doc,
		"depth":         depth,
		"formatted":     formatScoreLine(tdd, constitution, doc, depth),
	}
}

// planTDDScore returns the fraction of implementation subtasks covered by tests.
const scoreWarningPenalty = 0.1

func planTDDScore(entries []planEntry, exceptions []tddException) float64 {
	exceptionSet := make(map[int]bool, len(exceptions))
	for _, e := range exceptions {
		exceptionSet[e.SubtaskIndex] = true
	}

	covered := make(map[int]bool)
	for _, entry := range entries {
		if entry.Phase != "test" {
			continue
		}
		for _, idx := range entry.TestsFor {
			covered[idx] = true
		}
	}

	var implCount int
	var total float64
	for i, entry := range entries {
		if entry.Phase != "implementation" {
			continue
		}
		if isDocOnlyEntry(entry) {
			continue
		}
		implCount++
		if covered[i] {
			total += 1.0
		} else if exceptionSet[i] {
			total += 0.5
		}
	}

	if implCount == 0 {
		return 1.0
	}
	tdd := total / float64(implCount)
	if tdd < 0 {
		tdd = 0
	}
	return tdd
}

func planConstitutionScore(v PlanValidationResult) float64 {
	if len(v.Errors) > 0 {
		return 0.0
	}
	s := 1.0 - scoreWarningPenalty*float64(len(v.Warnings))
	if s < 0 {
		s = 0
	}
	return s
}

func planDocScore(entries []planEntry) float64 {
	for _, entry := range entries {
		for _, f := range entry.EstimatedFiles {
			if isDocPath(f) {
				return 1.0
			}
		}
	}
	return 0.0
}

var scoreCoverageRe = regexp.MustCompile(`coverage:\s+([\d.]+)%\s+of\s+statements`)

func implTDDScore(coverageOutput string) float64 {
	matches := scoreCoverageRe.FindAllStringSubmatch(coverageOutput, -1)
	if len(matches) == 0 {
		return 0.0
	}
	var total float64
	for _, m := range matches {
		pct, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		total += pct / 100.0
	}
	return total / float64(len(matches))
}

func implConstitutionScore(passed, failed int) float64 {
	t := passed + failed
	if t == 0 {
		return 1.0
	}
	return float64(passed) / float64(t)
}

func implDocScore(changedFiles []string) float64 {
	for _, f := range changedFiles {
		if isDocPath(f) {
			return 1.0
		}
	}
	return 0.0
}

func isDocOnlyEntry(entry planEntry) bool {
	if len(entry.EstimatedFiles) == 0 {
		return false
	}
	for _, f := range entry.EstimatedFiles {
		if !isDocPath(f) {
			return false
		}
	}
	return true
}

func isDocPath(path string) bool {
	lower := strings.ToLower(path)
	if strings.Contains(lower, "readme") {
		return true
	}
	if strings.HasPrefix(lower, "docs/") || strings.Contains(lower, "/docs/") {
		return true
	}
	return false
}

func formatScoreLine(tdd, constitution, doc, depth float64) string {
	return fmt.Sprintf("TDD: %d%% | Constitution: %d%% | Docs: %d%% | Depth: %d%%",
		int(tdd*100+0.5),
		int(constitution*100+0.5),
		int(doc*100+0.5),
		int(depth*100+0.5),
	)
}

// moduleBoundary describes a package boundary in a plan subtask's depth metadata.
// Local mirror of score.ModuleBoundary to avoid importing internal/score.
type moduleBoundary struct {
	Package     string `json:"package"`
	Description string `json:"description"`
	Exports     int    `json:"exports"`
}

// interfaceShape describes an interface contract in a plan subtask's depth metadata.
// Local mirror of score.InterfaceShape to avoid importing internal/score.
type interfaceShape struct {
	Package   string   `json:"package"`
	Functions []string `json:"functions"`
	Types     []string `json:"types"`
}

// depthMeta holds depth-related metadata for a plan subtask.
// Local mirror of score.DepthMeta to avoid importing internal/score.
type depthMeta struct {
	ModuleBoundaries []moduleBoundary `json:"module_boundaries"`
	InterfaceShapes  []interfaceShape `json:"interface_shapes"`
}

// planDepthScore scores plan depth on three equally-weighted sub-criteria
// when DepthMeta is available:
//  1. Module boundaries defined — at least one subtask has valid ModuleBoundaries.
//  2. Interface shapes specified — at least one subtask has valid InterfaceShapes.
//  3. Deep decomposition — all boundary-defining subtasks keep Exports in (0, 20].
//
// When no subtask has DepthMeta, falls back to the file-coverage ratio
// (fraction of entries with EstimatedFiles). This handles legacy plans that
// predate the DepthMeta requirement.
//
// Mirrors score.scorePlanDepth.
func planDepthScore(entries []planEntry) float64 {
	if len(entries) == 0 {
		return 1.0
	}

	// Check if any entry has DepthMeta.
	hasAnyDepthMeta := false
	for _, entry := range entries {
		if entry.DepthMeta != nil {
			hasAnyDepthMeta = true
			break
		}
	}

	// Fallback: file-coverage ratio when no DepthMeta is present.
	if !hasAnyDepthMeta {
		var withFiles int
		for _, entry := range entries {
			if len(entry.EstimatedFiles) > 0 {
				withFiles++
			}
		}
		return float64(withFiles) / float64(len(entries))
	}

	// Three-criterion scoring with DepthMeta.
	hasBoundaries := false
	hasInterfaces := false

	boundarySubtaskCount := 0
	deepSubtaskCount := 0

	for _, entry := range entries {
		if entry.DepthMeta == nil {
			continue
		}

		for _, b := range entry.DepthMeta.ModuleBoundaries {
			if b.Package != "" && b.Description != "" {
				hasBoundaries = true
				break
			}
		}

		for _, s := range entry.DepthMeta.InterfaceShapes {
			if s.Package != "" && (len(s.Functions) > 0 || len(s.Types) > 0) {
				hasInterfaces = true
				break
			}
		}

		hasValidBoundary := false
		allDeep := true
		for _, b := range entry.DepthMeta.ModuleBoundaries {
			if b.Package != "" && b.Description != "" {
				hasValidBoundary = true
				if b.Exports <= 0 || b.Exports > 20 {
					allDeep = false
				}
			}
		}
		if hasValidBoundary {
			boundarySubtaskCount++
			if allDeep {
				deepSubtaskCount++
			}
		}
	}

	var s float64
	if hasBoundaries {
		s += 1.0
	}
	if hasInterfaces {
		s += 1.0
	}
	if boundarySubtaskCount > 0 && deepSubtaskCount == boundarySubtaskCount {
		s += 1.0
	}
	return s / 3.0
}
