package benchv2

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	AdapterOpenCode = "opencode"
	AdapterQwenCode = "qwen-code"
	AdapterMiniSWE  = "mini-swe-agent"
	AdapterPi       = "pi"
)

type CommandInvocation struct {
	Executable string            `json:"executable"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env"`
	WorkDir    string            `json:"work_dir"`
	StdoutPath string            `json:"stdout_path"`
}

type ExternalCLIAdapter struct {
	Kind       string
	Executable string
	Version    string
	DryRun     bool
	Isolation  string
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
	prompt := strings.TrimSpace(request.Task.SystemPrompt + "\n\n" + request.Task.UserMessage)
	trajectory := filepath.Join(request.WorkDir, ".canvasbench", adapter.Kind+"-trajectory.json")
	invocation := CommandInvocation{
		Executable: adapter.Executable, WorkDir: request.WorkDir, StdoutPath: trajectory,
		Env: map[string]string{
			"CANVASBENCH_SEED": fmt.Sprint(request.Seed), "CANVASBENCH_TEMPERATURE": fmt.Sprint(request.Temperature),
			"CANVASBENCH_READ_PATHS":  strings.Join(request.Task.ReadPaths, ":"),
			"CANVASBENCH_WRITE_PATHS": strings.Join(request.Task.WritePaths, ":"),
		},
	}
	switch adapter.Kind {
	case AdapterOpenCode:
		invocation.Args = []string{"run", "--pure", "--auto", "--format", "json", "--agent", "build", "--dir", request.WorkDir, "--model", request.Harness.AdapterModelRef, prompt}
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

func (adapter ExternalCLIAdapter) Run(_ context.Context, request TrialRequest) (HarnessRun, error) {
	invocation, err := adapter.BuildInvocation(request)
	if err != nil {
		return HarnessRun{}, err
	}
	if !adapter.DryRun {
		return HarnessRun{}, fmt.Errorf("external adapter execution is disabled until its filesystem scope wrapper and ATIF normalizer are installed")
	}
	raw, _ := json.Marshal(invocation)
	return HarnessRun{Output: string(raw), StopReason: "dry_run"}, fmt.Errorf("dry-run adapter produced no inference and cannot be scored")
}

func WriteInvocation(path string, invocation CommandInvocation) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(invocation, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}
