package agentmon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseTranscriptLine(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantNil    bool
		wantTool   string
		wantTarget string
	}{
		{
			name:       "assistant with Edit tool_use extracts file_path",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/home/user/project/main.go","old_string":"foo","new_string":"bar"}}]}}`,
			wantTool:   "Edit",
			wantTarget: "/home/user/project/main.go",
		},
		{
			name:       "assistant with Read tool_use extracts file_path",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/home/user/project/README.md"}}]}}`,
			wantTool:   "Read",
			wantTarget: "/home/user/project/README.md",
		},
		{
			name:       "assistant with Bash tool_use extracts description",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"description":"Run tests","command":"go test ./..."}}]}}`,
			wantTool:   "Bash",
			wantTarget: "Run tests",
		},
		{
			name:       "assistant with Bash but no description truncates command",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo this is a very long command that should be truncated after eighty characters because it is too long to display"}}]}}`,
			wantTool:   "Bash",
			wantTarget: "echo this is a very long command that should be truncated after eighty character",
		},
		{
			name:       "assistant with Bash short command no description",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}}]}}`,
			wantTool:   "Bash",
			wantTarget: "ls -la",
		},
		{
			name:       "assistant with Grep tool_use extracts path and pattern",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"path":"/home/user/project","pattern":"func main"}}]}}`,
			wantTool:   "Grep",
			wantTarget: "/home/user/project func main",
		},
		{
			name:       "assistant with Glob tool_use extracts pattern only",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Glob","input":{"pattern":"**/*.go"}}]}}`,
			wantTool:   "Glob",
			wantTarget: "**/*.go",
		},
		{
			name:       "assistant with Write tool_use extracts file_path",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/home/user/project/new.go","content":"package main"}}]}}`,
			wantTool:   "Write",
			wantTarget: "/home/user/project/new.go",
		},
		{
			name:    "non-assistant type returns nil",
			line:    `{"type":"human","message":{"content":"hello"}}`,
			wantNil: true,
		},
		{
			name:    "assistant with no tool_use returns nil",
			line:    `{"type":"assistant","message":{"content":[{"type":"text","text":"I will help you."}]}}`,
			wantNil: true,
		},
		{
			name:    "invalid JSON returns nil",
			line:    `{not valid json`,
			wantNil: true,
		},
		{
			name:    "system type returns nil",
			line:    `{"type":"system","message":{"role":"system"}}`,
			wantNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTranscriptLine([]byte(tc.line))
			if tc.wantNil {
				if got != nil {
					t.Errorf("parseTranscriptLine() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("parseTranscriptLine() = nil, want non-nil")
			}
			if got.Tool != tc.wantTool {
				t.Errorf("Tool = %q, want %q", got.Tool, tc.wantTool)
			}
			if got.Target != tc.wantTarget {
				t.Errorf("Target = %q, want %q", got.Target, tc.wantTarget)
			}
			if got.Timestamp.IsZero() {
				t.Error("Timestamp should not be zero")
			}
		})
	}
}

