package ctxmon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectDirName(t *testing.T) {
	got := ProjectDirName("/home/godinj/git/drem-canvas.git/feature/abc/agent-xyz")
	want := "-home-godinj-git-drem-canvas.git-feature-abc-agent-xyz"
	if got != want {
		t.Errorf("ProjectDirName = %q, want %q", got, want)
	}
}

func TestParseTranscriptUsage(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.jsonl")

	lines := []string{
		`{"type":"system","message":{"role":"system"}}`,
		`{"type":"human","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","message":{"model":"claude-opus-4-6","usage":{"input_tokens":100,"cache_creation_input_tokens":500,"cache_read_input_tokens":10000,"output_tokens":200}}}`,
		`{"type":"assistant","message":{"model":"claude-opus-4-6","usage":{"input_tokens":50,"cache_creation_input_tokens":1000,"cache_read_input_tokens":30000,"output_tokens":400}}}`,
	}
	var data []byte
	for _, l := range lines {
		data = append(data, []byte(l+"\n")...)
	}
	if err := os.WriteFile(transcript, data, 0o644); err != nil {
		t.Fatal(err)
	}

	usage, err := parseTranscriptUsage(transcript)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage == nil {
		t.Fatal("expected usage, got nil")
	}

	// Last entry: 50 + 1000 + 30000 = 31050 input tokens
	wantInput := 31050
	if usage.TotalInputTokens != wantInput {
		t.Errorf("TotalInputTokens = %d, want %d", usage.TotalInputTokens, wantInput)
	}
	if usage.TotalOutputTokens != 400 {
		t.Errorf("TotalOutputTokens = %d, want 400", usage.TotalOutputTokens)
	}

	// 31050 / 200000 = 15%
	wantPct := 15
	if usage.UsedPercent != wantPct {
		t.Errorf("UsedPercent = %d, want %d", usage.UsedPercent, wantPct)
	}
	if usage.ContextWindowSize != defaultContextWindowSize {
		t.Errorf("ContextWindowSize = %d, want %d", usage.ContextWindowSize, defaultContextWindowSize)
	}
}

func TestParseTranscriptUsageEmpty(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"system"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	usage, err := parseTranscriptUsage(transcript)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage != nil {
		t.Errorf("expected nil usage for transcript with no assistant entries, got %+v", usage)
	}
}

func TestReadTranscriptUsageNoProjectDir(t *testing.T) {
	usage, err := ReadTranscriptUsage("/nonexistent/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage != nil {
		t.Errorf("expected nil usage for nonexistent project dir, got %+v", usage)
	}
}
