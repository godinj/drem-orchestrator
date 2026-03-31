package ctxmon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectDirName(t *testing.T) {
	got := ProjectDirName("/home/godinj/git/drem-canvas.git/feature/abc/agent-xyz")
	want := "-home-godinj-git-drem-canvas-git-feature-abc-agent-xyz"
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

	// Cumulative across both entries:
	// input: 100 + 50 = 150
	// cache_creation: 500 + 1000 = 1500
	// cache_read: 10000 + 30000 = 40000
	// total input = 150 + 1500 + 40000 = 41650
	wantInput := 41650
	if usage.TotalInputTokens != wantInput {
		t.Errorf("TotalInputTokens = %d, want %d", usage.TotalInputTokens, wantInput)
	}

	// output: 200 + 400 = 600
	wantOutput := 600
	if usage.TotalOutputTokens != wantOutput {
		t.Errorf("TotalOutputTokens = %d, want %d", usage.TotalOutputTokens, wantOutput)
	}

	// 41650 / 200000 = 20%
	wantPct := 20
	if usage.UsedPercent != wantPct {
		t.Errorf("UsedPercent = %d, want %d", usage.UsedPercent, wantPct)
	}
	if usage.ContextWindowSize != defaultContextWindowSize {
		t.Errorf("ContextWindowSize = %d, want %d", usage.ContextWindowSize, defaultContextWindowSize)
	}

	// Cost should be estimated from opus pricing:
	// input: 150 × $15/M = $0.00225
	// cache_creation: 1500 × $18.75/M = $0.028125
	// cache_read: 40000 × $1.875/M = $0.075
	// output: 600 × $75/M = $0.045
	// total = $0.150375
	wantCost := 0.150375
	if diff := usage.TotalCostUSD - wantCost; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("TotalCostUSD = %f, want %f", usage.TotalCostUSD, wantCost)
	}
}

func TestParseTranscriptUsageSonnetCost(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.jsonl")

	lines := []string{
		`{"type":"assistant","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":1000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":500}}}`,
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

	// Sonnet pricing: input 1000 × $3/M + output 500 × $15/M
	// = $0.003 + $0.0075 = $0.0105
	wantCost := 0.0105
	if diff := usage.TotalCostUSD - wantCost; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("TotalCostUSD = %f, want %f", usage.TotalCostUSD, wantCost)
	}
}

func TestParseTranscriptUsageUnknownModel(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.jsonl")

	lines := []string{
		`{"type":"assistant","message":{"model":"unknown-model-v1","usage":{"input_tokens":1000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":500}}}`,
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

	// Unknown model — cost should be 0
	if usage.TotalCostUSD != 0 {
		t.Errorf("TotalCostUSD = %f, want 0 for unknown model", usage.TotalCostUSD)
	}
	// But tokens should still be populated
	if usage.TotalInputTokens != 1000 {
		t.Errorf("TotalInputTokens = %d, want 1000", usage.TotalInputTokens)
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