func TestDetectPhase(t *testing.T) {
	tests := []struct {
		name  string
		calls []ToolCall
		want  string
	}{
		{
			name:  "empty calls returns exploring",
			calls: nil,
			want:  "exploring",
		},
		{
			name: "all Read/Grep calls returns exploring",
			calls: []ToolCall{
				{Tool: "Read", Target: "/a.go"},
				{Tool: "Grep", Target: "/project func"},
				{Tool: "Glob", Target: "**/*.go"},
				{Tool: "Read", Target: "/b.go"},
			},
			want: "exploring",
		},
		{
			name: "mix with Edit returns implementing",
			calls: []ToolCall{
				{Tool: "Read", Target: "/a.go"},
				{Tool: "Edit", Target: "/a.go"},
				{Tool: "Read", Target: "/b.go"},
			},
			want: "implementing",
		},
		{
			name: "Bash with go test returns testing",
			calls: []ToolCall{
				{Tool: "Read", Target: "/a.go"},
				{Tool: "Bash", Target: "go test ./..."},
			},
			want: "testing",
		},
		{
			name: "Edit then test cycle returns fixing",
			calls: []ToolCall{
				{Tool: "Edit", Target: "/a.go"},
				{Tool: "Bash", Target: "go test ./..."},
				{Tool: "Edit", Target: "/a.go"},
				{Tool: "Bash", Target: "go test ./..."},
				{Tool: "Edit", Target: "/a.go"},
				{Tool: "Bash", Target: "go test ./..."},
			},
			want: "fixing",
		},
		{
			name: "single edit-test is testing not fixing",
			calls: []ToolCall{
				{Tool: "Read", Target: "/a.go"},
				{Tool: "Edit", Target: "/a.go"},
				{Tool: "Bash", Target: "go test ./..."},
			},
			want: "testing",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detectPhase(tc.calls)
			if got != tc.want {
				t.Errorf("detectPhase() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDetectStuck(t *testing.T) {
	tests := []struct {
		name      string
		calls     []ToolCall
		wantScore int
		wantHint  string
	}{
		{
			name: "varied calls returns score 0",
			calls: []ToolCall{
				{Tool: "Read", Target: "/a.go"},
				{Tool: "Edit", Target: "/b.go"},
				{Tool: "Bash", Target: "go build"},
				{Tool: "Read", Target: "/c.go"},
				{Tool: "Grep", Target: "/project func"},
			},
			wantScore: 0,
		},
		{
			name: "too few calls returns score 0",
			calls: []ToolCall{
				{Tool: "Read", Target: "/a.go"},
				{Tool: "Read", Target: "/a.go"},
			},
			wantScore: 0,
		},
		{
			name: "same action repeated returns score 1",
			calls: func() []ToolCall {
				calls := make([]ToolCall, 10)
				for i := range calls {
					calls[i] = ToolCall{Tool: "Read", Target: "/a.go"}
				}
				return calls
			}(),
			wantScore: 1,
			wantHint:  "repeating same action:",
		},
		{
			name: "edit-test loop returns score 2",
			calls: func() []ToolCall {
				var calls []ToolCall
				for i := 0; i < 5; i++ {
					calls = append(calls,
						ToolCall{Tool: "Edit", Target: "/a.go"},
						ToolCall{Tool: "Bash", Target: "go test ./..."},
					)
				}
				return calls
			}(),
			wantScore: 2,
			wantHint:  "edit-test loop on /a.go",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			score, hint := detectStuck(tc.calls)
			if score != tc.wantScore {
				t.Errorf("detectStuck() score = %d, want %d", score, tc.wantScore)
			}
			if tc.wantHint != "" && !strings.Contains(hint, tc.wantHint) {
				t.Errorf("detectStuck() hint = %q, want to contain %q", hint, tc.wantHint)
			}
		})
	}
}

func TestMonitorScan(t *testing.T) {
	// Create a temp directory simulating the Claude project dir structure.
	tmpDir := t.TempDir()
	transcriptDir := filepath.Join(tmpDir, ".claude", "projects", "test-project")
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}

	transcriptPath := filepath.Join(transcriptDir, "session.jsonl")
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/project/main.go"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/project/main.go","old_string":"old","new_string":"new"}}]}}`,
		`{"type":"human","message":{"content":"looks good"}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"description":"go test ./...","command":"go test ./..."}}]}}`,
	}
	var data []byte
	for _, l := range lines {
		data = append(data, []byte(l+"\n")...)
	}
	if err := os.WriteFile(transcriptPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	m := &Monitor{
		worktreePath:   "/test/path",
		estimatedFiles: []string{"/project/main.go", "/project/util.go"},
		transcriptPath: transcriptPath,
	}

	activity, err := m.Scan()
	if err != nil {
		t.Fatalf("Scan() error: %v", err)
	}
	if activity == nil {
		t.Fatal("Scan() returned nil activity")
	}

	// Verify last tool info.
	if activity.LastTool != "Bash" {
		t.Errorf("LastTool = %q, want %q", activity.LastTool, "Bash")
	}

	// Verify files touched.
	if !activity.FilesTouched["/project/main.go"] {
		t.Error("FilesTouched should contain /project/main.go")
	}

	// Verify files modified.
	if !activity.FilesModified["/project/main.go"] {
		t.Error("FilesModified should contain /project/main.go")
	}

	// Verify tool counts.
	if activity.ToolCounts["Read"] != 1 {
		t.Errorf("ToolCounts[Read] = %d, want 1", activity.ToolCounts["Read"])
	}
	if activity.ToolCounts["Edit"] != 1 {
		t.Errorf("ToolCounts[Edit] = %d, want 1", activity.ToolCounts["Edit"])
	}
	if activity.ToolCounts["Bash"] != 1 {
		t.Errorf("ToolCounts[Bash] = %d, want 1", activity.ToolCounts["Bash"])
	}

	// Verify file progress (1/2 estimated files modified).
	if activity.FileProgress != "1/2 files" {
		t.Errorf("FileProgress = %q, want %q", activity.FileProgress, "1/2 files")
	}

	// Verify phase.
	if activity.Phase != "testing" {
		t.Errorf("Phase = %q, want %q", activity.Phase, "testing")
	}

	// Now append more lines and scan again to verify incremental behavior.
	extra := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/project/util.go","content":"package main"}}]}}` + "\n"
	f, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(extra); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	activity2, err := m.Scan()
	if err != nil {
		t.Fatalf("Scan() second call error: %v", err)
	}
	if activity2 == nil {
		t.Fatal("Scan() second call returned nil")
	}

	// Now both estimated files should be modified.
	if activity2.FileProgress != "2/2 files" {
		t.Errorf("FileProgress after second scan = %q, want %q", activity2.FileProgress, "2/2 files")
	}
	if activity2.LastTool != "Write" {
		t.Errorf("LastTool after second scan = %q, want %q", activity2.LastTool, "Write")
	}
	if !activity2.FilesModified["/project/util.go"] {
		t.Error("FilesModified should contain /project/util.go after second scan")
	}
}

