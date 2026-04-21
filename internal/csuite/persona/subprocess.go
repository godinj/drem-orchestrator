package persona

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
)

// stderrCaptureLimit caps how many bytes of child stderr we surface in
// error messages. 4 KiB is enough to hold a claude CLI usage blurb or a
// stack trace head without letting a runaway diagnostic spew unbounded
// bytes into the returned error string (which ends up in structured
// logs).
const stderrCaptureLimit = 4 * 1024

// NewClaudeSpawner returns a Spawner that launches `claude -p` via
// os/exec.CommandContext. The returned spawner:
//
//   - Runs the command in a new process group (Setpgid) so SIGKILL-on-
//     context-cancellation terminates any grandchildren claude may have
//     spawned (e.g. its internal tool shells).
//   - Captures stdout into a buffer; stdout is the persona's reply and
//     goes to the outbox.
//   - Captures stderr into a separate buffer so diagnostic output from
//     claude can be folded into the returned error when the process
//     exits non-zero or fails to launch. Prior to this change stderr was
//     silently discarded (cmd.Stderr=nil routes to /dev/null in os/exec),
//     which masked bugs like "claude -p parses ---frontmatter--- as CLI
//     flags".
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
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		trimmed := truncateStderr(stderr.String(), stderrCaptureLimit)
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Preserve the existing Spawner contract: ExitError means
			// "claude ran and chose to exit non-zero", so err is nil and
			// exitCode carries the signal. Fold the captured stderr into
			// the stdout bytes so the poller's recordFailure reason
			// string surfaces the real diagnostic (e.g. "unknown option
			// '---\nfrom: alex...'") in logs + state.md.
			return appendDiagnostic(stdout.Bytes(), trimmed), exitErr.ExitCode(), nil
		}
		return stdout.Bytes(), -1, fmt.Errorf("claude -p launch failed: %w: stderr=%q", err, trimmed)
	}
	return stdout.Bytes(), 0, nil
}

// appendDiagnostic folds a trimmed stderr blob into the stdout bytes so
// the poller's recordFailure path (which derives its reason from stdout)
// logs the real cause of a non-zero exit. Called only on ExitError —
// stdout here is not going to the outbox.
func appendDiagnostic(stdout []byte, stderr string) []byte {
	if stderr == "" {
		return stdout
	}
	if len(stdout) == 0 {
		return []byte("stderr: " + stderr)
	}
	return []byte(string(stdout) + "\nstderr: " + stderr)
}

// truncateStderr strips leading/trailing whitespace and caps the result
// at limit bytes. A "...(truncated)" sentinel is appended when the cap
// is hit so operators can tell at a glance that there was more they
// didn't see.
func truncateStderr(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "...(truncated)"
}
