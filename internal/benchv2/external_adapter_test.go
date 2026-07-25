package benchv2

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeOuterExecutor struct {
	result OuterExecutionResult
	seen   *OuterExecutionSpec
}

func (executor fakeOuterExecutor) Execute(_ context.Context, spec OuterExecutionSpec) (OuterExecutionResult, error) {
	*executor.seen = spec
	if _, err := os.Stat(filepath.Join(spec.HostWorkspace, "secret.cpp")); !os.IsNotExist(err) {
		return OuterExecutionResult{}, errUnexpectedVisibleFixture
	}
	if _, err := os.Stat(filepath.Join(spec.HostWorkspace, ".git")); !os.IsNotExist(err) {
		return OuterExecutionResult{}, errUnexpectedVisibleFixture
	}
	if err := os.WriteFile(filepath.Join(spec.HostWorkspace, "write.cpp"), []byte("after"), 0o644); err != nil {
		return OuterExecutionResult{}, err
	}
	return executor.result, nil
}

type fakeUsageAttestor struct {
	usage   ServerUsage
	session UsageSession
	err     error
}

func (attestor fakeUsageAttestor) StartServerUsage(context.Context, TrialRequest) (UsageSession, error) {
	if attestor.session.CorrelationID == "" {
		return UsageSession{CorrelationID: "trial", BaseURL: "http://usage-proxy:8080/v1", APIKey: "trial-secret"}, nil
	}
	return attestor.session, nil
}

func (attestor fakeUsageAttestor) AttestServerUsage(_ context.Context, session UsageSession) (ServerUsage, error) {
	if attestor.err != nil {
		return ServerUsage{}, attestor.err
	}
	if attestor.usage.CorrelationID == "" {
		attestor.usage.CorrelationID = session.CorrelationID
	}
	return attestor.usage, nil
}

func TestExternalAdapterRetainsOuterExitEvidenceWhenUsageIsIncomplete(t *testing.T) {
	fixture := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "read.cpp"), []byte("read"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "write.cpp"), []byte("before"), 0o644))
	seen := OuterExecutionSpec{}
	adapter := externalAdapter(AdapterQwenCode, NormalizerQwenCode)
	adapter.Executor = fakeOuterExecutor{
		result: OuterExecutionResult{ExitCode: 125, Stderr: []byte("invalid mount contract"), Artifacts: map[string][]byte{}}, seen: &seen,
	}
	adapter.UsageAttestor = fakeUsageAttestor{err: errors.New("ledger incomplete")}
	_, err := adapter.Run(context.Background(), adapterRequest(fixture, AdapterQwenCode, NormalizerQwenCode))
	require.ErrorContains(t, err, "outer harness exited 125: invalid mount contract")
}

func TestExternalAdapterClassifiesUpstreamRequestRejectionBeforeTrajectoryErrors(t *testing.T) {
	fixture := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "read.cpp"), []byte("read"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "write.cpp"), []byte("before"), 0o644))
	seen := OuterExecutionSpec{}
	adapter := externalAdapter(AdapterQwenCode, NormalizerQwenCode)
	adapter.Executor = fakeOuterExecutor{result: OuterExecutionResult{Stdout: []byte(`{"type":"system","subtype":"init","session_id":"partial"}`), Artifacts: map[string][]byte{}}, seen: &seen}
	adapter.UsageAttestor = fakeUsageAttestor{usage: ServerUsage{
		Source: ServerUsageSourceProxy, RequestsRejected: 1, RequestsTotal: 1,
		Rejections: []ServerUsageRejection{{HTTPStatus: 400, Count: 1}}, Complete: true,
	}}
	run, err := adapter.Run(context.Background(), adapterRequest(fixture, AdapterQwenCode, NormalizerQwenCode))
	require.ErrorContains(t, err, "upstream rejected 1 harness request")
	require.Equal(t, "upstream_rejected", run.StopReason)
	require.Equal(t, 1, run.ServerUsage.RequestsRejected)
	require.True(t, run.ServerUsage.Complete)
	require.Zero(t, run.Telemetry.TokensIn)
	contents, readErr := os.ReadFile(filepath.Join(fixture, "write.cpp"))
	require.NoError(t, readErr)
	require.Equal(t, "before", string(contents), "rejected runs must not apply scoped workspace changes")
}

