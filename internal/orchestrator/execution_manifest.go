package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/model"
)

const executionManifestVersion = 1

type executionLane string

const (
	executionLaneAtomic     executionLane = "atomic_repair"
	executionLaneDecomposed executionLane = "decomposed_dag"
)

// executionManifest is the deterministic runtime contract compiled from an
// accepted plan. Reviewers may advise on semantics, but workers execute this
// immutable shape: ownership, ordering, gates, budgets, and recovery are no
// longer inferred independently by each process.
type executionManifest struct {
	Version         int                     `json:"version"`
	TaskID          string                  `json:"task_id"`
	SpecFingerprint string                  `json:"spec_fingerprint,omitempty"`
	BaseSHA         string                  `json:"base_sha,omitempty"`
	Lane            executionLane           `json:"lane"`
	Steps           []executionManifestStep `json:"steps"`
	Gates           []string                `json:"gates"`
	Recovery        executionRecoveryPolicy `json:"recovery"`
	Hash            string                  `json:"hash"`
}

type executionManifestStep struct {
	ID                  string                        `json:"id"`
	Title               string                        `json:"title"`
	Phase               string                        `json:"phase"`
	ReadFiles           []string                      `json:"read_files"`
	WritableFiles       []string                      `json:"writable_files"`
	ContractHash        string                        `json:"contract_hash"`
	Dependencies        []string                      `json:"dependencies,omitempty"`
	DependencyArtifacts []executionDependencyArtifact `json:"dependency_artifacts,omitempty"`
	ExpectedTurns       int                           `json:"expected_turns"`
}

type executionDependencyArtifact struct {
	StepID       string `json:"step_id"`
	ContractHash string `json:"contract_hash"`
}

type executionRecoveryPolicy struct {
	ResumeCompletedTurns       bool `json:"resume_completed_turns"`
	PreserveMutationCheckpoint bool `json:"preserve_mutation_checkpoint"`
	MaxSemanticRecoveries      int  `json:"max_semantic_recoveries"`
	BlindRetries               int  `json:"blind_retries"`
}

func compileExecutionManifest(task *model.Task, plans []planEntry, specFingerprint string) (executionManifest, error) {
	if task == nil {
		return executionManifest{}, fmt.Errorf("compile execution manifest: task is required")
	}
	lane := chooseExecutionLane(plans)
	manifest := executionManifest{
		Version:         executionManifestVersion,
		TaskID:          task.ID.String(),
		SpecFingerprint: specFingerprint,
		BaseSHA:         strings.TrimSpace(task.WorktreeBaseSHA),
		Lane:            lane,
		Gates: []string{
			"scope_admission", "git_diff_check", "planned_contract", "native_verification", "computer_use",
		},
		Recovery: executionRecoveryPolicy{
			ResumeCompletedTurns:       true,
			PreserveMutationCheckpoint: true,
			MaxSemanticRecoveries:      1,
			BlindRetries:               0,
		},
	}
	for i, plan := range plans {
		deps := make([]string, 0, len(plan.Dependencies))
		artifacts := make([]executionDependencyArtifact, 0, len(plan.Dependencies))
		for _, dep := range plan.Dependencies {
			if dep < 0 || dep >= len(plans) {
				return executionManifest{}, fmt.Errorf("compile execution manifest: step %d has invalid dependency %d", i, dep)
			}
			deps = append(deps, manifestStepID(dep))
			artifacts = append(artifacts, executionDependencyArtifact{StepID: manifestStepID(dep), ContractHash: planContractHash(plans[dep])})
		}
		manifest.Steps = append(manifest.Steps, executionManifestStep{
			ID:                  manifestStepID(i),
			Title:               plan.Title,
			Phase:               plan.Phase,
			ReadFiles:           normalizedManifestFiles(allFiles(plan)),
			WritableFiles:       normalizedManifestFiles(writablePlanFiles(plan)),
			ContractHash:        planContractHash(plan),
			Dependencies:        deps,
			DependencyArtifacts: artifacts,
			ExpectedTurns:       expectedTurnsForPhase(plan.Phase),
		})
	}

	hash, err := hashExecutionManifest(manifest)
	if err != nil {
		return executionManifest{}, err
	}
	manifest.Hash = hash
	return manifest, nil
}

func planContractHash(plan planEntry) string {
	raw, _ := json.Marshal(struct {
		Title string   `json:"title"`
		Phase string   `json:"phase"`
		Read  []string `json:"read"`
		Write []string `json:"write"`
	}{plan.Title, plan.Phase, normalizedManifestFiles(allFiles(plan)), normalizedManifestFiles(writablePlanFiles(plan))})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func chooseExecutionLane(plans []planEntry) executionLane {
	// Shared writable files are a tightly coupled repair. Making multiple weak
	// model workers rediscover and successively edit the same artifact proved
	// both expensive and unreliable. One atomic owner receives the union and
	// deterministic gates validate the result afterward.
	owner := map[string]int{}
	for i, plan := range plans {
		for _, path := range normalizedManifestFiles(writablePlanFiles(plan)) {
			if prior, ok := owner[path]; ok && prior != i {
				return executionLaneAtomic
			}
			owner[path] = i
		}
	}
	return executionLaneDecomposed
}

func collapseAtomicPlan(task *model.Task, plans []planEntry) []planEntry {
	if chooseExecutionLane(plans) != executionLaneAtomic {
		return plans
	}
	var files, writable []string
	var descriptions []string
	for i, plan := range plans {
		files = append(files, allFiles(plan)...)
		writable = append(writable, writablePlanFiles(plan)...)
		descriptions = append(descriptions, fmt.Sprintf("%d. [%s] %s", i+1, plan.Phase, plan.Description))
	}
	return []planEntry{{
		Title:          "Atomic repair: " + task.Title,
		Description:    "Execute the accepted plan in order within one ownership boundary.\n" + strings.Join(descriptions, "\n"),
		AgentType:      string(model.AgentCoder),
		Files:          normalizedManifestFiles(files),
		EstimatedFiles: normalizedManifestFiles(files),
		WritableFiles:  normalizedManifestFiles(writable),
		Phase:          "implementation",
	}}
}

func manifestStepID(index int) string { return fmt.Sprintf("step-%02d", index+1) }

func expectedTurnsForPhase(phase string) int {
	switch phase {
	case "test":
		return 8
	case "integration":
		return 8
	default:
		return 10
	}
}

func normalizedManifestFiles(files []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(files))
	for _, file := range files {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		if _, ok := seen[file]; ok {
			continue
		}
		seen[file] = struct{}{}
		out = append(out, file)
	}
	sort.Strings(out)
	return out
}

func hashExecutionManifest(manifest executionManifest) (string, error) {
	manifest.Hash = ""
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode execution manifest: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func executionManifestMap(manifest executionManifest) (map[string]any, error) {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func taskExecutionLane(task *model.Task) executionLane {
	if task == nil || task.Context == nil {
		return ""
	}
	if lane, ok := task.Context["execution_lane"].(string); ok {
		return executionLane(lane)
	}
	return ""
}

func taskManifestExpectedTurns(task *model.Task) int {
	if task == nil || task.Context == nil {
		return 0
	}
	manifest, _ := task.Context["execution_manifest"].(map[string]any)
	steps, _ := manifest["steps"].([]any)
	total := 0
	for _, raw := range steps {
		step, _ := raw.(map[string]any)
		total += intFromAny(step["expected_turns"])
	}
	return total
}
