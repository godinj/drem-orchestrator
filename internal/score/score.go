// Package score calculates quality scores for plan and implementation review gates.
package score

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// StepScore holds quality scores for a single review gate.
// Each dimension is normalized to 0.0–1.0.
type StepScore struct {
	TDD           float64
	Constitution  float64
	Documentation float64
	Depth         float64
}

// ModuleBoundary describes a package-level boundary for depth scoring.
type ModuleBoundary struct {
	Package     string
	Description string
	Exports     int
}

// InterfaceShape describes the public API surface of a module for depth scoring.
type InterfaceShape struct {
	Package   string
	Functions []string
	Types     []string
}

// DepthMeta carries module boundary and interface shape info for depth scoring.
type DepthMeta struct {
	ModuleBoundaries []ModuleBoundary
	InterfaceShapes  []InterfaceShape
}

// PlanEntry represents a single subtask in a plan.
type PlanEntry struct {
	Title          string
	AgentType      string
	Phase          string // "test", "implementation", "integration", "research"
	EstimatedFiles []string
	TestsFor       []int
	Dependencies   []int
	DepthMeta      *DepthMeta // nil for plans without depth metadata
}

// TDDException marks a subtask as exempt from the test-before-impl requirement.
type TDDException struct {
	SubtaskIndex int
	Reason       string
}

// PlanValidationResult captures constitution check output for a plan.
type PlanValidationResult struct {
	Valid    bool
	Warnings []string
	Errors   []string
}

// PlanScoreInput bundles the inputs needed to score a plan.
type PlanScoreInput struct {
	Entries          []PlanEntry
	TDDExceptions    []TDDException
	ValidationResult PlanValidationResult
}

// ImplScoreInput bundles the inputs needed to score an implementation.
// Uses primitive types to avoid coupling to the constraints package.
type ImplScoreInput struct {
	ConstraintsPassed int
	ConstraintsFailed int
	ChangedFiles      []string
	CoverageOutput    string
}

// warningPenalty is the constitution score reduction per plan validation warning.
const warningPenalty = 0.1

// orderingPenalty is the TDD score reduction when a test depends on its impl subtask.
const orderingPenalty = 0.25

// ScorePlan scores a plan at plan_review time.
func ScorePlan(input PlanScoreInput) StepScore {
	return StepScore{
		TDD:           scorePlanTDD(input.Entries, input.TDDExceptions),
		Constitution:  scorePlanConstitution(input.ValidationResult),
		Documentation: scorePlanDocumentation(input.Entries),
		Depth:         scorePlanDepth(input.Entries),
	}
}

func scorePlanTDD(entries []PlanEntry, exceptions []TDDException) float64 {
	exceptionSet := make(map[int]bool, len(exceptions))
	for _, e := range exceptions {
		exceptionSet[e.SubtaskIndex] = true
	}

	// Build set of impl indices covered by a test subtask.
	covered := make(map[int]bool)
	hasOrderingViolation := false
	for _, entry := range entries {
		if entry.Phase != "test" {
			continue
		}
		for _, idx := range entry.TestsFor {
			covered[idx] = true
		}
		// Check ordering: if test depends on an impl subtask it covers.
		for _, dep := range entry.Dependencies {
			if dep >= 0 && dep < len(entries) && entries[dep].Phase == "implementation" {
				hasOrderingViolation = true
			}
		}
	}

	// Count impl subtasks and compute score.
	// Documentation-only subtasks (all estimated files are doc files) are exempt.
	var implCount int
	var totalScore float64
	for i, entry := range entries {
		if entry.Phase != "implementation" {
			continue
		}
		if isDocOnlySubtask(entry) {
			continue
		}
		implCount++
		if covered[i] {
			totalScore += 1.0
		} else if exceptionSet[i] {
			totalScore += 0.5
		}
	}

	if implCount == 0 {
		return 1.0
	}

	tdd := totalScore / float64(implCount)
	if hasOrderingViolation {
		tdd -= orderingPenalty
	}
	if tdd < 0 {
		tdd = 0
	}
	return tdd
}

func scorePlanConstitution(v PlanValidationResult) float64 {
	if len(v.Errors) > 0 {
		return 0.0
	}
	score := 1.0 - warningPenalty*float64(len(v.Warnings))
	if score < 0 {
		score = 0
	}
	return score
}

func scorePlanDocumentation(entries []PlanEntry) float64 {
	for _, entry := range entries {
		for _, f := range entry.EstimatedFiles {
			if isDocFile(f) {
				return 1.0
			}
		}
	}
	return 0.0
}

