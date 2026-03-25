package tui_test

import (
	"testing"

	"github.com/godinj/drem-orchestrator/internal/orchestrator"
	"github.com/godinj/drem-orchestrator/internal/tui"
)

// TestOrchestratorSatisfiesTUIOrchestrator is a compile-time assertion that
// *orchestrator.Orchestrator implements tui.TUIOrchestrator. If the interface
// drifts from the concrete type, this test fails at build time.
func TestOrchestratorSatisfiesTUIOrchestrator(t *testing.T) {
	var _ tui.TUIOrchestrator = (*orchestrator.Orchestrator)(nil)
}