var errUnexpectedVisibleFixture = &scopeTestError{"full fixture leaked into outer workspace"}

type scopeTestError struct{ message string }

func (err *scopeTestError) Error() string { return err.message }

func adapterRequest(workDir, kind, normalizer string) TrialRequest {
	modelRef := "provider/qwen36"
	if adapterUsesLiteLLM(kind) {
		modelRef = "openai/qwen36"
	}
	return TrialRequest{
		Task: TaskSpec{
			SystemPrompt: "system", UserMessage: "task", AllowedToolPolicies: []string{ToolPolicyStructured, ToolPolicySandboxed},
			ReadPaths: []string{"read.cpp", "write.cpp"}, WritePaths: []string{"write.cpp"},
			Budget: Budget{MaxOutputTokens: 1024, MaxToolCalls: 12, MaxIterations: 8, TimeoutSeconds: 600},
		},
		WorkDir: workDir, Seed: 42, Temperature: .6, TopP: .95, TopK: 20, ContextWindow: 32768, PreserveThinking: true,
		Harness: HarnessConfig{
			Name: kind, Version: "pinned", ConfigSHA256: "cfg", AdapterModelRef: modelRef,
			OuterIsolation: "outer_container", ToolPolicy: ToolPolicySandboxed, TrajectoryNormalizer: normalizer,
			InferenceEnvContract: inferenceEnvContractForAdapter(kind),
		},
		Runtime: RuntimeAttestation{ModelID: "raw-qwen-attestation"},
	}
}

func externalAdapter(kind, normalizer string) ExternalCLIAdapter {
	return ExternalCLIAdapter{
		Kind: kind, Executable: externalExecutable(kind), Version: "pinned", Isolation: "outer_container",
		Image: testOuterImage, Network: OuterNetworkPolicy{Mode: OuterNetworkIsolatedInference, NetworkName: "canvasbench-inference"},
		Normalizer: normalizer,
	}
}

