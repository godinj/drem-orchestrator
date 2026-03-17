// Package agentmon parses Claude JSONL transcripts for agent activity metrics.
// It incrementally reads transcript files and accumulates tool call history
// to provide real-time insight into what an agent is doing.
package agentmon

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godinj/drem-orchestrator/internal/ctxmon"
)

// maxToolCalls is the maximum number of tool calls kept in memory.
const maxToolCalls = 500

// ToolCall represents a single tool invocation extracted from the transcript.
type ToolCall struct {
	Timestamp time.Time
	Tool      string // "Bash", "Read", "Edit", "Write", "Grep", "Glob", etc.
	Target    string // file path or command description
}

// Activity summarises the agent's current activity based on accumulated
// tool calls from the transcript.
type Activity struct {
	LastTool       string
	LastTarget     string
	LastToolTime   time.Time
	DwellFile      string
	DwellDuration  time.Duration
	FilesTouched   map[string]bool // distinct files from Read/Edit/Write/Grep/Glob
	FilesModified  map[string]bool // from Edit/Write only
	EstimatedFiles []string        // from task context
	FileProgress   string          // "3/5 files" or "3 files"
	ToolCounts     map[string]int
	HasCommit      bool   // "git commit" detected in Bash
	Phase          string // "exploring", "implementing", "testing", "fixing"
	StuckScore     int    // 0=ok, 1=warning, 2=stuck
	StuckHint      string
}

// Monitor tracks agent activity by incrementally reading a Claude JSONL
// transcript file.
type Monitor struct {
	worktreePath   string
	estimatedFiles []string
	byteOffset     int64
	toolCalls      []ToolCall
	transcriptPath string
	mu             sync.Mutex
}

// NewMonitor creates a new Monitor for the given worktree path. The
// estimatedFiles parameter lists files the agent is expected to modify,
// used for computing file progress.
func NewMonitor(worktreePath string, estimatedFiles []string) *Monitor {
	return &Monitor{
		worktreePath:   worktreePath,
		estimatedFiles: estimatedFiles,
	}
}

// Scan reads new lines from the transcript and returns an Activity summary.
// Returns nil, nil if no transcript file is found yet.
func (m *Monitor) Scan() (*Activity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Resolve transcript path lazily on first scan.
	if m.transcriptPath == "" {
		path, err := findTranscript(m.worktreePath)
		if err != nil {
			return nil, fmt.Errorf("agentmon scan: %w", err)
		}
		if path == "" {
			return nil, nil
		}
		m.transcriptPath = path
	}

	if err := m.readNewLines(); err != nil {
		return nil, fmt.Errorf("agentmon scan: %w", err)
	}

	if len(m.toolCalls) == 0 {
		return nil, nil
	}

	return buildActivity(m.toolCalls, m.estimatedFiles), nil
}

// findTranscript locates the most recently modified .jsonl file in the
// Claude project directory for the given worktree path.
func findTranscript(worktreePath string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find transcript: %w", err)
	}

	projectDir := filepath.Join(homeDir, ".claude", "projects", ctxmon.ProjectDirName(worktreePath))
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("find transcript: %w", err)
	}

	var latestPath string
	var latestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(latestMod) {
			latestMod = info.ModTime()
			latestPath = filepath.Join(projectDir, e.Name())
		}
	}

	return latestPath, nil
}

