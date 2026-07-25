package benchv2

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type BuiltinVerifier struct {
	OracleRoot string
	NativeGate func(context.Context, string) (string, error)
}

func (verifier BuiltinVerifier) Verify(ctx context.Context, task TaskSpec, workDir string, run HarnessRun) VerifyOutcome {
	switch task.OracleID {
	case "api-contract-grounding-v1":
		return verifyAPIContract(workDir, task.ResultArtifact)
	case "lower-zone-clamp-v1":
		return verifier.verifyLowerZone(ctx, workDir)
	case "keymap-take-cycling-v1":
		return verifyKeymap(workDir)
	case "transient-registration-production-v1":
		return verifyAudioProcessRegistration(workDir)
	case "take-cycling-red-tests-canonical-v1", "take-cycling-implementation-canonical-v1", "take-cycling-bad-artifact-861eebff-v1":
		return verifier.verifyTakeCycling(ctx, task, workDir)
	case "ownership-rework-99894b1a-v1":
		if run.StopReason != "deterministic" || !run.Telemetry.CheckpointObserved {
			return VerifyOutcome{Failures: []string{"production ownership replay did not pass"}}
		}
		return VerifyOutcome{Passed: true, Compiled: true}
	default:
		return VerifyOutcome{Failures: []string{"unknown or unavailable hidden oracle"}}
	}
}

func verifyAPIContract(workDir, artifact string) VerifyOutcome {
	raw, err := os.ReadFile(filepath.Join(workDir, artifact))
	if err != nil {
		return VerifyOutcome{Failures: []string{err.Error()}}
	}
	var answer struct {
		Symbols  []string `json:"symbols"`
		Rejected []string `json:"rejected"`
	}
	if err := DecodeStrict(raw, &answer); err != nil {
		return VerifyOutcome{Failures: []string{"result schema: " + err.Error()}}
	}
	required := []string{"Arrangement::selectTrack", "VimContext::setAutomationLaneIndex", "ActionRegistry::getAllActions", "ActionRegistry::executeAction", "VimContext::Panel", "KeymapRegistry::resolve(VimMode,VimContext::Panel,const KeySequence&)"}
	forbidden := []string{"Arrangement::setSelectedTrackIndex", "VimContext::setInAutomationLane", "ActionRegistry::getAction", "dc::Panel", "KeymapRegistry::resolve(VimMode,const KeySequence&)"}
	if !containsAll(answer.Symbols, required) || !containsAll(answer.Rejected, forbidden) {
		return VerifyOutcome{Compiled: true, Failures: []string{"exact API contract answer is incomplete"}}
	}
	return VerifyOutcome{Passed: true, Compiled: true}
}

func (verifier BuiltinVerifier) verifyLowerZone(ctx context.Context, workDir string) VerifyOutcome {
	source := filepath.Join(verifier.OracleRoot, "lower_zone_state_verify.cpp")
	if _, err := os.Stat(source); err != nil {
		return VerifyOutcome{Failures: []string{"hidden lower-zone oracle unavailable"}}
	}
	binDir, err := os.MkdirTemp("", "canvasbench-oracle-")
	if err != nil {
		return VerifyOutcome{Failures: []string{err.Error()}}
	}
	defer os.RemoveAll(binDir)
	bin := filepath.Join(binDir, "verify")
	cmd := exec.CommandContext(ctx, "c++", "-std=c++17", "-Wall", "-Wextra", "-Werror", "-I", workDir, source, "-o", bin)
	if out, err := cmd.CombinedOutput(); err != nil {
		return VerifyOutcome{Failures: []string{fmt.Sprintf("compile: %v: %s", err, out)}}
	}
	cmd = exec.CommandContext(ctx, bin)
	if out, err := cmd.CombinedOutput(); err != nil {
		return VerifyOutcome{Compiled: true, Failures: []string{fmt.Sprintf("semantic oracle: %v: %s", err, out)}}
	}
	return VerifyOutcome{Passed: true, Compiled: true}
}

func verifyKeymap(workDir string) VerifyOutcome {
	raw, err := os.ReadFile(filepath.Join(workDir, "config/default_keymap.yaml"))
	if err != nil {
		return VerifyOutcome{Failures: []string{err.Error()}}
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return VerifyOutcome{Failures: []string{"YAML parse: " + err.Error()}}
	}
	flat := flattenYAML(doc)
	want := map[string]string{"Alt+h": "edit.slip_left", "Alt+j": "take.next", "Alt+k": "take.prev", "Alt+l": "edit.slip_right"}
	for key, action := range want {
		if flat[key] != action {
			return VerifyOutcome{Compiled: true, Failures: []string{fmt.Sprintf("production keymap route %s -> %s missing", key, action)}}
		}
	}
	return VerifyOutcome{Passed: true, Compiled: true}
}

func flattenYAML(value any) map[string]string {
	result := map[string]string{}
	var visit func(any)
	visit = func(current any) {
		switch node := current.(type) {
		case map[string]any:
			for key, child := range node {
				if text, ok := child.(string); ok && strings.HasPrefix(key, "Alt+") {
					result[key] = text
				}
				visit(child)
			}
		case []any:
			for _, child := range node {
				visit(child)
			}
		}
	}
	visit(value)
	return result
}

func containsAll(actual, expected []string) bool {
	set := map[string]bool{}
	for _, item := range actual {
		set[item] = true
	}
	for _, item := range expected {
		if !set[item] {
			return false
		}
	}
	return true
}

func MarshalResult(result TrialResult) []byte {
	raw, _ := json.Marshal(result)
	return raw
}
