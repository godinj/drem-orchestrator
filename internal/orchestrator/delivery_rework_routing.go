package orchestrator

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

const (
	deliveryReworkSourceTaskKey = "delivery_rework_source_task_id"
	deliveryReworkRootTaskKey   = "delivery_rework_root_task_id"
	deliveryReworkGenerationKey = "delivery_rework_generation"
)

// stageScopedDeliveryRepairChildren preserves the original ownership model
// during an orchestrated artifact correction. Each active test,
// implementation, and integration owner gets an immutable repair child with
// exactly the same mutation scope. Dependencies and TestsFor links are
// remapped to the new generation, so integration cannot run (and the parent
// cannot re-freeze) until every upstream scoped repair has completed.
func stageScopedDeliveryRepairChildren(tx *gorm.DB, task *model.Task, reason string) error {
	sources, err := activeDeliveryRepairSources(tx, task.ID)
	if errors.Is(err, gorm.ErrRecordNotFound) || len(sources) == 0 {
		return errors.New("stage orchestrated delivery rework: no completed test, implementation, or integration subtask is available for repair")
	}
	if err != nil {
		return fmt.Errorf("stage orchestrated delivery rework: find repair owners: %w", err)
	}

	replacements := make(map[string]uuid.UUID, len(sources))
	for i := range sources {
		replacements[sources[i].ID.String()] = uuid.New()
	}

	repairIDs := make([]string, 0, len(sources))
	for i := range sources {
		source := &sources[i]
		repairID := replacements[source.ID.String()]
		ctx := cloneTaskContext(source.Context)
		rootID := deliveryReworkRootID(*source)
		generation := deliveryReworkGeneration(*source) + 1
		writable := extractWritableFiles(*source)
		ctx["prompt_adjustment"] = scopedDeliveryReworkPrompt(reason, source.Phase, writable)
		ctx["delivery_rework_pending"] = true
		ctx[deliveryReworkSourceTaskKey] = source.ID.String()
		ctx[deliveryReworkRootTaskKey] = rootID
		ctx[deliveryReworkGenerationKey] = generation
		ctx["skip_existing_work_dedup"] = true
		ctx["skip_existing_work_dedup_reason"] = "delivery_verification_failed"
		clearRepairRuntimeContext(ctx)

		repair := model.Task{
			ID:              repairID,
			ProjectID:       source.ProjectID,
			ParentTaskID:    source.ParentTaskID,
			Title:           fmt.Sprintf("%s (delivery repair %d)", source.Title, generation),
			Description:     source.Description,
			Status:          model.StatusBacklog,
			Category:        source.Category,
			Priority:        source.Priority,
			ComplexityScore: source.ComplexityScore,
			Labels:          append(model.JSONArray(nil), source.Labels...),
			DependencyIDs:   remapDeliveryReworkIDs(source.DependencyIDs, replacements),
			Phase:           source.Phase,
			TestsFor:        remapDeliveryReworkIDs(source.TestsFor, replacements),
			Context:         ctx,
			WorktreeBranch:  fmt.Sprintf("feature/%s-rework-%s", taskFeatureName(source), repairID.String()[:8]),
		}
		if err := tx.Create(&repair).Error; err != nil {
			return fmt.Errorf("stage orchestrated delivery rework: create %s repair for %s: %w", source.Phase, source.ID, err)
		}
		if err := tx.Create(&model.TaskEvent{
			ID: uuid.New(), TaskID: repair.ID, EventType: "delivery_rework_scoped",
			OldValue: "", NewValue: string(model.StatusBacklog), Actor: "orchestrator", CreatedAt: time.Now(),
			Details: model.JSONField{"evidence": map[string]any{
				"task_id": repair.ID.String(), "actor": "orchestrator", "source": "delivery_rework",
				"reason": "scoped repair child created", "references": map[string]any{
					"source_task_id": source.ID.String(), "root_task_id": rootID,
					"generation": generation, "phase": source.Phase, "writable_files": writable,
				},
			}},
		}).Error; err != nil {
			return fmt.Errorf("stage orchestrated delivery rework: audit %s repair for %s: %w", source.Phase, source.ID, err)
		}
		repairIDs = append(repairIDs, repair.ID.String())
	}

	task.Context["delivery_rework_pending"] = repairIDs[0]
	task.Context["delivery_rework_repair_ids"] = repairIDs
	task.Context["delivery_rework_repair_count"] = len(repairIDs)
	task.AssignedAgentID = nil
	if err := tx.Model(task).Select("context", "assigned_agent_id").Updates(task).Error; err != nil {
		return fmt.Errorf("stage orchestrated delivery rework: save repair routing: %w", err)
	}
	return nil
}

