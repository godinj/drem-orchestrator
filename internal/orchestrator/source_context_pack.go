package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

type verifiedSourcePack struct {
	SpecFingerprint    string                               `json:"spec_fingerprint"`
	Subtask            string                               `json:"subtask"`
	Phase              string                               `json:"phase"`
	OwnedFiles         []string                             `json:"owned_files"`
	PairedFiles        []string                             `json:"paired_files,omitempty"`
	AcceptanceCriteria []orchdto.TaskAcceptanceCriterionDTO `json:"acceptance_criteria"`
	IntegrationSeams   []orchdto.TaskIntegrationSeamDTO     `json:"integration_seams"`
}

// verifiedSourcePacks carries the immutable, hash-checked excerpts from the
// adapter specification into every worker prompt. The child gets its exact
// ownership and paired TDD files without having to rediscover the call chain.
func verifiedSourcePacks(db *gorm.DB, task *model.Task, plans []planEntry) ([]string, error) {
	if !db.Migrator().HasTable(&model.TaskSpecification{}) {
		return nil, nil
	}
	var stored model.TaskSpecification
	if err := db.Where("task_id = ?", task.ID).First(&stored).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("load immutable task specification: %w", err)
	}
	var spec orchdto.TaskSpecDTO
	if err := json.Unmarshal([]byte(stored.SpecJSON), &spec); err != nil {
		return nil, fmt.Errorf("decode immutable task specification: %w", err)
	}
	packs := make([]string, len(plans))
	for i, plan := range plans {
		paired := pairedPlanFiles(plans, i)
		pack := verifiedSourcePack{
			SpecFingerprint:    stored.SpecFingerprint,
			Subtask:            plan.Title,
			Phase:              plan.Phase,
			OwnedFiles:         allFiles(plan),
			PairedFiles:        paired,
			AcceptanceCriteria: spec.AcceptanceCriteria,
			IntegrationSeams:   spec.IntegrationSeams,
		}
		raw, err := json.MarshalIndent(pack, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encode source pack %d: %w", i, err)
		}
		packs[i] = string(raw)
	}
	return packs, nil
}

func pairedPlanFiles(plans []planEntry, index int) []string {
	seen := map[string]struct{}{}
	var paired []string
	add := func(files []string) {
		for _, file := range files {
			if _, ok := seen[file]; ok {
				continue
			}
			seen[file] = struct{}{}
			paired = append(paired, file)
		}
	}
	plan := plans[index]
	if plan.Phase == "test" {
		for _, target := range plan.TestsFor {
			if target >= 0 && target < len(plans) {
				add(allFiles(plans[target]))
			}
		}
	}
	if plan.Phase == "implementation" {
		for i := range plans {
			for _, target := range plans[i].TestsFor {
				if target == index {
					add(allFiles(plans[i]))
				}
			}
		}
	}
	return paired
}