func TestExternalAdapterInvocationContracts(t *testing.T) {
	tests := []struct {
		kind       string
		normalizer string
		want       []string
		forbid     []string
	}{
		{AdapterOpenCode, NormalizerOpenCode, []string{"run", "--pure", "--auto", "--format", "json", "--agent", "build", "--dir", "/workspace", "--model", "provider/qwen36"}, []string{"raw-qwen-attestation"}},
		{AdapterQwenCode, NormalizerQwenCode, []string{"--output-format", "stream-json", "--auth-type", "openai", "--safe-mode", "--yolo", "--max-tool-calls", "12", "--max-session-turns", "8", "--max-wall-time", "600s", "--exclude-tools", "agent"}, nil},
		{AdapterMiniSWE, NormalizerMiniSWE, []string{"-t", "-m", "openai/qwen36", "-y", "--exit-immediately", "-o", "/workspace/.canvasbench/mini-swe-agent-trajectory.json"}, nil},
		{AdapterPi, NormalizerPi, []string{"--mode", "json", "--no-session", "--no-context-files", "--model", "provider/qwen36", "--system-prompt"}, []string{"-p", "--prompt"}},
		{AdapterAider, NormalizerAider, []string{"--model", "openai/qwen36", "--message", "--edit-format", "diff", "--input-history-file", "/home/bench/.aider.input.history", "--chat-history-file", "/home/bench/.aider.chat.history.md", "--llm-history-file", "/home/bench/.aider.llm.history", "--read", "read.cpp", "--file", "write.cpp"}, []string{"raw-qwen-attestation"}},
		{AdapterOpenHands, NormalizerOpenHands, []string{"--model", "openai/qwen36", "--headless", "--json", "--yolo", "--override-with-envs", "-t"}, []string{"raw-qwen-attestation"}},
		{AdapterGoose, NormalizerGoose, []string{"run", "--no-session", "--no-profile", "--with-builtin", "developer", "--provider", "openai", "--model", "provider/qwen36", "--max-turns", "8", "--quiet", "--output-format", "json", "--text"}, []string{"raw-qwen-attestation"}},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			adapter := externalAdapter(test.kind, test.normalizer)
			invocation, err := adapter.BuildInvocation(adapterRequest("/host/fixture", test.kind, test.normalizer), UsageSession{
				CorrelationID: "trial", BaseURL: "http://usage-proxy:8080/v1", APIKey: "trial-secret",
			})
			require.NoError(t, err)
			joined := strings.Join(invocation.Args, " ")
			for _, want := range test.want {
				require.Contains(t, joined, want)
			}
			for _, forbidden := range test.forbid {
				require.NotContains(t, invocation.Args, forbidden)
			}
			require.Equal(t, outerWorkspace, invocation.WorkDir)
			require.NotContains(t, invocation.Env, "CANVASBENCH_USAGE_PROXY_ADMIN_TOKEN")
			require.NotContains(t, invocation.Env, "OPENAI_API_KEY")
			require.Equal(t, "trial-secret", invocation.SensitiveEnv["OPENAI_API_KEY"])
			if test.kind == AdapterMiniSWE {
				require.Equal(t, "http://usage-proxy:8080/v1", invocation.Env["OPENAI_API_BASE"])
				require.NotContains(t, invocation.Env, "OPENAI_BASE_URL")
			} else {
				require.Equal(t, "http://usage-proxy:8080/v1", invocation.Env["OPENAI_BASE_URL"])
			}
			require.Equal(t, "42", invocation.Env["CANVASBENCH_SEED"])
			require.Equal(t, "0.6", invocation.Env["CANVASBENCH_TEMPERATURE"])
			require.Equal(t, "0.95", invocation.Env["CANVASBENCH_TOP_P"])
			require.Equal(t, "20", invocation.Env["CANVASBENCH_TOP_K"])
			require.Equal(t, "32768", invocation.Env["CANVASBENCH_CONTEXT_WINDOW"])
			require.Equal(t, "1024", invocation.Env["CANVASBENCH_MAX_OUTPUT_TOKENS"])
			require.Equal(t, "true", invocation.Env["CANVASBENCH_PRESERVE_THINKING"])
		})
	}
}

