package branchpolicy

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/gitexec"
)

type checkpointContract struct {
	RedMode                string   `json:"red_mode"`
	ExpectedMissingSymbols []string `json:"expected_missing_symbols"`
	SemanticContracts      []struct {
		Kind         string `json:"kind"`
		State        string `json:"state"`
		Signature    string `json:"signature"`
		Symbol       string `json:"symbol"`
		ActionID     string `json:"action_id"`
		Route        string `json:"route"`
		TargetAction string `json:"target_action"`
		Caller       string `json:"caller"`
		Callee       string `json:"callee"`
	} `json:"semantic_contracts"`
}

// testCheckpointRejections is a deterministic pre-review filter. It rejects
// known non-tests and requires contract symbols to occur in active added code;
// semantic quality remains the later reviewer's responsibility.
func testCheckpointRejections(ctx context.Context, req AcceptanceRequest, headRef string) ([]Rejection, error) {
	var contract checkpointContract
	if err := json.Unmarshal([]byte(req.TestContract), &contract); err != nil {
		return nil, fmt.Errorf("decode test checkpoint contract: %w", err)
	}
	baseRef := strings.TrimSpace(req.TestContractBaseRef)
	if baseRef == "" {
		baseRef = req.BaseRef
	}
	patch, err := gitexec.RunGit(ctx, req.RepoDir, "diff", "--unified=0", baseRef+".."+headRef)
	if err != nil {
		return nil, fmt.Errorf("inspect test checkpoint: %w", err)
	}
	byPath := activeAddedLinesByPath(patch)
	active := joinedAddedLines(byPath)
	lower := strings.ToLower(active)
	for _, marker := range []string{"succeed()", "expect_true(false)", "assert(false)", "todo: add test", "placeholder test"} {
		if strings.Contains(lower, marker) {
			return []Rejection{{Reason: "invalid_test_checkpoint", Status: "placeholder", Path: marker}}, nil
		}
	}
	for path, lines := range byPath {
		if strings.EqualFold(filepath.Ext(path), ".cmake") && strings.Contains(strings.ToLower(lines), "#include") {
			return []Rejection{{Reason: "invalid_test_checkpoint", Status: "language_mismatch", Path: path}}, nil
		}
	}
	for _, symbol := range activeCompileContractSymbols(contract) {
		token := callableToken(symbol)
		if token != "" && !strings.Contains(active, token) {
			return []Rejection{{Reason: "missing_active_contract_assertion", Status: contract.RedMode, Path: token}}, nil
		}
	}
	if contract.RedMode == "runtime_assertion" || contract.RedMode == "mixed" {
		for _, semantic := range contract.SemanticContracts {
			if semantic.Kind == "cpp_function" || semantic.Kind == "cpp_type" {
				continue
			}
			for _, token := range semanticTokens(semantic.Kind, semantic.ActionID, semantic.Route, semantic.TargetAction, semantic.Caller) {
				if token != "" && !hasActiveRuntimeEvidence(byPath, token) {
					return []Rejection{{Reason: "missing_active_runtime_assertion", Status: semantic.Kind, Path: token}}, nil
				}
			}
		}
	}
	return nil, nil
}

// hasActiveRuntimeEvidence keeps labels and test names from satisfying a
// runtime contract merely by mentioning its token. The deterministic gate is
// intentionally language-light, but the token must occur on an added line
// that visibly asserts, resolves, enumerates, or executes production state.
func hasActiveRuntimeEvidence(byPath map[string]string, token string) bool {
	for _, added := range byPath {
		for _, line := range strings.Split(added, "\n") {
			if !strings.Contains(line, token) {
				continue
			}
			lower := strings.ToLower(line)
			for _, signal := range []string{
				"check(", "check_false(", "require(", "require_false(",
				"expect_", "assert", "executeaction", "resolvecommand(",
				"getallactions(",
			} {
				if strings.Contains(lower, signal) {
					return true
				}
			}
		}
	}
	return false
}

// activeCompileContractSymbols returns the minimum planned C++ surface that
// must appear in executable added code. A function call is the compile-red
// anchor for a callable contract; its return type may be idiomatically
// inferred with auto and does not need to be repeated. Type-only contracts
// still require an explicit type use. Legacy contracts without typed semantic
// metadata retain the original all-symbol behavior.
func activeCompileContractSymbols(contract checkpointContract) []string {
	var functions, types []string
	for _, semantic := range contract.SemanticContracts {
		if semantic.State == "existing" {
			continue
		}
		switch semantic.Kind {
		case "cpp_function":
			if strings.TrimSpace(semantic.Signature) != "" {
				functions = append(functions, semantic.Signature)
			}
		case "cpp_type":
			if strings.TrimSpace(semantic.Symbol) != "" {
				types = append(types, semantic.Symbol)
			}
		}
	}
	if len(functions) > 0 {
		return functions
	}
	if len(types) > 0 {
		return types
	}
	return contract.ExpectedMissingSymbols
}

func activeAddedLinesByPath(patch string) map[string]string {
	builders := map[string]*strings.Builder{}
	current := ""
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				current = strings.TrimPrefix(fields[3], "b/")
			}
			continue
		}
		if !strings.HasPrefix(line, "+") || strings.HasPrefix(line, "+++") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "+"))
		if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "/*") || strings.HasPrefix(line, "*") {
			continue
		}
		if builders[current] == nil {
			builders[current] = &strings.Builder{}
		}
		builders[current].WriteString(line)
		builders[current].WriteByte('\n')
	}
	result := make(map[string]string, len(builders))
	for path, builder := range builders {
		result[path] = builder.String()
	}
	return result
}

func joinedAddedLines(byPath map[string]string) string {
	var joined strings.Builder
	for _, lines := range byPath {
		joined.WriteString(lines)
	}
	return joined.String()
}

func callableToken(signature string) string {
	name := strings.TrimSpace(strings.SplitN(signature, "(", 2)[0])
	if idx := strings.LastIndex(name, "::"); idx >= 0 {
		name = name[idx+2:]
	}
	fields := strings.Fields(name)
	if len(fields) > 0 {
		name = fields[len(fields)-1]
	}
	return strings.Trim(name, "*& ")
}

func semanticTokens(kind, actionID, route, targetAction, caller string) []string {
	switch kind {
	case "registry_action":
		return []string{actionID}
	case "keymap_route":
		return []string{route, targetAction}
	case "call_edge":
		return []string{callableToken(caller)}
	default:
		return nil
	}
}
