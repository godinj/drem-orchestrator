package benchv2

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	AdapterOpenCode = "opencode"
	AdapterQwenCode = "qwen-code"
	AdapterMiniSWE  = "mini-swe-agent"
	AdapterPi       = "pi"

	NormalizerOpenCode = "opencode-json-v1"
	NormalizerQwenCode = "qwen-code-stream-json-v1"
	NormalizerMiniSWE  = "mini-swe-agent-1.1"
	NormalizerPi       = "pi-json-v3"
)

type CommandInvocation struct {
	Executable string            `json:"executable"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env"`
	WorkDir    string            `json:"work_dir"`
}

// ServerUsageAttestor obtains usage from an independent inference-server
// truth source. Harness stdout/stderr is deliberately absent from this API so
// a CLI-reported token count cannot masquerade as server telemetry.
type ServerUsageAttestor interface {
	AttestServerUsage(context.Context, TrialRequest) (ServerUsage, error)
}

type ExternalCLIAdapter struct {
	Kind          string
	Executable    string
	Version       string
	Isolation     string
	Image         string
	Network       OuterNetworkPolicy
	Normalizer    string
	Executor      OuterExecutor
	UsageAttestor ServerUsageAttestor
}

func (adapter ExternalCLIAdapter) Name() string { return adapter.Kind }

func (adapter ExternalCLIAdapter) BuildInvocation(request TrialRequest) (CommandInvocation, error) {
	if adapter.Executable == "" || adapter.Version == "" {
		return CommandInvocation{}, fmt.Errorf("external adapter executable/version must be attested")
	}
	if adapter.Isolation != "outer_container" || request.Harness.OuterIsolation != "outer_container" {
		return CommandInvocation{}, fmt.Errorf("external CLI adapters require benchmark outer-container isolation")
	}
	if request.Harness.AdapterModelRef == "" {
		return CommandInvocation{}, fmt.Errorf("adapter-specific model reference is required")
	}
	if request.Harness.ToolPolicy != ToolPolicySandboxed || !taskAllowsToolPolicy(request.Task, ToolPolicySandboxed) {
		return CommandInvocation{}, fmt.Errorf("external CLI adapters require an allowed %s tool policy", ToolPolicySandboxed)
	}
	if adapter.Normalizer != normalizerForAdapter(adapter.Kind) || request.Harness.TrajectoryNormalizer != adapter.Normalizer {
		return CommandInvocation{}, fmt.Errorf("external adapter normalizer is missing or mismatched")
	}
	prompt := strings.TrimSpace(request.Task.SystemPrompt + "\n\n" + request.Task.UserMessage)
	trajectory := filepath.ToSlash(filepath.Join(outerWorkspace, ".canvasbench", adapter.Kind+"-trajectory.json"))
	invocation := CommandInvocation{
		Executable: adapter.Executable, WorkDir: outerWorkspace,
		Env: map[string]string{
			"CANVASBENCH_SEED": fmt.Sprint(request.Seed), "CANVASBENCH_TEMPERATURE": fmt.Sprint(request.Temperature),
		},
	}
	switch adapter.Kind {
	case AdapterOpenCode:
		invocation.Args = []string{"run", "--pure", "--auto", "--format", "json", "--agent", "build", "--dir", outerWorkspace, "--model", request.Harness.AdapterModelRef, prompt}
	case AdapterQwenCode:
		invocation.Args = []string{"--prompt", prompt, "--output-format", "stream-json", "--system-prompt", request.Task.SystemPrompt,
			"--model", request.Harness.AdapterModelRef, "--safe-mode", "--yolo", "--max-tool-calls", fmt.Sprint(request.Task.Budget.MaxToolCalls),
			"--max-session-turns", fmt.Sprint(request.Task.Budget.MaxIterations), "--max-wall-time", fmt.Sprintf("%ds", request.Task.Budget.TimeoutSeconds),
			"--exclude-tools", "agent"}
	case AdapterMiniSWE:
		invocation.Args = []string{"-t", prompt, "-m", request.Harness.AdapterModelRef, "-y", "--exit-immediately", "-o", trajectory}
	case AdapterPi:
		invocation.Args = []string{"--mode", "json", "--no-session", "--no-context-files", "--model", request.Harness.AdapterModelRef, "--system-prompt", request.Task.SystemPrompt, prompt}
	default:
		return CommandInvocation{}, fmt.Errorf("unsupported external adapter %q", adapter.Kind)
	}
	return invocation, nil
}

