# Agent: tmux Manager

You are working on the `feature/go-rewrite` branch of Drem Orchestrator, a multi-agent task orchestration system being rewritten from Python to Go.
Your task is to implement the tmux session and window management layer that will host Claude Code agent terminals.

## Context

Read these before starting:
- `docs/go-rewrite/DESIGN.md` (focus on "tmux Integration" section — session layout, manager API, agent spawning)
- `CLAUDE.md` (build commands, conventions)

## Deliverables

### New file: `internal/tmux/tmux.go`

A Go wrapper around the `tmux` CLI. All methods exec `tmux` as a subprocess.

```go
package tmux

import (
    "fmt"
    "os/exec"
    "strings"
)

// Manager manages a tmux session for the orchestrator.
type Manager struct {
    SessionName string // e.g., "drem-myproject"
}

// NewManager creates a Manager for the given session name.
func NewManager(sessionName string) *Manager
```

Implement these methods:

#### `EnsureSession() error`

Creates the tmux session if it doesn't exist. If it already exists, do nothing (no error).

```bash
# Check if session exists
tmux has-session -t <session> 2>/dev/null
# If not, create detached session
tmux new-session -d -s <session> -n dashboard
```

The first window should be named "dashboard" — this is where the TUI will run.

#### `CreateWindow(name, cmd, cwd string) error`

Creates a new tmux window in the session and runs a command in it.

```bash
tmux new-window -t <session> -n <name> -c <cwd> <cmd>
```

Return error if window creation fails. The `cmd` string is passed as-is to tmux (it runs in a shell).

#### `CloseWindow(name string) error`

Kills a tmux window by name.

```bash
tmux kill-window -t <session>:<name>
```

Ignore "window not found" errors (idempotent close).

#### `ListWindows() ([]WindowInfo, error)`

Lists all windows in the session.

```bash
tmux list-windows -t <session> -F "#{window_index}:#{window_name}:#{window_active}"
```

Parse the output into:

```go
type WindowInfo struct {
    Index  int
    Name   string
    Active bool
}
```

#### `FocusWindow(name string) error`

Switches the tmux client to a specific window.

```bash
tmux select-window -t <session>:<name>
```

#### `CapturePane(name string, lines int) (string, error)`

Captures the visible content of a window's pane. Used by the TUI to preview agent output.

```bash
tmux capture-pane -t <session>:<name> -p -S -<lines>
```

The `-S -<lines>` flag captures the last N lines of scrollback. `-p` prints to stdout.

#### `IsWindowAlive(name string) (bool, error)`

Checks if the process in a window is still running.

```bash
tmux list-panes -t <session>:<name> -F "#{pane_dead}"
```

Returns `true` if `pane_dead` is `0`, `false` if `1`. Return `false, nil` if the window doesn't exist.

#### `WaitForExit(name string) (int, error)`

Blocks until the command in a window's pane exits and returns its exit code. This is used by the agent monitor goroutine.

**Implementation approach**: Poll `pane_dead` and `pane_dead_status` every 500ms:

```bash
tmux list-panes -t <session>:<name> -F "#{pane_dead}:#{pane_dead_status}"
```

When `pane_dead` becomes `1`, parse and return `pane_dead_status` as the exit code.

Important: Set `remain-on-exit on` for agent windows so the pane stays around after the command exits (otherwise the window auto-closes and we can't read the exit code). Do this in `CreateWindow`:

```bash
tmux set-option -t <session>:<name> remain-on-exit on
```

After reading the exit code in `WaitForExit`, the caller is responsible for calling `CloseWindow` to clean up.

#### `KillSession() error`

Destroys the entire tmux session (used on shutdown).

```bash
tmux kill-session -t <session>
```

### Helper: `runTmux`

Internal helper to exec tmux commands:

```go
func runTmux(args ...string) (string, error) {
    cmd := exec.Command("tmux", args...)
    out, err := cmd.CombinedOutput()
    if err != nil {
        return "", fmt.Errorf("tmux %s: %w\n%s", strings.Join(args, " "), err, string(out))
    }
    return strings.TrimSpace(string(out)), nil
}
```

### New file: `internal/tmux/tmux_test.go`

Write tests that verify tmux integration. Tests should:
1. Skip with `t.Skip("requires tmux")` if tmux is not in PATH
2. Create a test session with a unique name
3. Test: EnsureSession, CreateWindow (with `sleep 1` command), ListWindows, CapturePane, IsWindowAlive, WaitForExit, CloseWindow, KillSession
4. Clean up the test session in `t.Cleanup`

## Conventions

- Go 1.22+, standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Build verification: `go build ./... && go vet ./...`
