package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// AgentProcess manages a Claude Code subprocess lifecycle.
type AgentProcess struct {
	cmd       *exec.Cmd
	stdinPipe io.WriteCloser
	logFile   *os.File
	logPath   string
	pid       int
	done      chan struct{} // closed when cmd.Wait() returns
	exitCode  int
	mu        sync.Mutex
}

// ProcessStarter is a function type for creating agent processes, allowing test injection.
type ProcessStarter func(ctx context.Context, claudeBin, promptPath, cwd string) (*AgentProcess, error)

// StartAgentProcess starts a Claude Code subprocess.
// It creates the command: claudeBin --dangerously-skip-permissions -p -
// The prompt file content is written to stdin. The pipe is kept open so that
// /exit can be sent later. stdout+stderr are redirected to <cwd>/.claude/agent-output.log.
func StartAgentProcess(ctx context.Context, claudeBin, promptPath, cwd string) (*AgentProcess, error) {
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

	// 3. Build command: <claudeBin> --dangerously-skip-permissions -p -
	cmd := exec.CommandContext(ctx, claudeBin, "--dangerously-skip-permissions", "-p", "-")
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
		cmd:       cmd,
		stdinPipe: stdinPipe,
		logFile:   logFile,
		logPath:   logPath,
		pid:       cmd.Process.Pid,
		done:      make(chan struct{}),
	}

	// 6. Write prompt content to stdin (in background goroutine).
	go func() {
		promptData, err := os.ReadFile(promptPath)
		if err == nil {
			_, _ = stdinPipe.Write(promptData)
			// Don't close the pipe — keep it open for /exit later.
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

// SendExit writes "/exit\n" to the stdin pipe, then closes it.
// This triggers Claude to exit gracefully when it is in idle mode.
func (p *AgentProcess) SendExit() {
	p.mu.Lock()
	pipe := p.stdinPipe
	p.mu.Unlock()
	if pipe != nil {
		// Write /exit command then close the pipe.
		_, _ = io.WriteString(pipe, "/exit\n")
		_ = pipe.Close()
		p.mu.Lock()
		p.stdinPipe = nil
		p.mu.Unlock()
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
