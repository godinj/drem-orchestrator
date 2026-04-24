package watcher

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const defaultSubprocessTimeout = 99 * time.Minute

// RunClaudeSubprocess executes a claude subprocess for the named agent and
// returns the stdout bytes, exit code, and any error. The subprocess runs
// with a 3-minute timeout and is killed (via process-group SIGKILL) if the
// timeout or context deadline is exceeded.
//
// systemPrompt is passed verbatim as the --system-prompt flag value. Callers
// are responsible for loading prompt content from the appropriate source
// (e.g. an agent prompt file).
//
// This function is used by the production CommandRunner in cmd/csuite-watcher
// and is intentionally exported so the binary can use it without duplicating
// subprocess logic.
func RunClaudeSubprocess(ctx context.Context, agent string, workDir string, systemPrompt string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultSubprocessTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude",
		"--dangerously-skip-permissions",
		"-p", "Process your turn",
		"--system-prompt", systemPrompt,
		"--output-format", "json",
	)
	if workDir != "" {
		cmd.Dir = workDir
		// Set CSUITE_PROTO_SH so agent prompts can source the protocol
		// library via absolute path regardless of claude's working directory.
		protoPath := filepath.Join(workDir, "scripts", "csuite-proto.sh")
		cmd.Env = append(os.Environ(), "CSUITE_PROTO_SH="+protoPath)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return stdout.Bytes(), exitErr.ExitCode(), nil
		}
		return stdout.Bytes(), -1, err
	}
	return stdout.Bytes(), 0, nil
}
