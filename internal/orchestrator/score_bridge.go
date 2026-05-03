package orchestrator

import "github.com/godinj/drem-orchestrator/pkg/score"

// scorePlanGate computes quality scores for a plan at plan_review time.
// Returns a map suitable for storing in task.Context["scores"].
func scorePlanGate(subtasks []planEntry, exceptions []tddException, validation PlanValidationResult) map[string]any {
	return score.ScoresToMap(score.ScorePlan(score.PlanScoreInput{
		Entries:          planScoreEntries(subtasks),
		TDDExceptions:    planScoreExceptions(exceptions),
		ValidationResult: planScoreValidation(validation),
	}))
}

// scoreImplGate computes quality scores for an implementation at testing_ready time.
// constraintsPassed/Failed come from the constraints.Report.
// changedFiles lists files modified by the agent.
// coverageOutput is the raw output from go test -cover.
func scoreImplGate(constraintsPassed, constraintsFailed int, changedFiles []string, coverageOutput string) map[string]any {
	return score.ScoresToMap(score.ScoreImplementation(score.ImplScoreInput{
		ConstraintsPassed: constraintsPassed,
		ConstraintsFailed: constraintsFailed,
		ChangedFiles:      changedFiles,
		CoverageOutput:    coverageOutput,
	}))
}

func planScoreEntries(subtasks []planEntry) []score.PlanEntry {
	entries := make([]score.PlanEntry, len(subtasks))
	for i, subtask := range subtasks {
		entries[i] = score.PlanEntry{
			Title:          subtask.Title,
			AgentType:      subtask.AgentType,
			Phase:          subtask.Phase,
			EstimatedFiles: subtask.EstimatedFiles,
			TestsFor:       subtask.TestsFor,
			Dependencies:   subtask.Dependencies,
			DepthMeta:      subtask.DepthMeta,
		}
	}
	return entries
}

func planScoreExceptions(exceptions []tddException) []score.TDDException {
	scoreExceptions := make([]score.TDDException, len(exceptions))
	for i, exception := range exceptions {
		scoreExceptions[i] = score.TDDException{
			SubtaskIndex: exception.SubtaskIndex,
			Reason:       exception.Reason,
		}
	}
	return scoreExceptions
}

func planScoreValidation(validation PlanValidationResult) score.PlanValidationResult {
	return score.PlanValidationResult{
		Valid:    validation.Valid,
		Warnings: validation.Warnings,
		Errors:   validation.Errors,
	}
}