// activeDeliveryRepairSources returns the completed leaf of each immutable
// owner lineage. A later artifact correction therefore clones only the newest
// repair generation rather than replaying every historical child.
func activeDeliveryRepairSources(tx *gorm.DB, parentID uuid.UUID) ([]model.Task, error) {
	var completed []model.Task
	if err := tx.Where("parent_task_id = ? AND status = ? AND phase IN ?", parentID, model.StatusDone,
		[]string{"test", "implementation", "integration"}).Find(&completed).Error; err != nil {
		return nil, err
	}
	if len(completed) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	latest := make(map[string]model.Task)
	for _, candidate := range completed {
		root := deliveryReworkRootID(candidate)
		current, exists := latest[root]
		if !exists || deliveryReworkGeneration(candidate) > deliveryReworkGeneration(current) ||
			(deliveryReworkGeneration(candidate) == deliveryReworkGeneration(current) && candidate.UpdatedAt.After(current.UpdatedAt)) {
			latest[root] = candidate
		}
	}
	sources := make([]model.Task, 0, len(latest))
	for _, source := range latest {
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool {
		left, right := deliveryPhaseRank(sources[i].Phase), deliveryPhaseRank(sources[j].Phase)
		if left != right {
			return left < right
		}
		if sources[i].Priority != sources[j].Priority {
			return sources[i].Priority > sources[j].Priority
		}
		return sources[i].ID.String() < sources[j].ID.String()
	})
	return sources, nil
}

func cloneTaskContext(source model.JSONField) model.JSONField {
	clone := make(model.JSONField, len(source)+8)
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func clearRepairRuntimeContext(ctx model.JSONField) {
	for _, key := range []string{
		"retry_count", "last_error", "failure_reason", "failure_class",
		"failure_diagnosis", "failure_category", "latest_failure_reason",
		"latest_failure_class", "latest_failure_type", "latest_failure_summary",
		"latest_failure_at", "latest_failure_current", "empty_work",
		"constraint_violations", "scores", "schedule", "prep_in_progress",
	} {
		delete(ctx, key)
	}
}

func remapDeliveryReworkIDs(ids model.JSONArray, replacements map[string]uuid.UUID) model.JSONArray {
	remapped := make(model.JSONArray, len(ids))
	for i, id := range ids {
		if replacement, ok := replacements[id]; ok {
			remapped[i] = replacement.String()
		} else {
			remapped[i] = id
		}
	}
	return remapped
}

func deliveryReworkRootID(task model.Task) string {
	if task.Context != nil {
		if root, ok := task.Context[deliveryReworkRootTaskKey].(string); ok && root != "" {
			return root
		}
	}
	return task.ID.String()
}

func deliveryReworkGeneration(task model.Task) int {
	if task.Context == nil {
		return 0
	}
	switch generation := task.Context[deliveryReworkGenerationKey].(type) {
	case int:
		return generation
	case float64:
		return int(generation)
	default:
		return 0
	}
}

func deliveryPhaseRank(phase string) int {
	switch phase {
	case "test":
		return 0
	case "implementation":
		return 1
	case "integration":
		return 2
	default:
		return 3
	}
}

func scopedDeliveryReworkPrompt(reason, phase string, writable []string) string {
	scope := "the original subtask mutation scope"
	if len(writable) > 0 {
		scope = strings.Join(writable, ", ")
	}
	return fmt.Sprintf("Delivery artifact correction for the original %s owner. Address the applicable diagnostics below, edit only this repair's writable_files (%s), and commit a scoped correction. Other owners receive separate dependency-ordered repairs; do not repair their files.\n\n%s",
		phase, scope, strings.TrimSpace(reason))
}
