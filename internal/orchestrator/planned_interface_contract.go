package orchestrator

import (
	"encoding/json"
	"fmt"

	"github.com/godinj/drem-orchestrator/pkg/score"
)

type testInterfaceContract struct {
	Kind                   string                    `json:"kind"`
	ImplementationSubtask  string                    `json:"implementation_subtask"`
	OwningFiles            []string                  `json:"owning_files"`
	ModuleBoundaries       []score.ModuleBoundary    `json:"module_boundaries,omitempty"`
	Interfaces             []score.InterfaceShape    `json:"interfaces,omitempty"`
	SemanticContracts      []score.InterfaceContract `json:"semantic_contracts,omitempty"`
	ExpectedMissingSymbols []string                  `json:"expected_missing_symbols,omitempty"`
	RedMode                string                    `json:"red_mode"`
	RedState               string                    `json:"red_state"`
}

// plannedInterfaceContract turns implementation-plan data into an actionable,
// deterministic test-worker contract. It spends no supervisor inference: the
// adapter or SGLang planner has already supplied the interface shapes.
func plannedInterfaceContract(subtasks []planEntry, testIndex int) (string, error) {
	if testIndex < 0 || testIndex >= len(subtasks) {
		return "", fmt.Errorf("test index %d is out of range", testIndex)
	}
	test := subtasks[testIndex]
	if len(test.TestsFor) != 1 {
		return "", fmt.Errorf("test subtask must reference exactly one implementation")
	}
	implIndex := test.TestsFor[0]
	if implIndex < 0 || implIndex >= len(subtasks) || subtasks[implIndex].Phase != "implementation" {
		return "", fmt.Errorf("tests_for index %d is not an implementation subtask", implIndex)
	}
	impl := subtasks[implIndex]
	contract := testInterfaceContract{
		Kind:                  "existing_api",
		ImplementationSubtask: impl.Title,
		OwningFiles:           allFiles(impl),
		RedMode:               "runtime_assertion",
		RedState:              "Use existing production APIs. If the required seam is absent, stop with a concise missing-contract error instead of searching the repository.",
	}
	if impl.DepthMeta != nil && (len(impl.DepthMeta.InterfaceShapes) > 0 || len(impl.DepthMeta.InterfaceContracts) > 0) {
		contract.Kind = "planned_api"
		contract.ModuleBoundaries = impl.DepthMeta.ModuleBoundaries
		contract.Interfaces = impl.DepthMeta.InterfaceShapes
		contract.SemanticContracts = impl.DepthMeta.InterfaceContracts
		for _, shape := range impl.DepthMeta.InterfaceShapes {
			contract.ExpectedMissingSymbols = append(contract.ExpectedMissingSymbols, shape.Types...)
			contract.ExpectedMissingSymbols = append(contract.ExpectedMissingSymbols, shape.Functions...)
		}
		runtimeRed := false
		for _, semantic := range impl.DepthMeta.InterfaceContracts {
			switch semantic.Kind {
			case "cpp_function":
				if semantic.State == "planned" || semantic.State == "missing" {
					contract.ExpectedMissingSymbols = append(contract.ExpectedMissingSymbols, semantic.Signature)
				}
			case "cpp_type":
				if semantic.State == "planned" || semantic.State == "missing" {
					contract.ExpectedMissingSymbols = append(contract.ExpectedMissingSymbols, semantic.Symbol)
				}
			default:
				runtimeRed = true
			}
		}
		switch {
		case len(contract.ExpectedMissingSymbols) > 0 && runtimeRed:
			contract.RedMode = "mixed"
		case len(contract.ExpectedMissingSymbols) > 0:
			contract.RedMode = "compile_missing_symbol"
		default:
			contract.RedMode = "runtime_assertion"
		}
		contract.RedState = redStateInstruction(contract.RedMode)
	}
	raw, err := json.MarshalIndent(contract, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode contract: %w", err)
	}
	return string(raw), nil
}

func redStateInstruction(mode string) string {
	switch mode {
	case "compile_missing_symbol":
		return "Call the planned C++ symbols exactly. A compile failure naming a listed missing symbol is the intended red artifact. Do not search for the symbols or invent another API."
	case "mixed":
		return "Use compile-red only for listed planned C++ symbols. Registry, keymap, and call-edge contracts require an active behavioral assertion once the test can compile; comments and placeholders are invalid evidence."
	default:
		return "The test must compile and fail on an active behavioral assertion through the named registry, keymap, or call edge. Comments, placeholders, and fabricated headers are invalid evidence."
	}
}

func implementationInterfaceContract(impl planEntry) (string, error) {
	if impl.DepthMeta == nil {
		return "", nil
	}
	contract := testInterfaceContract{
		Kind:                  "implementation_api",
		ImplementationSubtask: impl.Title,
		OwningFiles:           allFiles(impl),
		ModuleBoundaries:      impl.DepthMeta.ModuleBoundaries,
		Interfaces:            impl.DepthMeta.InterfaceShapes,
		SemanticContracts:     impl.DepthMeta.InterfaceContracts,
	}
	raw, err := json.MarshalIndent(contract, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode implementation contract: %w", err)
	}
	return string(raw), nil
}