// readNewLines reads lines from the transcript starting at byteOffset and
// parses any new tool calls.
func (m *Monitor) readNewLines() error {
	f, err := os.Open(m.transcriptPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read transcript: %w", err)
	}
	defer f.Close()

	if m.byteOffset > 0 {
		if _, err := f.Seek(m.byteOffset, 0); err != nil {
			return fmt.Errorf("seek transcript: %w", err)
		}
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB line buffer

	for scanner.Scan() {
		line := scanner.Bytes()
		m.byteOffset += int64(len(line)) + 1 // +1 for newline

		tc := parseTranscriptLine(line)
		if tc != nil {
			m.toolCalls = append(m.toolCalls, *tc)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan transcript: %w", err)
	}

	// Trim to last maxToolCalls entries.
	if len(m.toolCalls) > maxToolCalls {
		m.toolCalls = m.toolCalls[len(m.toolCalls)-maxToolCalls:]
	}

	return nil
}

// buildActivity constructs an Activity summary from accumulated tool calls.
func buildActivity(calls []ToolCall, estimatedFiles []string) *Activity {
	a := &Activity{
		FilesTouched:   make(map[string]bool),
		FilesModified:  make(map[string]bool),
		EstimatedFiles: estimatedFiles,
		ToolCounts:     make(map[string]int),
	}

	for _, tc := range calls {
		a.ToolCounts[tc.Tool]++

		// Track files touched by file-oriented tools.
		switch tc.Tool {
		case "Read", "Edit", "Write", "Grep", "Glob":
			if tc.Target != "" && isFilePath(tc.Target) {
				a.FilesTouched[tc.Target] = true
			}
		}

		// Track files modified.
		if tc.Tool == "Edit" || tc.Tool == "Write" {
			if tc.Target != "" && isFilePath(tc.Target) {
				a.FilesModified[tc.Target] = true
			}
		}

		// Detect git commit in Bash commands.
		if tc.Tool == "Bash" && strings.Contains(tc.Target, "git commit") {
			a.HasCommit = true
		}
	}

	// Set last tool info.
	last := calls[len(calls)-1]
	a.LastTool = last.Tool
	a.LastTarget = last.Target
	a.LastToolTime = last.Timestamp

	// Compute dwell file and duration from consecutive calls on the same file.
	a.DwellFile, a.DwellDuration = computeDwell(calls)

	// Compute file progress.
	a.FileProgress = computeFileProgress(a.FilesModified, estimatedFiles)

	// Detect phase and stuck status.
	a.Phase = detectPhase(calls)
	a.StuckScore, a.StuckHint = detectStuck(calls)

	return a
}

// computeDwell finds the file that has been the target of the most recent
// consecutive calls and computes the duration spent on it.
func computeDwell(calls []ToolCall) (string, time.Duration) {
	if len(calls) == 0 {
		return "", 0
	}

	last := calls[len(calls)-1]
	if last.Target == "" || !isFilePath(last.Target) {
		return "", 0
	}

	dwellFile := last.Target
	dwellStart := last.Timestamp
	for i := len(calls) - 2; i >= 0; i-- {
		if calls[i].Target != dwellFile {
			break
		}
		dwellStart = calls[i].Timestamp
	}

	return dwellFile, last.Timestamp.Sub(dwellStart)
}

// computeFileProgress returns a human-readable string like "3/5 files"
// or "3 files" depending on whether estimated files are available.
func computeFileProgress(modified map[string]bool, estimated []string) string {
	if len(estimated) > 0 {
		matched := 0
		for _, ef := range estimated {
			if modified[ef] {
				matched++
			}
		}
		return fmt.Sprintf("%d/%d files", matched, len(estimated))
	}
	return fmt.Sprintf("%d files", len(modified))
}

// isFilePath returns true if the target looks like a file path (starts with /).
func isFilePath(target string) bool {
	// For Grep/Glob the target may include a pattern after the path;
	// we only consider absolute paths.
	parts := strings.Fields(target)
	if len(parts) == 0 {
		return false
	}
	return strings.HasPrefix(parts[0], "/")
}

// SortedFilesTouched returns a sorted slice of all files the agent has
// interacted with.
func (a *Activity) SortedFilesTouched() []string {
	files := make([]string, 0, len(a.FilesTouched))
	for f := range a.FilesTouched {
		files = append(files, f)
	}
	sort.Strings(files)
	return files
}

// SortedFilesModified returns a sorted slice of all files the agent has
// modified.
func (a *Activity) SortedFilesModified() []string {
	files := make([]string, 0, len(a.FilesModified))
	for f := range a.FilesModified {
		files = append(files, f)
	}
	sort.Strings(files)
	return files
}
