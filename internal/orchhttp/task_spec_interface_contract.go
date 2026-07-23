package orchhttp

import (
	"fmt"
	"strings"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

func validateImplementationDepth(index int, sub orchdto.TaskExecutionSubtaskDTO, scope map[string]struct{}) error {
	if len(sub.ModuleBoundaries) == 0 {
		return fmt.Errorf("implementation subtask %d requires module_boundaries", index)
	}
	if len(sub.InterfaceShapes) == 0 && len(sub.InterfaceContracts) == 0 {
		return fmt.Errorf("implementation subtask %d requires interface_shapes or interface_contracts", index)
	}
	boundaries := make(map[string]struct{}, len(sub.ModuleBoundaries))
	for i, boundary := range sub.ModuleBoundaries {
		if boundary.Package == "" || boundary.Description == "" || boundary.Exports < 1 {
			return fmt.Errorf("subtasks[%d].module_boundaries[%d] requires package, description, and exports >= 1", index, i)
		}
		if _, exists := boundaries[boundary.Package]; exists {
			return fmt.Errorf("subtasks[%d] module boundary %q is duplicated", index, boundary.Package)
		}
		boundaries[boundary.Package] = struct{}{}
	}
	shapes := make(map[string]struct{}, len(sub.InterfaceShapes))
	for i, shape := range sub.InterfaceShapes {
		if shape.Package == "" || (len(shape.Functions) == 0 && len(shape.Types) == 0) || hasBlank(shape.Functions) || hasBlank(shape.Types) {
			return fmt.Errorf("subtasks[%d].interface_shapes[%d] requires package and at least one non-blank function or type", index, i)
		}
		if _, exists := boundaries[shape.Package]; !exists {
			return fmt.Errorf("subtasks[%d] interface shape %q has no matching module boundary", index, shape.Package)
		}
		if _, exists := shapes[shape.Package]; exists {
			return fmt.Errorf("subtasks[%d] interface shape %q is duplicated", index, shape.Package)
		}
		for j, function := range shape.Functions {
			open := strings.Index(function, "(")
			if open < 1 || !strings.Contains(function[open:], ")") || strings.Contains(function[:open], ".") {
				return fmt.Errorf("subtasks[%d].interface_shapes[%d].functions[%d] must be an explicit callable signature", index, i, j)
			}
		}
		shapes[shape.Package] = struct{}{}
	}
	contractPackages := make(map[string]struct{})
	for i, contract := range sub.InterfaceContracts {
		if err := validateInterfaceContract(index, i, contract, boundaries, scope); err != nil {
			return err
		}
		contractPackages[contract.Package] = struct{}{}
	}
	for pkg := range boundaries {
		_, hasShape := shapes[pkg]
		_, hasContract := contractPackages[pkg]
		if !hasShape && !hasContract {
			return fmt.Errorf("subtasks[%d] module boundary %q has no matching interface shape or semantic contract", index, pkg)
		}
	}
	return nil
}

func validateInterfaceContract(index, contractIndex int, contract orchdto.TaskInterfaceContractDTO, boundaries, scope map[string]struct{}) error {
	prefix := fmt.Sprintf("subtasks[%d].interface_contracts[%d]", index, contractIndex)
	if _, ok := boundaries[contract.Package]; !ok {
		return fmt.Errorf("%s package %q has no matching module boundary", prefix, contract.Package)
	}
	if _, ok := scope[contract.OwnerFile]; !ok {
		return fmt.Errorf("%s owner_file %q is outside proposed_scope", prefix, contract.OwnerFile)
	}
	if contract.State != "existing" && contract.State != "planned" && contract.State != "missing" {
		return fmt.Errorf("%s state must be existing, planned, or missing", prefix)
	}
	requireCallable := func(value, field string) error {
		open := strings.Index(value, "(")
		if open < 1 || !strings.Contains(value[open:], ")") || strings.Contains(value[:open], ".") {
			return fmt.Errorf("%s.%s must be an explicit C++ callable signature", prefix, field)
		}
		return nil
	}
	switch contract.Kind {
	case "cpp_function":
		return requireCallable(contract.Signature, "signature")
	case "cpp_type":
		if contract.Symbol == "" || strings.ContainsAny(contract.Symbol, "()") {
			return fmt.Errorf("%s.symbol must name a C++ type", prefix)
		}
	case "registry_action":
		if contract.ActionID == "" {
			return fmt.Errorf("%s.action_id is required", prefix)
		}
		if err := requireCallable(contract.CallbackSignature, "callback_signature"); err != nil {
			return err
		}
	case "keymap_route":
		if contract.Route == "" || contract.TargetAction == "" {
			return fmt.Errorf("%s requires route and target_action", prefix)
		}
	case "call_edge":
		if err := requireCallable(contract.Caller, "caller"); err != nil {
			return err
		}
		if err := requireCallable(contract.Callee, "callee"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%s kind must be cpp_function, cpp_type, registry_action, keymap_route, or call_edge", prefix)
	}
	return nil
}