// scorePlanDepth scores plan depth on three equally-weighted sub-criteria
// when DepthMeta is available:
//  1. Module boundaries defined — at least one subtask has valid ModuleBoundaries.
//  2. Interface shapes specified — at least one subtask has valid InterfaceShapes.
//  3. Deep decomposition — all boundary-defining subtasks keep Exports ≤ 20.
//
// When no subtask has DepthMeta, falls back to the file-coverage ratio
// (fraction of entries with EstimatedFiles). This handles legacy plans that
// predate the DepthMeta requirement.
func scorePlanDepth(entries []PlanEntry) float64 {
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

	hasBoundaries := false
	hasInterfaces := false

	// Track deep decomposition: only evaluated on subtasks that define boundaries.
	boundarySubtaskCount := 0
	deepSubtaskCount := 0

	for _, entry := range entries {
		if entry.DepthMeta == nil {
			continue
		}

		// Check module boundaries.
		for _, b := range entry.DepthMeta.ModuleBoundaries {
			if b.Package != "" && b.Description != "" {
				hasBoundaries = true
				break
			}
		}

		// Check interface shapes.
		for _, s := range entry.DepthMeta.InterfaceShapes {
			if s.Package != "" && (len(s.Functions) > 0 || len(s.Types) > 0) {
				hasInterfaces = true
				break
			}
		}

		// Deep decomposition: for subtasks with valid boundaries, check Exports.
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

	var score float64
	criteria := 0.0

	// Criterion 1: boundaries defined.
	criteria++
	if hasBoundaries {
		score += 1.0
	}

	// Criterion 2: interfaces specified.
	criteria++
	if hasInterfaces {
		score += 1.0
	}

	// Criterion 3: deep decomposition.
	criteria++
	if boundarySubtaskCount > 0 && deepSubtaskCount == boundarySubtaskCount {
		score += 1.0
	}

	return score / criteria
}

// coverageRegex matches "coverage: 85.0% of statements" lines.
var coverageRegex = regexp.MustCompile(`coverage:\s+([\d.]+)%\s+of\s+statements`)

// ScoreImplementation scores an implementation at testing_ready time.
// Depth is not evaluated at implementation time (enforced at plan review).
func ScoreImplementation(input ImplScoreInput) StepScore {
	return StepScore{
		TDD:           scoreImplTDD(input.CoverageOutput),
		Constitution:  scoreImplConstitution(input.ConstraintsPassed, input.ConstraintsFailed),
		Documentation: scoreImplDocumentation(input.ChangedFiles),
		Depth:         0.0,
	}
}

func scoreImplTDD(coverageOutput string) float64 {
	matches := coverageRegex.FindAllStringSubmatch(coverageOutput, -1)
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

func scoreImplConstitution(passed, failed int) float64 {
	total := passed + failed
	if total == 0 {
		return 1.0
	}
	return float64(passed) / float64(total)
}

func scoreImplDocumentation(changedFiles []string) float64 {
	for _, f := range changedFiles {
		if isDocFile(f) {
			return 1.0
		}
	}
	return 0.0
}

// isDocOnlySubtask returns true if all estimated files in the subtask are documentation files.
func isDocOnlySubtask(entry PlanEntry) bool {
	if len(entry.EstimatedFiles) == 0 {
		return false
	}
	for _, f := range entry.EstimatedFiles {
		if !isDocFile(f) {
			return false
		}
	}
	return true
}

// isDocFile returns true if the path looks like documentation (README or docs/ directory).
// Agent prompts (.drem/prompts/) do not count.
func isDocFile(path string) bool {
	lower := strings.ToLower(path)
	if strings.Contains(lower, "readme") {
		return true
	}
	if strings.HasPrefix(lower, "docs/") || strings.Contains(lower, "/docs/") {
		return true
	}
	return false
}

// FormatScores renders scores as human-readable text.
func FormatScores(s StepScore) string {
	return fmt.Sprintf("TDD: %d%% | Constitution: %d%% | Docs: %d%% | Depth: %d%%",
		int(s.TDD*100+0.5),
		int(s.Constitution*100+0.5),
		int(s.Documentation*100+0.5),
		int(s.Depth*100+0.5),
	)
}

// ScoresToMap converts a StepScore to a JSON-serializable map.
func ScoresToMap(s StepScore) map[string]any {
	return map[string]any{
		"tdd":           s.TDD,
		"constitution":  s.Constitution,
		"documentation": s.Documentation,
		"depth":         s.Depth,
		"formatted":     FormatScores(s),
	}
}
