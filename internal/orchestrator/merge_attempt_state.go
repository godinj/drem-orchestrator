package orchestrator

import "github.com/godinj/drem-orchestrator/internal/model"

// contextKeyMergeAttemptCount is the task.Context key for merge retry tracking.
const contextKeyMergeAttemptCount = "merge_attempt_count"

// MergeAttemptState provides typed access to merge retry tracking fields
// stored in task.Context. It reads and writes the merge_attempt_count field,
// replacing stringly-typed map access with a structured API.
type MergeAttemptState struct {
	attemptCount int
}

// LoadMergeAttemptState reads the current merge attempt state from a task's
// Context map. Returns a zero-valued state if no prior attempts exist.
func LoadMergeAttemptState(task *model.Task) MergeAttemptState {
	if task.Context == nil {
		return MergeAttemptState{}
	}
	raw, ok := task.Context[contextKeyMergeAttemptCount]
	if !ok {
		return MergeAttemptState{}
	}
	switch v := raw.(type) {
	case float64:
		return MergeAttemptState{attemptCount: int(v)}
	case int:
		return MergeAttemptState{attemptCount: v}
	default:
		return MergeAttemptState{}
	}
}

// AttemptCount returns the number of merge attempts made so far.
func (s MergeAttemptState) AttemptCount() int {
	return s.attemptCount
}

// Increment bumps the attempt count by one.
func (s *MergeAttemptState) Increment() {
	s.attemptCount++
}

// Save writes the attempt state back into the task's Context map,
// initializing the map if nil.
func (s MergeAttemptState) Save(task *model.Task) {
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context[contextKeyMergeAttemptCount] = s.attemptCount
}
