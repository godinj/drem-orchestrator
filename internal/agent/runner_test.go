package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestPromptWriteVerification_Success(t *testing.T) {
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "agent-prompt.md")
	prompt := "This is a test prompt with special chars: $HOME $(echo hello) `backticks` 'quotes'"

	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Verify readback matches.
	written, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(written) != len(prompt) {
		t.Errorf("prompt length mismatch: wrote %d, read %d", len(prompt), len(written))
	}
	if string(written) != prompt {
		t.Errorf("prompt content mismatch: wrote %q, read %q", prompt, string(written))
	}
}

func TestPromptWriteVerification_ReadOnlyPath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("skipping read-only path test when running as root")
	}
	dir := t.TempDir()

	// Create a read-only directory.
	roDir := filepath.Join(dir, "readonly")
	if err := os.MkdirAll(roDir, 0o555); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Cleanup(func() {
		// Restore write permissions so t.TempDir cleanup works.
		_ = os.Chmod(roDir, 0o755)
	})

	promptPath := filepath.Join(roDir, "agent-prompt.md")
	prompt := "test prompt"

	err := os.WriteFile(promptPath, []byte(prompt), 0o644)
	if err == nil {
		t.Fatal("expected WriteFile to fail on read-only directory, but it succeeded")
	}
}

func TestTruncateTitle(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "short string under limit",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "exact length",
			input:  "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "over limit truncated with ellipsis",
			input:  "hello world",
			maxLen: 5,
			want:   "hell\u2026",
		},
		{
			name:   "empty string",
			input:  "",
			maxLen: 10,
			want:   "",
		},
		{
			name:   "single char limit with longer input",
			input:  "abc",
			maxLen: 1,
			want:   "\u2026",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncateTitle(tc.input, tc.maxLen)
			if got != tc.want {
				t.Errorf("TruncateTitle(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestSanitizeSessionName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "valid alphanumeric with hyphens",
			input: "drem-agent-123",
			want:  "drem-agent-123",
		},
		{
			name:  "dots replaced with hyphens",
			input: "agent.v1.2",
			want:  "agent-v1-2",
		},
		{
			name:  "colons replaced with hyphens",
			input: "agent:task:sub",
			want:  "agent-task-sub",
		},
		{
			name:  "slashes preserved",
			input: "drem/agent/test",
			want:  "drem/agent/test",
		},
		{
			name:  "mixed dots and colons",
			input: "drem/plan.v2:fix",
			want:  "drem/plan-v2-fix",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeSessionName(tc.input)
			if got != tc.want {
				t.Errorf("SanitizeSessionName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestAgentTypeLabel(t *testing.T) {
	tests := []struct {
		name      string
		agentType model.AgentType
		want      string
	}{
		{name: "planner", agentType: model.AgentPlanner, want: "plan"},
		{name: "coder", agentType: model.AgentCoder, want: "code"},
		{name: "researcher", agentType: model.AgentResearcher, want: "research"},
		{name: "reviewer", agentType: model.AgentReviewer, want: "review"},
		{name: "fixer", agentType: model.AgentFixer, want: "fix"},
		{name: "orchestrator fallback", agentType: model.AgentOrchestrator, want: "orchestrator"},
		{name: "unknown fallback", agentType: model.AgentType("custom"), want: "custom"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AgentTypeLabel(tc.agentType)
			if got != tc.want {
				t.Errorf("AgentTypeLabel(%q) = %q, want %q", tc.agentType, got, tc.want)
			}
		})
	}
}

func TestCanSpawn(t *testing.T) {
	t.Run("under limit returns true", func(t *testing.T) {
		r := &Runner{
			maxConcurrent: 2,
			running:       make(map[uuid.UUID]*RunningAgent),
			completions:   make(chan Completion, 2),
			semaphore:     make(chan struct{}, 2),
		}
		if !r.CanSpawn() {
			t.Error("CanSpawn() = false, want true when no agents running")
		}
	})

	t.Run("at limit returns false", func(t *testing.T) {
		r := &Runner{
			maxConcurrent: 2,
			running:       make(map[uuid.UUID]*RunningAgent),
			completions:   make(chan Completion, 2),
			semaphore:     make(chan struct{}, 2),
		}
		// Fill the running map to the limit.
		id1, id2 := uuid.New(), uuid.New()
		r.running[id1] = &RunningAgent{AgentID: id1}
		r.running[id2] = &RunningAgent{AgentID: id2}

		if r.CanSpawn() {
			t.Error("CanSpawn() = true, want false when at max concurrent limit")
		}
	})
}

func TestGetRunningAgents_Empty(t *testing.T) {
	r := &Runner{
		maxConcurrent: 2,
		running:       make(map[uuid.UUID]*RunningAgent),
		completions:   make(chan Completion, 2),
		semaphore:     make(chan struct{}, 2),
	}

	agents := r.GetRunningAgents()
	if agents == nil {
		t.Fatal("GetRunningAgents() returned nil, want empty slice")
	}
	if len(agents) != 0 {
		t.Errorf("GetRunningAgents() returned %d agents, want 0", len(agents))
	}
}

func TestDrainCompletions_Empty(t *testing.T) {
	r := &Runner{
		maxConcurrent: 2,
		running:       make(map[uuid.UUID]*RunningAgent),
		completions:   make(chan Completion, 2),
		semaphore:     make(chan struct{}, 2),
	}

	completions := r.DrainCompletions()
	if len(completions) != 0 {
		t.Errorf("DrainCompletions() returned %d completions, want 0", len(completions))
	}
}
