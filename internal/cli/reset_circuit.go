package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/godinj/drem-orchestrator/internal/orchestrator"
)

// handleResetCircuit writes the reset-circuit signal file so the running
// orchestrator will close its circuit breaker on the next tick.
func handleResetCircuit(dbPath string, w io.Writer) error {
	if dbPath == "" {
		return fmt.Errorf("reset-circuit requires --config to resolve signal directory")
	}
	signalPath := orchestrator.SignalFilePath(dbPath, orchestrator.SignalResetCircuit)
	if err := os.WriteFile(signalPath, []byte("reset"), 0644); err != nil {
		return fmt.Errorf("write signal file: %w", err)
	}
	fmt.Fprintf(w, "Signal written: %s\nOrchestrator will reset circuit breaker on next tick.\n", signalPath)
	return nil
}