func TestMonitorScan_NoTranscript(t *testing.T) {
	m := NewMonitor("/nonexistent/path/that/does/not/exist", nil)

	activity, err := m.Scan()
	if err != nil {
		t.Fatalf("Scan() error: %v", err)
	}
	if activity != nil {
		t.Errorf("Scan() = %+v, want nil when no transcript exists", activity)
	}
}

func TestBuildActivity(t *testing.T) {
	now := time.Now()
	calls := []ToolCall{
		{Timestamp: now.Add(-4 * time.Second), Tool: "Read", Target: "/project/a.go"},
		{Timestamp: now.Add(-3 * time.Second), Tool: "Grep", Target: "/project func main"},
		{Timestamp: now.Add(-2 * time.Second), Tool: "Edit", Target: "/project/a.go"},
		{Timestamp: now.Add(-1 * time.Second), Tool: "Bash", Target: "git commit -m 'update'"},
		{Timestamp: now, Tool: "Read", Target: "/project/b.go"},
	}

	estimated := []string{"/project/a.go", "/project/b.go", "/project/c.go"}
	activity := buildActivity(calls, estimated)

	// Verify last tool.
	if activity.LastTool != "Read" {
		t.Errorf("LastTool = %q, want %q", activity.LastTool, "Read")
	}
	if activity.LastTarget != "/project/b.go" {
		t.Errorf("LastTarget = %q, want %q", activity.LastTarget, "/project/b.go")
	}

	// Verify files touched: /project/a.go (Read, Edit), /project/b.go (Read).
	// Grep target "/project func main" has path "/project" which is a dir, starts with /.
	if len(activity.FilesTouched) < 2 {
		t.Errorf("FilesTouched has %d entries, want at least 2", len(activity.FilesTouched))
	}
	if !activity.FilesTouched["/project/a.go"] {
		t.Error("FilesTouched should contain /project/a.go")
	}
	if !activity.FilesTouched["/project/b.go"] {
		t.Error("FilesTouched should contain /project/b.go")
	}

	// Verify files modified: only /project/a.go from Edit.
	if len(activity.FilesModified) != 1 {
		t.Errorf("FilesModified has %d entries, want 1", len(activity.FilesModified))
	}
	if !activity.FilesModified["/project/a.go"] {
		t.Error("FilesModified should contain /project/a.go")
	}

	// Verify tool counts.
	if activity.ToolCounts["Read"] != 2 {
		t.Errorf("ToolCounts[Read] = %d, want 2", activity.ToolCounts["Read"])
	}
	if activity.ToolCounts["Edit"] != 1 {
		t.Errorf("ToolCounts[Edit] = %d, want 1", activity.ToolCounts["Edit"])
	}

	// Verify HasCommit.
	if !activity.HasCommit {
		t.Error("HasCommit should be true")
	}

	// Verify file progress: 1 of 3 estimated files modified.
	if activity.FileProgress != "1/3 files" {
		t.Errorf("FileProgress = %q, want %q", activity.FileProgress, "1/3 files")
	}

	// Verify phase: has Edit so should be implementing (no test commands).
	if activity.Phase != "implementing" {
		t.Errorf("Phase = %q, want %q", activity.Phase, "implementing")
	}

	// Verify stuck score.
	if activity.StuckScore != 0 {
		t.Errorf("StuckScore = %d, want 0", activity.StuckScore)
	}
}

func TestBuildActivity_NoEstimatedFiles(t *testing.T) {
	calls := []ToolCall{
		{Timestamp: time.Now(), Tool: "Edit", Target: "/project/a.go"},
		{Timestamp: time.Now(), Tool: "Write", Target: "/project/b.go"},
	}

	activity := buildActivity(calls, nil)

	// Without estimated files, progress shows just count.
	if activity.FileProgress != "2 files" {
		t.Errorf("FileProgress = %q, want %q", activity.FileProgress, "2 files")
	}
}

