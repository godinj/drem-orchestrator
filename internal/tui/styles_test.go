package tui

import (
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ---------------------------------------------------------------------------
// StatusBadge tests
// ---------------------------------------------------------------------------

func TestStatusBadge_AllStatuses(t *testing.T) {
	statuses := []model.TaskStatus{
		model.StatusBacklog,
		model.StatusPlanning,
		model.StatusPlanReview,
		model.StatusInProgress,
		model.StatusTestingReady,
		model.StatusMerging,
		model.StatusPaused,
		model.StatusDone,
		model.StatusFailed,
	}

	for _, s := range statuses {
		t.Run(string(s), func(t *testing.T) {
			result := StatusBadge(s)
			if result == "" {
				t.Errorf("StatusBadge(%q) returned empty string", s)
			}
			if !strings.Contains(result, string(s)) {
				t.Errorf("StatusBadge(%q) = %q, should contain status text", s, result)
			}
		})
	}
}

func TestStatusBadge_UnknownStatus(t *testing.T) {
	result := StatusBadge("nonexistent_status")
	if result == "" {
		t.Error("StatusBadge for unknown status should still return something")
	}
	// Should contain "?" icon for unknown status
	if !strings.Contains(result, "?") {
		t.Errorf("StatusBadge for unknown status = %q, should contain '?'", result)
	}
}

// ---------------------------------------------------------------------------
// AgentStatusBadge tests
// ---------------------------------------------------------------------------

func TestAgentStatusBadge_AllStatuses(t *testing.T) {
	statuses := []model.AgentStatus{
		model.AgentIdle,
		model.AgentWorking,
		model.AgentBlocked,
		model.AgentDead,
	}

	for _, s := range statuses {
		t.Run(string(s), func(t *testing.T) {
			result := AgentStatusBadge(s)
			if result == "" {
				t.Errorf("AgentStatusBadge(%q) returned empty string", s)
			}
			if !strings.Contains(result, string(s)) {
				t.Errorf("AgentStatusBadge(%q) = %q, should contain status text", s, result)
			}
		})
	}
}

func TestAgentStatusBadge_UnknownStatus(t *testing.T) {
	result := AgentStatusBadge("unknown_agent_status")
	if result == "" {
		t.Error("AgentStatusBadge for unknown status should still return something")
	}
}