func (adapter ExternalCLIAdapter) BuildOuterSpec(request TrialRequest) (OuterExecutionSpec, error) {
	invocation, err := adapter.BuildInvocation(request)
	if err != nil {
		return OuterExecutionSpec{}, err
	}
	spec := OuterExecutionSpec{
		Image: adapter.Image, HostWorkspace: request.WorkDir, ContainerWorkspace: outerWorkspace,
		ReadPaths: request.Task.ReadPaths, WritePaths: append([]string(nil), request.Task.WritePaths...),
		Network: adapter.Network, Timeout: time.Duration(request.Task.Budget.TimeoutSeconds) * time.Second,
		Invocation: invocation,
	}
	if request.Task.ResultArtifact != "" {
		spec.WritePaths = append(spec.WritePaths, request.Task.ResultArtifact)
	}
	if adapter.Kind == AdapterMiniSWE {
		spec.CaptureRelativePath = filepath.ToSlash(filepath.Join(".canvasbench", adapter.Kind+"-trajectory.json"))
	}
	if _, err := DockerCommand(spec); err != nil {
		return OuterExecutionSpec{}, err
	}
	return spec, nil
}

func (adapter ExternalCLIAdapter) Run(ctx context.Context, request TrialRequest) (HarnessRun, error) {
	if adapter.Executor == nil {
		return HarnessRun{}, fmt.Errorf("outer executor is required; host execution is forbidden")
	}
	if adapter.UsageAttestor == nil {
		return HarnessRun{}, fmt.Errorf("independent server-usage attestor is required before external execution")
	}
	writePaths := append([]string(nil), request.Task.WritePaths...)
	if request.Task.ResultArtifact != "" {
		writePaths = append(writePaths, request.Task.ResultArtifact)
	}
	var internalPaths []string
	if adapter.Kind == AdapterMiniSWE {
		internalPaths = []string{filepath.ToSlash(filepath.Join(".canvasbench", adapter.Kind+"-trajectory.json"))}
	}
	workspace, err := PrepareScopedAgentWorkspace(request.WorkDir, request.Task.ReadPaths, writePaths, internalPaths)
	if err != nil {
		return HarnessRun{}, err
	}
	defer workspace.Cleanup()
	scopedRequest := request
	scopedRequest.WorkDir = workspace.WorkDir
	spec, err := adapter.BuildOuterSpec(scopedRequest)
	if err != nil {
		return HarnessRun{}, err
	}
	execution, execErr := adapter.Executor.Execute(ctx, spec)
	if scopeErr := workspace.Validate(); scopeErr != nil {
		return HarnessRun{Output: string(execution.Stdout), StopReason: "scope_violation"}, scopeErr
	}
	run, normalizeErr := NormalizeExternal(adapter.Kind, adapter.Normalizer, request, execution)
	if normalizeErr != nil {
		return run, normalizeErr
	}
	usage, usageErr := adapter.UsageAttestor.AttestServerUsage(ctx, request)
	if usageErr != nil {
		return run, fmt.Errorf("independent server usage failed: %w", usageErr)
	}
	if usage.Source != "server_response" || !usage.Complete || usage.RequestsMeasured <= 0 || usage.RequestsMeasured != usage.RequestsTotal {
		return run, fmt.Errorf("independent server usage is incomplete")
	}
	run.ServerUsage = usage
	run.Telemetry.TokensIn = usage.PromptTokens
	run.Telemetry.TokensOut = usage.CompletionTokens
	run.Trajectory.FinalMetrics.PromptTokens = usage.PromptTokens
	run.Trajectory.FinalMetrics.CompletionTokens = usage.CompletionTokens
	if err := ValidateATIF(run.Trajectory); err != nil {
		return run, err
	}
	if execErr != nil {
		return run, execErr
	}
	if execution.ExitCode != 0 {
		return run, fmt.Errorf("outer harness exited %d: %s", execution.ExitCode, strings.TrimSpace(string(execution.Stderr)))
	}
	if err := workspace.Apply(); err != nil {
		return run, fmt.Errorf("apply scoped outputs: %w", err)
	}
	return run, nil
}

func normalizerForAdapter(kind string) string {
	switch kind {
	case AdapterOpenCode:
		return NormalizerOpenCode
	case AdapterQwenCode:
		return NormalizerQwenCode
	case AdapterMiniSWE:
		return NormalizerMiniSWE
	case AdapterPi:
		return NormalizerPi
	default:
		return ""
	}
}
