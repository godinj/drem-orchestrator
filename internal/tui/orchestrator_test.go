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

// TestHTTPOrchestratorSatisfiesTUIOrchestrator_Parallel mirrors the check
// above for the Phase-3 HTTP adapter. Both implementations must stay in
// lockstep with the interface: one for in-process callers (reconciler,
// scheduler, tests) and one for the TUI's gate-mutation path. See
// plans/orch-api-gate-mutations.md "Phase 3".
//
// The primary test for the HTTP adapter lives in http_orchestrator_test.go;
// this assertion is duplicated here so a quick grep for "Satisfies" finds
// every TUIOrchestrator implementation in one file.
func TestHTTPOrchestratorSatisfiesTUIOrchestrator_Parallel(t *testing.T) {
	var _ tui.TUIOrchestrator = (*tui.HTTPOrchestrator)(nil)
}
