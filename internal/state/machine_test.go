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

