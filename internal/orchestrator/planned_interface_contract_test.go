package orchestrator

import (
	"testing"

	"github.com/godinj/drem-orchestrator/pkg/score"
	"github.com/stretchr/testify/require"
)

func TestPlannedInterfaceContractUsesImplementationAPIWithoutRepositoryInference(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Write transient slicing regression", Phase: "test", TestsFor: []int{1}},
		{
			Title: "Implement transient slicing", Phase: "implementation",
			Files: []string{"src/model/AudioClip.h", "src/model/AudioClipTransientSlicing.cpp"},
			DepthMeta: &score.DepthMeta{
				ModuleBoundaries: []score.ModuleBoundary{{Package: "src/model", Description: "audio transient slicing", Exports: 2}},
				InterfaceShapes: []score.InterfaceShape{{
					Package:   "src/model",
					Types:     []string{"AudioClip::TransientSlicingSettings"},
					Functions: []string{"AudioClip::divideAtTransients(const TransientSlicingSettings&)"},
				}},
			},
		},
	}

	contract, err := plannedInterfaceContract(subtasks, 0)
	require.NoError(t, err)
	require.Contains(t, contract, `"kind": "planned_api"`)
	require.Contains(t, contract, "AudioClip::TransientSlicingSettings")
	require.Contains(t, contract, "AudioClip::divideAtTransients")
	require.Contains(t, contract, "src/model/AudioClip.h")
	require.Contains(t, contract, "Do not search for the symbols")
}

func TestPlannedInterfaceContractSeparatesRuntimeAndCompileRed(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Write registration regression", Phase: "test", TestsFor: []int{1}},
		{Title: "Implement action", Phase: "implementation", Files: []string{"src/ui/ActionAudio.cpp"}, DepthMeta: &score.DepthMeta{
			ModuleBoundaries: []score.ModuleBoundary{{Package: "src/ui", Description: "audio action", Exports: 1}},
			InterfaceContracts: []score.InterfaceContract{
				{Package: "src/ui", Kind: "cpp_function", State: "planned", OwnerFile: "src/ui/ActionAudio.cpp", Signature: "AudioClip::divideAtTransients(const Settings&)"},
				{Package: "src/ui", Kind: "registry_action", State: "planned", OwnerFile: "src/ui/ActionAudio.cpp", ActionID: "audio.divide-transients", CallbackSignature: "ActionCoordinator::registerAudioProcessActions()"},
			},
		}},
	}

	contract, err := plannedInterfaceContract(subtasks, 0)
	require.NoError(t, err)
	require.Contains(t, contract, `"red_mode": "mixed"`)
	require.Contains(t, contract, `"action_id": "audio.divide-transients"`)
	require.NotContains(t, contract, `"expected_missing_symbols": [\n    "audio.divide-transients"`)
}

func TestParsePlanPreservesAdapterInterfaceShapes(t *testing.T) {
	parsed, err := parsePlan(map[string]any{"subtasks": []any{
		map[string]any{
			"title": "impl", "description": "d", "phase": "implementation", "files": []any{"src/model/AudioClip.h"},
			"module_boundaries": []any{map[string]any{"package": "src/model", "description": "slice", "exports": float64(2)}},
			"interface_shapes":  []any{map[string]any{"package": "src/model", "types": []any{"Settings"}, "functions": []any{"divide()"}}},
		},
	}})
	require.NoError(t, err)
	require.NotNil(t, parsed.Subtasks[0].DepthMeta)
	require.Equal(t, []string{"divide()"}, parsed.Subtasks[0].DepthMeta.InterfaceShapes[0].Functions)
}
