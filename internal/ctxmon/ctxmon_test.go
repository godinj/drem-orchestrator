package ctxmon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadUsageFile(t *testing.T) {
	tests := []struct {
		name      string
		content   *string // nil means don't create file
		wantNil   bool
		wantErr   bool
		checkUsed int // expected UsedPercent if not nil/err
	}{
		{
			name: "valid JSON",
			content: ptr(`{
				"total_input_tokens": 15234,
				"total_output_tokens": 4521,
				"context_window_size": 200000,
				"used_percentage": 8,
				"remaining_percentage": 92,
				"total_cost_usd": 0.01234
			}`),
			checkUsed: 8,
		},
		{
			name:    "file not found",
			content: nil,
			wantNil: true,
		},
		{
			name:    "corrupt JSON",
			content: ptr(`{not json`),
			wantErr: true,
		},
		{
			name:    "empty file",
			content: ptr(""),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "context-usage.json")

			if tt.content != nil {
				if err := os.WriteFile(path, []byte(*tt.content), 0o644); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}

			usage, err := ReadUsageFile(path)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantNil {
				if usage != nil {
					t.Fatalf("expected nil, got %+v", usage)
				}
				return
			}

			if usage == nil {
				t.Fatal("expected non-nil usage")
			}
			if usage.UsedPercent != tt.checkUsed {
				t.Errorf("UsedPercent = %d, want %d", usage.UsedPercent, tt.checkUsed)
			}
			if usage.LastUpdated.IsZero() {
				t.Error("LastUpdated should be set")
			}
		})
	}
}

func TestReadUsageFileAllFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	if err := os.WriteFile(path, []byte(`{
		"total_input_tokens": 15234,
		"total_output_tokens": 4521,
		"context_window_size": 200000,
		"used_percentage": 8,
		"remaining_percentage": 92,
		"total_cost_usd": 0.01234
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	u, err := ReadUsageFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if u.TotalInputTokens != 15234 {
		t.Errorf("TotalInputTokens = %d, want 15234", u.TotalInputTokens)
	}
	if u.TotalOutputTokens != 4521 {
		t.Errorf("TotalOutputTokens = %d, want 4521", u.TotalOutputTokens)
	}
	if u.ContextWindowSize != 200000 {
		t.Errorf("ContextWindowSize = %d, want 200000", u.ContextWindowSize)
	}
	if u.RemainingPercent != 92 {
		t.Errorf("RemainingPercent = %d, want 92", u.RemainingPercent)
	}
	if u.TotalCostUSD != 0.01234 {
		t.Errorf("TotalCostUSD = %f, want 0.01234", u.TotalCostUSD)
	}
}

func TestUsageFilePath(t *testing.T) {
	got := UsageFilePath("/tmp/worktree")
	want := "/tmp/worktree/.claude/context-usage.json"
	if got != want {
		t.Errorf("UsageFilePath = %q, want %q", got, want)
	}
}

func TestCompactionSignalPath(t *testing.T) {
	got := CompactionSignalPath("/tmp/worktree")
	want := "/tmp/worktree/.claude/compaction-triggered"
	if got != want {
		t.Errorf("CompactionSignalPath = %q, want %q", got, want)
	}
}

func TestStatusScript(t *testing.T) {
	outputPath := "/tmp/test/.claude/context-usage.json"
	script := StatusScript(outputPath)

	if len(script) == 0 {
		t.Fatal("script is empty")
	}

	// Verify the script contains the output path.
	if !strings.Contains(script, outputPath) {
		t.Error("script does not contain output path")
	}

	// Verify it starts with a shebang.
	if !strings.Contains(script, "#!/bin/sh") {
		t.Error("script missing shebang")
	}

	// If jq is available, test the script end-to-end.
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available, skipping end-to-end script test")
	}

	dir := t.TempDir()
	outFile := filepath.Join(dir, "context-usage.json")
	scriptContent := StatusScript(outFile)
	scriptPath := filepath.Join(dir, "test-status.sh")

	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}

	sampleInput := `{
		"context_window": {
			"total_input_tokens": 15234,
			"total_output_tokens": 4521,
			"context_window_size": 200000,
			"used_percentage": 8,
			"remaining_percentage": 92,
			"current_usage": {
				"input_tokens": 8500,
				"output_tokens": 1200,
				"cache_creation_input_tokens": 5000,
				"cache_read_input_tokens": 2000
			}
		},
		"cost": {
			"total_cost_usd": 0.01234,
			"total_duration_ms": 45000,
			"total_api_duration_ms": 2300
		}
	}`

	cmd := exec.Command("sh", scriptPath)
	cmd.Stdin = strings.NewReader(sampleInput)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script execution failed: %v\n%s", err, out)
	}

	// Read and verify the output file.
	usage, err := ReadUsageFile(outFile)
	if err != nil {
		t.Fatalf("reading script output: %v", err)
	}
	if usage.TotalInputTokens != 15234 {
		t.Errorf("TotalInputTokens = %d, want 15234", usage.TotalInputTokens)
	}
	if usage.UsedPercent != 8 {
		t.Errorf("UsedPercent = %d, want 8", usage.UsedPercent)
	}
	if usage.TotalCostUSD != 0.01234 {
		t.Errorf("TotalCostUSD = %f, want 0.01234", usage.TotalCostUSD)
	}
}

func TestHooksJSON(t *testing.T) {
	hooks := HooksJSON("/tmp/signal")

	preCompact, ok := hooks["PreCompact"]
	if !ok {
		t.Fatal("missing PreCompact key")
	}

	entries, ok := preCompact.([]any)
	if !ok || len(entries) != 1 {
		t.Fatal("PreCompact should have exactly one entry")
	}

	entry, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatal("PreCompact entry should be a map")
	}

	hooksList, ok := entry["hooks"].([]any)
	if !ok || len(hooksList) != 1 {
		t.Fatal("hooks should have exactly one hook")
	}

	hook, ok := hooksList[0].(map[string]any)
	if !ok {
		t.Fatal("hook should be a map")
	}

	if hook["type"] != "command" {
		t.Errorf("hook type = %v, want command", hook["type"])
	}
	if hook["command"] != "touch /tmp/signal" {
		t.Errorf("hook command = %v, want 'touch /tmp/signal'", hook["command"])
	}
}

func ptr(s string) *string { return &s }
