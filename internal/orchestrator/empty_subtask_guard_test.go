package orchestrator

import (
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestSubtaskRecoveryPolicy_Evaluate(t *testing.T) {
	tests := []struct {
		name           string
		maxEmptyChecks int // 0 means use default
		subtaskCount   int
		context        model.JSONField // pre-existing task context
		wantAction     RecoveryAction
		wantCounter    float64 // counter value after Evaluate; -1 means expect 0 (reset)
	}{
		{
			name:         "empty subtasks first check returns replan and sets counter to 1",
			subtaskCount: 0,
			context:      nil,
			wantAction:   RecoveryReplan,
			wantCounter:  1,
		},
		{
			name:         "empty subtasks second check increments counter and returns replan",
			subtaskCount: 0,
			context:      model.JSONField{emptySubtaskCounterKey: float64(1)},
			wantAction:   RecoveryReplan,
			wantCounter:  2,
		},
		{
			name:         "empty subtasks at max checks returns fail",
			subtaskCount: 0,
			context:      model.JSONField{emptySubtaskCounterKey: float64(defaultMaxEmptyChecks - 1)},
			wantAction:   RecoveryFail,
			wantCounter:  float64(defaultMaxEmptyChecks),
		},
		{
			name:         "subtasks exist resets counter and returns continue",
			subtaskCount: 3,
			context:      model.JSONField{emptySubtaskCounterKey: float64(2)},
			wantAction:   RecoveryContinue,
			wantCounter:  0,
		},
		{
			name:         "subtasks exist with clean context returns continue",
			subtaskCount: 1,
			context:      nil,
			wantAction:   RecoveryContinue,
			wantCounter:  0,
		},
		{
			name:           "custom max empty checks of 1 fails immediately",
			maxEmptyChecks: 1,
			subtaskCount:   0,
			context:        nil,
			wantAction:     RecoveryFail,
			wantCounter:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &model.Task{
				ID:      uuid.New(),
				Context: tt.context,
			}

			policy := &SubtaskRecoveryPolicy{
				MaxEmptyChecks: tt.maxEmptyChecks,
			}

			got := policy.Evaluate(task, tt.subtaskCount)

			if got != tt.wantAction {
				t.Errorf("Evaluate() action = %d, want %d", got, tt.wantAction)
			}

			// Verify counter in task context after evaluation.
			if task.Context == nil {
				t.Fatal("Evaluate() did not initialize task.Context")
			}
			counterRaw, ok := task.Context[emptySubtaskCounterKey]
			if !ok {
				t.Fatal("Evaluate() did not set counter in task.Context")
			}
			counter, ok := counterRaw.(float64)
			if !ok {
				t.Fatalf("counter is %T, want float64", counterRaw)
			}
			if counter != tt.wantCounter {
				t.Errorf("counter = %v, want %v", counter, tt.wantCounter)
			}
		})
	}
}
