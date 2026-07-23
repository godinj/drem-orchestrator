package orchestrator

import (
	"encoding/json"
	"fmt"

	"github.com/godinj/drem-orchestrator/pkg/score"
)

type testInterfaceContract struct {
	Kind                   string                 `json:"kind"`
	ImplementationSubtask  string                 `json:"implementation_subtask"`
	OwningFiles            []string               `json:"owning_files"`
	ModuleBoundaries       []score.ModuleBoundary `json:"module_boundaries,omitempty"`
	Interfaces             []score.InterfaceShape `json:"interfaces,omitempty"`
	ExpectedMissingSymbols []string               `json:"expected_missing_symbols,omitempty"`
	RedState               string                 `json:"red_state"`
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
		RedState:              "Use existing production APIs. If the required seam is absent, stop with a concise missing-contract error instead of searching the repository.",
	}
	if impl.DepthMeta != nil && len(impl.DepthMeta.InterfaceShapes) > 0 {
		contract.Kind = "planned_api"
		contract.ModuleBoundaries = impl.DepthMeta.ModuleBoundaries
		contract.Interfaces = impl.DepthMeta.InterfaceShapes
		contract.RedState = "Call these planned interfaces exactly. They are expected to be absent before implementation; a compile failure naming one of the listed symbols is the intended red artifact. Do not search for the symbols or invent another API."
		for _, shape := range impl.DepthMeta.InterfaceShapes {
			contract.ExpectedMissingSymbols = append(contract.ExpectedMissingSymbols, shape.Types...)
			contract.ExpectedMissingSymbols = append(contract.ExpectedMissingSymbols, shape.Functions...)
		}
	}
	raw, err := json.MarshalIndent(contract, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode contract: %w", err)
	}
	return string(raw), nil
}
