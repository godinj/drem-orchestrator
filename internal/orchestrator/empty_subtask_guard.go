package orchestrator

import "github.com/godinj/drem-orchestrator/internal/model"

// RecoveryAction represents the action a recovery policy recommends.
type RecoveryAction int

const (
	// RecoveryContinue indicates subtasks exist and processing should proceed.
	RecoveryContinue RecoveryAction = iota
	// RecoveryReplan indicates empty subtasks were detected and the task
	// should return to planning with a directive to generate tests.
	RecoveryReplan
	// RecoveryFail indicates the maximum number of empty-subtask checks
	// has been exhausted and the task should fail.
	RecoveryFail
)

// emptySubtaskCounterKey is the context key used to track how many
// consecutive times the policy has observed an empty subtask set.
const emptySubtaskCounterKey = "empty_subtask_checks"

// defaultMaxEmptyChecks is the number of replan attempts before failing.
// Set to 5 to give the system enough cycles to recover through a full
// replan: TEST_WRITING → PLANNING → new planner → PLAN_REVIEW → TEST_WRITING.
const defaultMaxEmptyChecks = 5

// SubtaskRecoveryPolicy evaluates whether a task's subtask set is healthy
// and selects a recovery action when subtasks are missing.
type SubtaskRecoveryPolicy struct {
	// MaxEmptyChecks is the number of replan cycles allowed before the
	// policy gives up and returns RecoveryFail. Zero means use the default.
	MaxEmptyChecks int
}

// Evaluate inspects subtaskCount (the number of relevant subtasks for the
// current phase) and the task's context to decide the recovery action.
// It updates task.Context with the current counter state.
func (p *SubtaskRecoveryPolicy) Evaluate(task *model.Task, subtaskCount int) RecoveryAction {
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}

	maxChecks := p.MaxEmptyChecks
	if maxChecks == 0 {
		maxChecks = defaultMaxEmptyChecks
	}

	if subtaskCount > 0 {
		task.Context[emptySubtaskCounterKey] = float64(0)
		return RecoveryContinue
	}

	counter, _ := task.Context[emptySubtaskCounterKey].(float64)
	counter++
	task.Context[emptySubtaskCounterKey] = counter

	if int(counter) >= maxChecks {
		return RecoveryFail
	}
	return RecoveryReplan
}