func TestComputeDwell(t *testing.T) {
	now := time.Now()
	calls := []ToolCall{
		{Timestamp: now.Add(-5 * time.Second), Tool: "Read", Target: "/other.go"},
		{Timestamp: now.Add(-3 * time.Second), Tool: "Read", Target: "/main.go"},
		{Timestamp: now.Add(-2 * time.Second), Tool: "Edit", Target: "/main.go"},
		{Timestamp: now, Tool: "Read", Target: "/main.go"},
	}

	file, dur := computeDwell(calls)
	if file != "/main.go" {
		t.Errorf("dwell file = %q, want %q", file, "/main.go")
	}
	if dur != 3*time.Second {
		t.Errorf("dwell duration = %v, want %v", dur, 3*time.Second)
	}
}

func TestComputeFileProgress(t *testing.T) {
	tests := []struct {
		name      string
		modified  map[string]bool
		estimated []string
		want      string
	}{
		{
			name:      "with estimated files",
			modified:  map[string]bool{"/a.go": true, "/b.go": true},
			estimated: []string{"/a.go", "/b.go", "/c.go"},
			want:      "2/3 files",
		},
		{
			name:      "no estimated files",
			modified:  map[string]bool{"/a.go": true},
			estimated: nil,
			want:      "1 files",
		},
		{
			name:      "no modified files with estimates",
			modified:  map[string]bool{},
			estimated: []string{"/a.go"},
			want:      "0/1 files",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := computeFileProgress(tc.modified, tc.estimated)
			if got != tc.want {
				t.Errorf("computeFileProgress() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsFilePath(t *testing.T) {
	tests := []struct {
		target string
		want   bool
	}{
		{"/home/user/file.go", true},
		{"/project func main", true},
		{"go test ./...", false},
		{"", false},
		{"**/*.go", false},
	}

	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			got := isFilePath(tc.target)
			if got != tc.want {
				t.Errorf("isFilePath(%q) = %v, want %v", tc.target, got, tc.want)
			}
		})
	}
}

func TestSortedFiles(t *testing.T) {
	a := &Activity{
		FilesTouched:  map[string]bool{"/c.go": true, "/a.go": true, "/b.go": true},
		FilesModified: map[string]bool{"/b.go": true, "/a.go": true},
	}

	touched := a.SortedFilesTouched()
	want := []string{"/a.go", "/b.go", "/c.go"}
	if len(touched) != len(want) {
		t.Fatalf("SortedFilesTouched() len = %d, want %d", len(touched), len(want))
	}
	for i, f := range touched {
		if f != want[i] {
			t.Errorf("SortedFilesTouched()[%d] = %q, want %q", i, f, want[i])
		}
	}

	modified := a.SortedFilesModified()
	wantMod := []string{"/a.go", "/b.go"}
	if len(modified) != len(wantMod) {
		t.Fatalf("SortedFilesModified() len = %d, want %d", len(modified), len(wantMod))
	}
	for i, f := range modified {
		if f != wantMod[i] {
			t.Errorf("SortedFilesModified()[%d] = %q, want %q", i, f, wantMod[i])
		}
	}
}

func TestParseTranscriptLine_MultipleContentBlocks(t *testing.T) {
	// When multiple content blocks exist, should return the first tool_use.
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"Let me read that."},{"type":"tool_use","name":"Read","input":{"file_path":"/project/main.go"}}]}}`
	tc := parseTranscriptLine([]byte(line))
	if tc == nil {
		t.Fatal("parseTranscriptLine() = nil, want non-nil")
	}
	if tc.Tool != "Read" {
		t.Errorf("Tool = %q, want %q", tc.Tool, "Read")
	}
}

func TestDetectPhase_NpmTest(t *testing.T) {
	calls := []ToolCall{
		{Tool: "Read", Target: "/a.js"},
		{Tool: "Bash", Target: "npm test"},
	}
	got := detectPhase(calls)
	if got != "testing" {
		t.Errorf("detectPhase() = %q, want %q", got, "testing")
	}
}

func TestDetectStuck_RepeatingAction(t *testing.T) {
	// 6 identical calls out of 10 should trigger score=1 for "repeating same action"
	calls := make([]ToolCall, 10)
	for i := 0; i < 6; i++ {
		calls[i] = ToolCall{Tool: "Edit", Target: "/a.go"}
	}
	for i := 6; i < 10; i++ {
		calls[i] = ToolCall{Tool: "Read", Target: "/b.go"}
	}

	score, hint := detectStuck(calls)
	if score != 1 {
		t.Errorf("detectStuck() score = %d, want 1", score)
	}
	if !strings.Contains(hint, "repeating same action") {
		t.Errorf("detectStuck() hint = %q, want to contain 'repeating same action'", hint)
	}
}