func TestExternalAdaptersExecuteOnlyThroughInjectedOuterBoundary(t *testing.T) {
	tests := []struct {
		kind, normalizer, fixture string
		artifact                  bool
	}{
		{AdapterOpenCode, NormalizerOpenCode, "opencode.jsonl", false},
		{AdapterQwenCode, NormalizerQwenCode, "qwen-code.jsonl", false},
		{AdapterMiniSWE, NormalizerMiniSWE, "mini-swe-agent.json", true},
		{AdapterPi, NormalizerPi, "pi.jsonl", false},
		{AdapterAider, NormalizerAider, "aider.json", false},
		{AdapterOpenHands, NormalizerOpenHands, "openhands.json", false},
		{AdapterGoose, NormalizerGoose, "goose.json", false},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			fixture := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(fixture, ".git"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(fixture, ".git", "config"), []byte("git"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(fixture, "read.cpp"), []byte("read"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(fixture, "write.cpp"), []byte("before"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(fixture, "secret.cpp"), []byte("secret"), 0o644))
			raw, err := os.ReadFile(filepath.Join("testdata", "external", test.fixture))
			require.NoError(t, err)
			result := OuterExecutionResult{Stdout: raw, StartedAt: time.Unix(1784937600, 0), Duration: time.Second, Artifacts: map[string][]byte{}}
			if test.artifact {
				result.Stdout = nil
				result.Artifacts[".canvasbench/mini-swe-agent-trajectory.json"] = raw
			}
			seen := OuterExecutionSpec{}
			adapter := externalAdapter(test.kind, test.normalizer)
			adapter.Executor = fakeOuterExecutor{result: result, seen: &seen}
			adapter.UsageAttestor = fakeUsageAttestor{usage: ServerUsage{Source: ServerUsageSourceProxy, RequestsMeasured: 2, RequestsTotal: 2, PromptTokens: 100, CompletionTokens: 20, Complete: true}}
			run, err := adapter.Run(context.Background(), adapterRequest(fixture, test.kind, test.normalizer))
			require.NoError(t, err)
			require.Equal(t, 100, run.Telemetry.TokensIn)
			require.True(t, run.Telemetry.MutationObserved)
			require.True(t, run.Telemetry.CheckpointObserved)
			require.NotEqual(t, fixture, seen.HostWorkspace)
			updated, err := os.ReadFile(filepath.Join(fixture, "write.cpp"))
			require.NoError(t, err)
			require.Equal(t, "after", string(updated))
			secret, err := os.ReadFile(filepath.Join(fixture, "secret.cpp"))
			require.NoError(t, err)
			require.Equal(t, "secret", string(secret))
		})
	}
}

func TestExternalAdapterFailsBeforeExecutionWithoutServerUsageTruth(t *testing.T) {
	adapter := externalAdapter(AdapterOpenCode, NormalizerOpenCode)
	seen := OuterExecutionSpec{}
	adapter.Executor = fakeOuterExecutor{seen: &seen}
	_, err := adapter.Run(context.Background(), adapterRequest(t.TempDir(), AdapterOpenCode, NormalizerOpenCode))
	require.ErrorContains(t, err, "server-usage attestor")
	require.Empty(t, seen.Image)
}

func TestExternalAdaptersRequireOuterIsolation(t *testing.T) {
	request := adapterRequest("/host/fixture", AdapterOpenCode, NormalizerOpenCode)
	adapter := externalAdapter(AdapterOpenCode, NormalizerOpenCode)
	adapter.Isolation = ""
	_, err := adapter.BuildInvocation(request, UsageSession{CorrelationID: "trial", BaseURL: "http://proxy/v1", APIKey: "key"})
	require.ErrorContains(t, err, "outer-container")
}

func TestExternalAdapterRejectsHarnessLikeOrUncorrelatedUsage(t *testing.T) {
	fixture := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "read.cpp"), []byte("read"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "write.cpp"), []byte("before"), 0o644))
	raw, err := os.ReadFile(filepath.Join("testdata", "external", "opencode.jsonl"))
	require.NoError(t, err)
	for _, usage := range []ServerUsage{
		{Source: "server_response", CorrelationID: "trial", RequestsMeasured: 1, RequestsTotal: 1, Complete: true},
		{Source: ServerUsageSourceProxy, CorrelationID: "other-trial", RequestsMeasured: 1, RequestsTotal: 1, Complete: true},
		{Source: ServerUsageSourceProxy, CorrelationID: "trial", RequestsMeasured: 1, RequestsTotal: 2, Complete: false},
	} {
		seen := OuterExecutionSpec{}
		adapter := externalAdapter(AdapterOpenCode, NormalizerOpenCode)
		adapter.Executor = fakeOuterExecutor{result: OuterExecutionResult{Stdout: raw, StartedAt: time.Now(), Artifacts: map[string][]byte{}}, seen: &seen}
		adapter.UsageAttestor = fakeUsageAttestor{usage: usage}
		_, runErr := adapter.Run(context.Background(), adapterRequest(fixture, AdapterOpenCode, NormalizerOpenCode))
		require.ErrorContains(t, runErr, "usage is incomplete")
	}
}
