package benchv2

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func adapterRequest() TrialRequest {
	return TrialRequest{
		Task:    TaskSpec{SystemPrompt: "system", UserMessage: "task", AllowedToolPolicies: []string{ToolPolicyStructured, ToolPolicySandboxed}, ReadPaths: []string{"read.cpp"}, WritePaths: []string{"write.cpp"}, Budget: Budget{MaxToolCalls: 12, MaxIterations: 8, TimeoutSeconds: 600}},
		WorkDir: "/bench/work", Seed: 42, Temperature: .6,
		Harness: HarnessConfig{AdapterModelRef: "provider/qwen36", OuterIsolation: "outer_container", ToolPolicy: ToolPolicySandboxed},
		Runtime: RuntimeAttestation{ModelID: "raw-qwen-attestation"},
	}
}

func TestExternalAdapterInvocationContracts(t *testing.T) {
	tests := []struct {
		kind   string
		want   []string
		forbid []string
	}{
		{AdapterOpenCode, []string{"run", "--pure", "--auto", "--format", "json", "--agent", "build", "--dir", "/bench/work", "--model", "provider/qwen36"}, []string{"raw-qwen-attestation"}},
		{AdapterQwenCode, []string{"--output-format", "stream-json", "--safe-mode", "--yolo", "--max-tool-calls", "12", "--max-session-turns", "8", "--max-wall-time", "600s", "--exclude-tools", "agent"}, nil},
		{AdapterMiniSWE, []string{"-t", "-m", "provider/qwen36", "-y", "--exit-immediately", "-o"}, nil},
		{AdapterPi, []string{"--mode", "json", "--no-session", "--no-context-files", "--model", "provider/qwen36", "--system-prompt"}, []string{"-p", "--prompt"}},
	}
	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			adapter := ExternalCLIAdapter{Kind: tc.kind, Executable: tc.kind, Version: "1.17.0", DryRun: true, Isolation: "outer_container"}
			invocation, err := adapter.BuildInvocation(adapterRequest())
			require.NoError(t, err)
			joined := strings.Join(invocation.Args, " ")
			for _, want := range tc.want {
				require.Contains(t, joined, want)
			}
			for _, forbidden := range tc.forbid {
				require.NotContains(t, invocation.Args, forbidden)
			}
			require.Equal(t, "/bench/work", invocation.WorkDir)
		})
	}
}

func TestExternalAdaptersRequireOuterIsolation(t *testing.T) {
	request := adapterRequest()
	adapter := ExternalCLIAdapter{Kind: AdapterOpenCode, Executable: "opencode", Version: "1.17.0", DryRun: true}
	_, err := adapter.BuildInvocation(request)
	require.ErrorContains(t, err, "outer-container")
}
