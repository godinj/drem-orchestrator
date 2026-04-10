package orchestrator

import (
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/experiment"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// ExperimentScheduler provides experiment-aware scheduling logic.
// When experiments are active, it partitions the agent pool across variants
// and blocks non-experiment tasks from being scheduled.
type ExperimentScheduler struct {
	db            *gorm.DB
	maxConcurrent int
}

// NewExperimentScheduler creates an experiment-aware scheduler.
func NewExperimentScheduler(db *gorm.DB, maxConcurrent int) *ExperimentScheduler {
	return &ExperimentScheduler{
		db:            db,
		maxConcurrent: maxConcurrent,
	}
}

// IsActive returns true if any experiment has status "running".
func (es *ExperimentScheduler) IsActive() (bool, error) {
	count, err := experiment.CountActiveExperiments(es.db)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetActiveExperiments returns all experiments with status "running".
func (es *ExperimentScheduler) GetActiveExperiments() ([]experiment.Experiment, error) {
	return experiment.ListActiveExperiments(es.db)
}

// IsExperimentTask returns true if the task belongs to an experiment variant.
func (es *ExperimentScheduler) IsExperimentTask(taskID uuid.UUID) (bool, *experiment.Variant, error) {
	variant, err := experiment.GetVariantByTaskID(es.db, taskID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, variant, nil
}

// GetVariantForTask returns the variant for a task, or nil if not found.
func (es *ExperimentScheduler) GetVariantForTask(taskID uuid.UUID) (*experiment.Variant, error) {
	variant, err := experiment.GetVariantByTaskID(es.db, taskID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return variant, nil
}

// AgentsPerVariant calculates how many agents each variant should get.
// Formula: max_concurrent_agents / total_active_variants
// Returns the per-variant allocation and total variant count.
func (es *ExperimentScheduler) AgentsPerVariant() (int, int, error) {
	experiments, err := es.GetActiveExperiments()
	if err != nil {
		return 0, 0, err
	}

	totalVariants := 0
	for _, exp := range experiments {
		totalVariants += len(exp.Variants)
	}

	if totalVariants == 0 {
		return 0, 0, nil
	}

	agentsPerVariant := es.maxConcurrent / totalVariants
	if agentsPerVariant < 1 {
		agentsPerVariant = 1
	}

	return agentsPerVariant, totalVariants, nil
}

// GetVariantAgentCounts returns a map of variant ID to the count of working
// agents currently assigned to tasks in that variant.
func (es *ExperimentScheduler) GetVariantAgentCounts() (map[uuid.UUID]int, error) {
	experiments, err := es.GetActiveExperiments()
	if err != nil {
		return nil, err
	}

	counts := make(map[uuid.UUID]int)

	// Initialize all variants with 0
	for _, exp := range experiments {
		for _, v := range exp.Variants {
			counts[v.ID] = 0
		}
	}

	// Count working agents per variant
	for _, exp := range experiments {
		for _, v := range exp.Variants {
			var agentCount int64
			if err := es.db.Model(&model.Agent{}).
				Where("status = ? AND current_task_id = ?", model.AgentWorking, v.TaskID).
				Count(&agentCount).Error; err != nil {
				return nil, fmt.Errorf("count agents for variant %s: %w", v.ID, err)
			}
			counts[v.ID] = int(agentCount)
		}
	}

	return counts, nil
}

// ShouldBlockNormalTasks returns true if normal (non-experiment) tasks should
// be blocked from scheduling. This is true when any experiment is active.
func (es *ExperimentScheduler) ShouldBlockNormalTasks() (bool, error) {
	return es.IsActive()
}

// CanScheduleTask returns true if the task can be scheduled given current
// experiment state and agent allocation. For experiment tasks, it checks
// if the variant has reached its agent quota. For normal tasks, it checks
// if experiments are active (which would block them).
func (es *ExperimentScheduler) CanScheduleTask(taskID uuid.UUID) (bool, string, error) {
	// Check if this is an experiment task
	isExperimentTask, variant, err := es.IsExperimentTask(taskID)
	if err != nil {
		return false, "", err
	}

	if !isExperimentTask {
		// Normal task — check if experiments are active
		active, err := es.IsActive()
		if err != nil {
			return false, "", err
		}
		if active {
			return false, "experiments active, normal tasks paused", nil
		}
		return true, "no active experiments", nil
	}

	// Experiment task — check variant agent quota
	agentsPerVariant, _, err := es.AgentsPerVariant()
	if err != nil {
		return false, "", err
	}

	variantCounts, err := es.GetVariantAgentCounts()
	if err != nil {
		return false, "", err
	}

	currentCount := variantCounts[variant.ID]
	if currentCount >= agentsPerVariant {
		return false, fmt.Sprintf("variant %s at agent limit (%d/%d)", variant.ID, currentCount, agentsPerVariant), nil
	}

	return true, fmt.Sprintf("variant %s has capacity (%d/%d)", variant.ID, currentCount, agentsPerVariant), nil
}

// GetUnderAllocatedVariants returns variants that have fewer agents than
// their allotted quota, ordered by under-allocation (most under-allocated first).
type VariantAllocation struct {
	Variant         *experiment.Variant
	CurrentAgents   int
	AllottedAgents  int
	UnderAllocation int
}

func (es *ExperimentScheduler) GetUnderAllocatedVariants() ([]VariantAllocation, error) {
	experiments, err := es.GetActiveExperiments()
	if err != nil {
		return nil, err
	}

	agentsPerVariant, _, err := es.AgentsPerVariant()
	if err != nil {
		return nil, err
	}

	variantCounts, err := es.GetVariantAgentCounts()
	if err != nil {
		return nil, err
	}

	var underAllocated []VariantAllocation
	for _, exp := range experiments {
		for _, v := range exp.Variants {
			current := variantCounts[v.ID]
			if current < agentsPerVariant {
				underAllocated = append(underAllocated, VariantAllocation{
					Variant:         &v,
					CurrentAgents:   current,
					AllottedAgents:  agentsPerVariant,
					UnderAllocation: agentsPerVariant - current,
				})
			}
		}
	}

	// Sort by under-allocation descending (most under-allocated first)
	sort.Slice(underAllocated, func(i, j int) bool {
		return underAllocated[i].UnderAllocation > underAllocated[j].UnderAllocation
	})

	return underAllocated, nil
}
