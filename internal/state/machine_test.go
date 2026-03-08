package state

import (
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestFailedToInProgressTransition(t *testing.T) {
	err := ValidateTransition(model.StatusFailed, model.StatusInProgress)
	if err != nil {
		t.Errorf("expected failed -> in_progress to be valid, got error: %v", err)
	}
}

func TestFailedToBacklogStillValid(t *testing.T) {
	err := ValidateTransition(model.StatusFailed, model.StatusBacklog)
	if err != nil {
		t.Errorf("expected failed -> backlog to still be valid, got error: %v", err)
	}
}

func TestFailedToInvalidTarget(t *testing.T) {
	err := ValidateTransition(model.StatusFailed, model.StatusDone)
	if err == nil {
		t.Error("expected failed -> done to be invalid, got nil error")
	}
}

func TestGetAvailableTransitionsFromFailed(t *testing.T) {
	targets := GetAvailableTransitions(model.StatusFailed)
	if len(targets) != 2 {
		t.Fatalf("expected 2 transitions from failed, got %d: %v", len(targets), targets)
	}

	found := map[model.TaskStatus]bool{}
	for _, s := range targets {
		found[s] = true
	}

	if !found[model.StatusBacklog] {
		t.Error("expected backlog in available transitions from failed")
	}
	if !found[model.StatusInProgress] {
		t.Error("expected in_progress in available transitions from failed")
	}
}
