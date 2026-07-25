package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/benchv2"
	"github.com/stretchr/testify/require"
)

func TestValidateCanaryAcceptsEverySupportedNormalizerWireFormat(t *testing.T) {
	tests := []struct {
		harness, fixture, output string
	}{
		{benchv2.AdapterOpenCode, "opencode.jsonl", "OpenCode finished exactly."},
		{benchv2.AdapterQwenCode, "qwen-code.jsonl", "Qwen finished exactly."},
		{benchv2.AdapterMiniSWE, "mini-swe-agent.json", "Mini-SWE finished exactly."},
		{benchv2.AdapterPi, "pi.jsonl", "Pi finished exactly."},
		{benchv2.AdapterAider, "aider.json", "Aider finished exactly."},
		{benchv2.AdapterOpenHands, "openhands.json", "OpenHands finished exactly."},
	}
	for _, test := range tests {
		t.Run(test.harness, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "benchv2", "testdata", "external", test.fixture))
			require.NoError(t, err)
			run, err := validateCanary(test.harness, raw)
			require.NoError(t, err)
			require.Equal(t, test.output, run.Output)
		})
	}
}

func TestValidateCanaryRejectsUnsupportedHarness(t *testing.T) {
	_, err := validateCanary("unknown", []byte(`{}`))
	require.ErrorContains(t, err, "unsupported canary")
}
