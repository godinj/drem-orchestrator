package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFakeClaudeScript writes a shell script to dir that mimics a Claude binary.
// The script accepts any arguments, reads stdin to /dev/null, and writes the given
// output string to stdout.
func writeFakeClaudeScript(t *testing.T, dir, output string) string {
	t.Helper()
	scriptPath := filepath.Join(dir, "fake-claude")
	script := "#!/bin/sh\ncat > /dev/null\necho " + shellescape(output) + "\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude script: %v", err)
	}
	return scriptPath
}

// shellescape wraps s in single quotes for shell safety.
func shellescape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func TestStartAgentProcess(t *testing.T) {
	cwd := t.TempDir()
	scriptDir := t.TempDir()

	// Create a fake claude binary that reads stdin (gets EOF) and writes output.
	claudeBin := writeFakeClaudeScript(t, scriptDir, "Agent output")

	// Write a prompt file.
	promptPath := filepath.Join(cwd, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("Test prompt"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	ctx := context.Background()
	p, err := StartAgentProcess(ctx, claudeBin, promptPath, cwd, nil)
	if err != nil {
		t.Fatalf("StartAgentProcess: %v", err)
	}

	// LogPath should point to the correct location.
	expectedLogPath := filepath.Join(cwd, ".claude", "agent-output.log")
	if p.LogPath() != expectedLogPath {
		t.Errorf("LogPath() = %q, want %q", p.LogPath(), expectedLogPath)
	}

	// Pid should be > 0.
	if p.Pid() <= 0 {
		t.Errorf("Pid() = %d, want > 0", p.Pid())
	}

	// Stdin is closed after prompt write, so the script receives EOF from
	// cat and exits on its own.
	exitCode, err := p.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	// IsAlive should be false after exit.
	if p.IsAlive() {
		t.Error("IsAlive() = true after Wait, want false")
	}
}

func TestAgentProcess_SendExit(t *testing.T) {
	cwd := t.TempDir()
	scriptDir := t.TempDir()

	// Create a long-running script that exits on SIGTERM.
	// Uses a background sleep + wait so the trap fires immediately.
	scriptPath := filepath.Join(scriptDir, "fake-claude")
	script := `#!/bin/sh
trap 'exit 0' TERM
cat > /dev/null
while true; do sleep 1 & wait; done
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	// Write a prompt file.
	promptPath := filepath.Join(cwd, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("prompt content\n"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	ctx := context.Background()
	p, err := StartAgentProcess(ctx, scriptPath, promptPath, cwd, nil)
	if err != nil {
		t.Fatalf("StartAgentProcess: %v", err)
	}

	// Give the process time to start and read stdin EOF.
	time.Sleep(200 * time.Millisecond)

	if !p.IsAlive() {
		t.Fatal("process should be alive before SendExit")
	}

	// SendExit sends SIGTERM for graceful shutdown.
	p.SendExit()

	// Wait with a timeout.
	done := make(chan struct{})
	go func() {
		p.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Good — process exited.
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit within 5 seconds after SendExit")
	}

	if p.IsAlive() {
		t.Error("IsAlive() = true after Wait, want false")
	}
}

func TestAgentProcess_Kill(t *testing.T) {
	cwd := t.TempDir()
	scriptDir := t.TempDir()

	// Create a long-running script.
	scriptPath := filepath.Join(scriptDir, "fake-claude")
	script := "#!/bin/sh\nsleep 300\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	// Write a prompt file.
	promptPath := filepath.Join(cwd, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("prompt"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	ctx := context.Background()
	p, err := StartAgentProcess(ctx, scriptPath, promptPath, cwd, nil)
	if err != nil {
		t.Fatalf("StartAgentProcess: %v", err)
	}

	// Give process time to start.
	time.Sleep(100 * time.Millisecond)

	if !p.IsAlive() {
		t.Fatal("process should be alive before Kill")
	}

	p.Kill()

	// Wait with a timeout.
	done := make(chan struct{})
	go func() {
		p.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Good — process was killed.
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit within 5 seconds after Kill")
	}

	if p.IsAlive() {
		t.Error("IsAlive() = true after Kill, want false")
	}
}

func TestAgentProcess_LogOutput(t *testing.T) {
	cwd := t.TempDir()
	scriptDir := t.TempDir()

	// Create a script that writes to both stdout and stderr.
	// Stdin is closed by StartAgentProcess after the prompt is written,
	// so cat receives EOF immediately and the script proceeds.
	scriptPath := filepath.Join(scriptDir, "fake-claude")
	script := `#!/bin/sh
cat > /dev/null
echo "stdout line"
echo "stderr line" >&2
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	promptPath := filepath.Join(cwd, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("prompt"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	ctx := context.Background()
	p, err := StartAgentProcess(ctx, scriptPath, promptPath, cwd, nil)
	if err != nil {
		t.Fatalf("StartAgentProcess: %v", err)
	}

	// Wait for exit — stdin is closed so the script runs to completion.
	p.Wait()

	// Read the log file.
	logData, err := os.ReadFile(p.LogPath())
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}

	logContent := string(logData)
	if !strings.Contains(logContent, "stdout line") {
		t.Errorf("log file should contain 'stdout line', got: %q", logContent)
	}
	if !strings.Contains(logContent, "stderr line") {
		t.Errorf("log file should contain 'stderr line', got: %q", logContent)
	}
}

func TestProcessStarter_Type(t *testing.T) {
	// Verify that ProcessStarter func type works and can be assigned a mock.
	var starter ProcessStarter = func(ctx context.Context, claudeBin, promptPath, cwd string, extraArgs []string) (*AgentProcess, error) {
		return &AgentProcess{
			logPath: "/mock/path/agent-output.log",
			pid:     12345,
			done:    make(chan struct{}),
		}, nil
	}

	ctx := context.Background()
	p, err := starter(ctx, "/usr/bin/claude", "/tmp/prompt.md", "/tmp/cwd", nil)
	if err != nil {
		t.Fatalf("mock ProcessStarter returned error: %v", err)
	}
	if p.LogPath() != "/mock/path/agent-output.log" {
		t.Errorf("LogPath() = %q, want %q", p.LogPath(), "/mock/path/agent-output.log")
	}
	if p.Pid() != 12345 {
		t.Errorf("Pid() = %d, want 12345", p.Pid())
	}

	// Verify that StartAgentProcess itself satisfies the ProcessStarter type.
	var _ ProcessStarter = StartAgentProcess
}
