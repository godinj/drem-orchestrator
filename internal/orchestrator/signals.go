// signals.go handles file-based IPC signals from the CLI to the running
// orchestrator. The CLI writes a signal file next to the database; the
// orchestrator checks for it once per tick, acts on it, and deletes it.
package orchestrator

import (
	"os"
	"path/filepath"
)

// SignalResetCircuit is the filename the CLI writes to request a circuit
// breaker reset. The file contents are ignored — presence is the signal.
const SignalResetCircuit = ".drem-signal-reset-circuit"

// SignalDir returns the directory where signal files are placed (same
// directory as the database file).
func SignalDir(dbPath string) string {
	return filepath.Dir(dbPath)
}

// SignalFilePath returns the full path for a named signal file.
func SignalFilePath(dbPath, signal string) string {
	return filepath.Join(SignalDir(dbPath), signal)
}

// checkSignalFiles scans for operator signal files and acts on them.
// Called once per tick at the top of doTick.
func (o *Orchestrator) checkSignalFiles() {
	// Reset circuit breaker signal.
	resetPath := SignalFilePath(o.dbPath, SignalResetCircuit)
	if _, err := os.Stat(resetPath); err == nil {
		if o.endpointHealth != nil {
			o.endpointHealth.ResetCircuit()
		}
		os.Remove(resetPath)
		o.logger.Info("signal: processed reset-circuit", "path", resetPath)
	}
}
