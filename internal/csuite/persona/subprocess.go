package persona

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"syscall"
)

// NewClaudeSpawner returns a Spawner that launches `claude -p` via
// os/exec.CommandContext. The returned spawner:
//
//   - Runs the command in a new process group (Setpgid) so SIGKILL-on-
//     context-cancellation terminates any grandchildren claude may have
//     spawned (e.g. its internal tool shells).
//   - Captures stdout into a buffer; stderr is forwarded to os.Stderr so
//     claude diagnostic output appears in `docker logs`.
//   - Reports the exit code via the int return so the caller can
//     distinguish "claude ran, returned non-zero" from "claude could not
//     be launched" (which surfaces as a non-nil error).
//
// Subscription-only auth: this function never sets any token env var.
// The claude CLI picks up credentials from the bind-mounted
// /home/drem/.claude/.credentials.json file.
func NewClaudeSpawner() Spawner {
	return SpawnerFunc(spawnClaude)
}

func spawnClaude(ctx context.Context, args []string, stdin io.Reader) ([]byte, int, error) {
	if len(args) == 0 {
		return nil, -1, fmt.Errorf("persona: empty spawner argv")
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Kill the whole process group so any claude sub-tools die
		// alongside the parent.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// stderr forwarding: don't capture it, let it ride through docker
	// logs so operators can see claude's own diagnostics.
	cmd.Stderr = nil // inherits from parent via docker log driver

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return stdout.Bytes(), exitErr.ExitCode(), nil
		}
		return stdout.Bytes(), -1, err
	}
	return stdout.Bytes(), 0, nil
}
