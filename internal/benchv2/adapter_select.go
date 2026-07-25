package benchv2

import "fmt"

const AdapterDirect = "direct_tool_agent"

// SelectAdapter binds the declared harness identity to one concrete adapter.
// It never falls back to DirectToolAgent for an unknown or external harness.
func SelectAdapter(harness HarnessConfig, task TaskSpec, endpoint string, usageAttestor ServerUsageAttestor) (HarnessAdapter, error) {
	if task.Mode == "deterministic_replay" {
		if !taskAllowsToolPolicy(task, ToolPolicyReplay) {
			return nil, fmt.Errorf("deterministic replay policy is not allowed")
		}
		return ReplayAdapter{}, nil
	}
	switch harness.Name {
	case AdapterDirect:
		if harness.ToolPolicy != ToolPolicyStructured || !taskAllowsToolPolicy(task, ToolPolicyStructured) {
			return nil, fmt.Errorf("direct harness requires allowed %s policy", ToolPolicyStructured)
		}
		return DirectToolAdapter{Endpoint: endpoint}, nil
	case AdapterOpenCode, AdapterQwenCode, AdapterMiniSWE, AdapterPi, AdapterAider, AdapterOpenHands, AdapterGoose, AdapterCline, AdapterContinue, AdapterSWEAgent:
		if usageAttestor == nil {
			return nil, fmt.Errorf("external harness requires a trusted usage proxy attestor")
		}
		if harness.ToolPolicy != ToolPolicySandboxed || !taskAllowsToolPolicy(task, ToolPolicySandboxed) {
			return nil, fmt.Errorf("external harness requires allowed %s policy", ToolPolicySandboxed)
		}
		if !pinnedOCIImage.MatchString(harness.OuterImage) || harness.OuterNetworkPolicy != OuterNetworkIsolatedInference || harness.OuterNetworkName == "" {
			return nil, fmt.Errorf("external harness requires pinned outer image and isolated inference network")
		}
		if harness.TrajectoryNormalizer != normalizerForAdapter(harness.Name) {
			return nil, fmt.Errorf("external harness normalizer is missing or mismatched")
		}
		return ExternalCLIAdapter{
			Kind: harness.Name, Executable: externalExecutable(harness.Name), Version: harness.Version,
			Isolation: harness.OuterIsolation, Image: harness.OuterImage,
			Network:    OuterNetworkPolicy{Mode: harness.OuterNetworkPolicy, NetworkName: harness.OuterNetworkName},
			Normalizer: harness.TrajectoryNormalizer, Executor: DockerOuterExecutor{}, UsageAttestor: usageAttestor,
		}, nil
	default:
		return nil, fmt.Errorf("unknown CanvasBench harness %q", harness.Name)
	}
}

func externalExecutable(adapter string) string {
	switch adapter {
	case AdapterQwenCode:
		return "qwen"
	case AdapterMiniSWE:
		return "mini"
	case AdapterContinue:
		return "cn"
	default:
		return adapter
	}
}
