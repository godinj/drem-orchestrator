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
	Executable   string            `json:"executable"`
	Args         []string          `json:"args"`
	Env          map[string]string `json:"env"`
	SensitiveEnv map[string]string `json:"-"`
	WorkDir      string            `json:"work_dir"`
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

func (adapter ExternalCLIAdapter) BuildInvocation(request TrialRequest, usage UsageSession) (CommandInvocation, error) {
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
	inferenceEnv, sensitiveEnv, err := inferenceEnvironment(adapter.Kind, request.Harness.InferenceEnvContract, usage)
	if err != nil {
		return CommandInvocation{}, err
	}
	prompt := strings.TrimSpace(request.Task.SystemPrompt + "\n\n" + request.Task.UserMessage)
	trajectory := filepath.ToSlash(filepath.Join(outerWorkspace, ".canvasbench", adapter.Kind+"-trajectory.json"))
	invocation := CommandInvocation{
		Executable: adapter.Executable, WorkDir: outerWorkspace,
		SensitiveEnv: sensitiveEnv,
		Env: map[string]string{
			"CANVASBENCH_SEED":              fmt.Sprint(request.Seed),
			"CANVASBENCH_TEMPERATURE":       fmt.Sprint(request.Temperature),
			"CANVASBENCH_TOP_P":             fmt.Sprint(request.TopP),
			"CANVASBENCH_TOP_K":             fmt.Sprint(request.TopK),
			"CANVASBENCH_CONTEXT_WINDOW":    fmt.Sprint(request.ContextWindow),
			"CANVASBENCH_MAX_OUTPUT_TOKENS": fmt.Sprint(request.Task.Budget.MaxOutputTokens),
			"CANVASBENCH_PRESERVE_THINKING": fmt.Sprint(request.PreserveThinking),
		},
	}
	for key, value := range inferenceEnv {
		invocation.Env[key] = value
	}
	switch adapter.Kind {
	case AdapterOpenCode:
		invocation.Args = []string{"run", "--pure", "--auto", "--format", "json", "--agent", "build", "--dir", outerWorkspace, "--model", request.Harness.AdapterModelRef, prompt}
	case AdapterQwenCode:
		invocation.Args = []string{"--prompt", prompt, "--output-format", "stream-json", "--system-prompt", request.Task.SystemPrompt,
			"--model", request.Harness.AdapterModelRef, "--auth-type", "openai", "--safe-mode", "--yolo", "--max-tool-calls", fmt.Sprint(request.Task.Budget.MaxToolCalls),
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

func (adapter ExternalCLIAdapter) BuildOuterSpec(request TrialRequest, usage UsageSession) (OuterExecutionSpec, error) {
	invocation, err := adapter.BuildInvocation(request, usage)
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
	if _, err := validateOuterSpec(spec); err != nil {
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
	usageSession, err := adapter.UsageAttestor.StartServerUsage(ctx, request)
	if err != nil {
		return HarnessRun{}, fmt.Errorf("start independent server usage: %w", err)
	}
	spec, err := adapter.BuildOuterSpec(scopedRequest, usageSession)
	if err != nil {
		freezeUsageLedger(ctx, adapter.UsageAttestor, usageSession)
		return HarnessRun{}, err
	}
	execution, execErr := adapter.Executor.Execute(ctx, spec)
	// The trusted proxy may still be draining a final server usage frame after
	// an external harness closes its stream at a local budget boundary.
	attestCtx, cancelAttest := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	usage, usageErr := adapter.UsageAttestor.AttestServerUsage(attestCtx, usageSession)
	cancelAttest()
	if usageErr != nil {
		if execErr != nil {
			return HarnessRun{Output: string(execution.Stdout)}, fmt.Errorf("independent server usage failed: %w (outer execution also failed: %v)", usageErr, execErr)
		}
		if execution.ExitCode != 0 {
			return HarnessRun{Output: string(execution.Stdout)}, fmt.Errorf(
				"independent server usage failed: %w (outer harness exited %d: %s)",
				usageErr, execution.ExitCode, strings.TrimSpace(string(execution.Stderr)),
			)
		}
		return HarnessRun{Output: string(execution.Stdout)}, fmt.Errorf("independent server usage failed: %w", usageErr)
	}
	if err := validateServerUsage(usage, usageSession.CorrelationID); err != nil {
		return HarnessRun{Output: string(execution.Stdout)}, fmt.Errorf("independent server usage is incomplete: %w", err)
	}
	if scopeErr := workspace.Validate(); scopeErr != nil {
		run := HarnessRun{Output: string(execution.Stdout), StopReason: "scope_violation"}
		applyTrustedUsage(&run, usage)
		return run, scopeErr
	}
	mutationObserved, mutationErr := workspace.MutationObserved()
	if mutationErr != nil {
		return HarnessRun{Output: string(execution.Stdout)}, fmt.Errorf("inspect scoped mutation: %w", mutationErr)
	}
	run, normalizeErr := NormalizeExternal(adapter.Kind, adapter.Normalizer, request, execution)
	applyTrustedUsage(&run, usage)
	run.Telemetry.MutationObserved = mutationObserved
	run.Telemetry.CheckpointObserved = mutationObserved
	if usage.RequestsRejected > 0 {
		run.StopReason = "upstream_rejected"
		return run, fmt.Errorf("upstream rejected %d harness request(s) before inference", usage.RequestsRejected)
	}
	if normalizeErr != nil {
		return run, normalizeErr
	}
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

func applyTrustedUsage(run *HarnessRun, usage ServerUsage) {
	run.ServerUsage = usage
	run.Telemetry.TokensIn = usage.PromptTokens
	run.Telemetry.TokensOut = usage.CompletionTokens
	run.Trajectory.FinalMetrics.PromptTokens = usage.PromptTokens
	run.Trajectory.FinalMetrics.CompletionTokens = usage.CompletionTokens
}

func freezeUsageLedger(ctx context.Context, attestor ServerUsageAttestor, session UsageSession) {
	freezeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = attestor.AttestServerUsage(freezeCtx, session)
}

func inferenceEnvContractForAdapter(kind string) string {
	if kind == AdapterMiniSWE {
		return "openai_api_base_api_key.v1"
	}
	switch kind {
	case AdapterOpenCode, AdapterQwenCode, AdapterPi:
		return "openai_base_url_api_key.v1"
	default:
		return ""
	}
}

func inferenceEnvironment(kind, contract string, usage UsageSession) (map[string]string, map[string]string, error) {
	if usage.CorrelationID == "" || usage.BaseURL == "" || usage.APIKey == "" {
		return nil, nil, fmt.Errorf("per-trial usage proxy credential is incomplete")
	}
	if contract == "" || contract != inferenceEnvContractForAdapter(kind) {
		return nil, nil, fmt.Errorf("external adapter inference environment contract is missing or mismatched")
	}
	environment := map[string]string{}
	if kind == AdapterMiniSWE {
		environment["OPENAI_API_BASE"] = strings.TrimRight(usage.BaseURL, "/")
	} else {
		environment["OPENAI_BASE_URL"] = strings.TrimRight(usage.BaseURL, "/")
	}
	return environment, map[string]string{"OPENAI_API_KEY": usage.APIKey}, nil
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
