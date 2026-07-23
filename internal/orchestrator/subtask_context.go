package orchestrator

import (
	"fmt"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func materializedSubtaskContext(sp planEntry, plans []planEntry, index int, sourcePacks []string) (model.JSONField, error) {
	ctx := model.JSONField{"agent_type": sp.AgentType, "estimated_files": sp.EstimatedFiles}
	if sp.Phase != "" {
		ctx["phase"] = sp.Phase
	}
	if len(sourcePacks) == len(plans) && sourcePacks[index] != "" {
		ctx["verified_source_pack"] = sourcePacks[index]
	}
	if sp.Phase == "test" && len(sp.TestsFor) == 1 {
		contract, err := plannedInterfaceContract(plans, index)
		if err != nil {
			return nil, fmt.Errorf("test contract %d: %w", index, err)
		}
		ctx["planned_interface_contract"] = contract
	}
	if sp.Phase == "implementation" {
		contract, err := implementationInterfaceContract(sp)
		if err != nil {
			return nil, fmt.Errorf("implementation contract %d: %w", index, err)
		}
		if contract != "" {
			ctx["implementation_interface_contract"] = contract
		}
	}
	return ctx, nil
}
