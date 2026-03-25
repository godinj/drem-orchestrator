package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
)

// AgentProcess manages a Claude Code subprocess lifecycle.
type AgentProcess struct {
	cmd      *exec.Cmd
	logFile  *os.File
	logPath  string
	pid      int
	done     chan struct{} // closed when cmd.Wait() returns
	exitCode int
	mu       sync.Mutex
}

// ProcessStarter is a function type for creating agent processes, allowing test injection.
// extraArgs carries per-agent model/effort flags (e.g. ["--model", "x", "--effort", "high"]).
type ProcessStarter func(ctx context.Context, claudeBin, promptPath, cwd string, extraArgs []string) (*AgentProcess, error)

// StartAgentProcess starts a Claude Code subprocess.
// extraArgs carries per-agent CLI flags (model, effort) inserted between
// --dangerously-skip-permissions and -p -.
// The prompt file content is written to stdin, then the pipe is closed so that
// Claude receives EOF and begins processing. In -p mode, graceful shutdown is
// handled via SIGTERM rather than stdin commands.
// stdout+stderr are redirected to <cwd>/.claude/agent-output.log.
func StartAgentProcess(ctx context.Context, claudeBin, promptPath, cwd string, extraArgs []string) (*AgentProcess, error) {
	// 1. Ensure .claude directory exists.
	claudeDir := filepath.Join(cwd, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return nil, fmt.Errorf("start agent process: mkdir .claude: %w", err)
	}

	// 2. Create log file at <cwd>/.claude/agent-output.log.
	logPath := filepath.Join(claudeDir, "agent-output.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("start agent process: create log: %w", err)
	}

	// 3. Build command with per-agent CLI flags.
	args := []string{"--dangerously-skip-permissions"}
	args = append(args, extraArgs...)
	args = append(args, "-p", "-")
	cmd := exec.CommandContext(ctx, claudeBin, args...)
	cmd.Dir = cwd
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	// 4. Create stdin pipe.
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		logFile.Close()
		return nil, fmt.Errorf("start agent process: stdin pipe: %w", err)
	}

	// 5. Start process.
	if err := cmd.Start(); err != nil {
		stdinPipe.Close()
		logFile.Close()
		return nil, fmt.Errorf("start agent process: start: %w", err)
	}

	p := &AgentProcess{
		cmd:     cmd,
		logFile: logFile,
		logPath: logPath,
		pid:     cmd.Process.Pid,
		done:    make(chan struct{}),
	}

	// 6. Write prompt content to stdin and close the pipe so Claude receives
	// EOF and begins processing. In -p mode, Claude reads the full prompt
	// from stdin until EOF before starting work.
	go func() {
		defer stdinPipe.Close()
		promptData, err := os.ReadFile(promptPath)
		if err == nil {
			_, _ = stdinPipe.Write(promptData)
		}
	}()

	// 7. Start a goroutine that waits for the process to exit.
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				p.exitCode = exitErr.ExitCode()
			} else {
				p.exitCode = -1
			}
		}
		p.mu.Unlock()
		logFile.Close()
		close(p.done)
	}()

	return p, nil
}

// SendExit sends SIGTERM to the process for graceful shutdown.
// In -p mode stdin is closed after the prompt is written, so we use a signal
// rather than writing /exit to stdin.
func (p *AgentProcess) SendExit() {
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
	}
}

// Kill sends SIGKILL to the process.
func (p *AgentProcess) Kill() {
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// IsAlive checks if the process is still running.
func (p *AgentProcess) IsAlive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// Wait blocks until the process exits and returns the exit code.
func (p *AgentProcess) Wait() (int, error) {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitCode, nil
}

// LogPath returns the path to the output log file.
func (p *AgentProcess) LogPath() string {
	return p.logPath
}

// Pid returns the process ID.
func (p *AgentProcess) Pid() int {
	return p.pid
}
