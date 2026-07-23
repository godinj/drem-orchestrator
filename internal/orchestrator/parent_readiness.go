package orchestrator

import (
	"fmt"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/model"
)

type parentReadiness struct {
	Ready     bool
	Blockers  []string
	AnyFailed bool
}

func (o *Orchestrator) evaluateParentReadiness(parent *model.Task, target model.TaskStatus) (parentReadiness, error) {
	var subtasks []model.Task
	if err := o.db.Where("parent_task_id = ?", parent.ID).Find(&subtasks).Error; err != nil {
		return parentReadiness{}, fmt.Errorf("parent readiness: query subtasks: %w", err)
	}
	if len(subtasks) == 0 {
		return parentReadiness{Blockers: []string{"no subtasks exist"}}, nil
	}

	byID := make(map[string]model.Task, len(subtasks))
	for _, sub := range subtasks {
		byID[sub.ID.String()] = sub
	}

	required := requiredSubtasksForParentTarget(subtasks, target)
	if len(required) == 0 {
		return parentReadiness{Blockers: []string{fmt.Sprintf("no required subtasks for %s", target)}}, nil
	}

	result := parentReadiness{Ready: true}
	for id := range required {
		sub := byID[id]
		if sub.Status == model.StatusCancelled {
			continue
		}
		if sub.Status != model.StatusDone {
			result.Ready = false
			result.Blockers = append(result.Blockers,
				fmt.Sprintf("subtask %s (%s, phase %q) is %s", sub.ID, sub.Title, sub.Phase, sub.Status))
			if sub.Status == model.StatusFailed || sub.Status == model.StatusRejected {
				result.AnyFailed = true
			}
		}
		for _, depID := range sub.DependencyIDs {
			dep, ok := byID[depID]
			if !ok {
				result.Ready = false
				result.Blockers = append(result.Blockers,
					fmt.Sprintf("subtask %s has missing dependency %s", sub.ID, depID))
				continue
			}
			if dep.Status != model.StatusDone {
				result.Ready = false
				result.Blockers = append(result.Blockers,
					fmt.Sprintf("dependency-blocked: subtask %s (phase %q) depends on %s (phase %q, status %s)",
						sub.ID, sub.Phase, dep.ID, dep.Phase, dep.Status))
				if dep.Status == model.StatusFailed || dep.Status == model.StatusRejected {
					result.AnyFailed = true
				}
			}
		}
	}
	return result, nil
}

func requiredSubtasksForParentTarget(subtasks []model.Task, target model.TaskStatus) map[string]bool {
	required := make(map[string]bool)
	switch target {
	case model.StatusTestReview:
		byID := make(map[string]model.Task, len(subtasks))
		var testSubtasks []model.Task
		for _, sub := range subtasks {
			byID[sub.ID.String()] = sub
			if sub.Phase == "test" {
				testSubtasks = append(testSubtasks, sub)
			}
		}
		for _, sub := range activeTestWritingSubtasks(testSubtasks) {
			if isFutureTestSubtaskBlockedByImplementation(sub, byID) {
				continue
			}
			required[sub.ID.String()] = true
		}
		for changed := true; changed; {
			changed = false
			for id := range required {
				for _, depID := range byID[id].DependencyIDs {
					if _, ok := byID[depID]; !ok || required[depID] {
						continue
					}
					required[depID] = true
					changed = true
				}
			}
		}
	case model.StatusTestingReady, model.StatusMerging, model.StatusDone:
		supersededRejectedTests := supersededRejectedTestIDs(subtasks)
		for _, sub := range subtasks {
			if _, superseded := supersededRejectedTests[sub.ID]; superseded {
				continue
			}
			required[sub.ID.String()] = true
		}
	}
	return required
}

func isFutureTestSubtaskBlockedByImplementation(sub model.Task, byID map[string]model.Task) bool {
	if sub.Phase != "test" || sub.Status != model.StatusBacklog {
		return false
	}
	for _, depID := range sub.DependencyIDs {
		dep, ok := byID[depID]
		if ok && dep.Phase == "implementation" && dep.Status != model.StatusDone {
			return true
		}
	}
	return false
}

func (o *Orchestrator) recordParentReadinessBlocked(parent *model.Task, target model.TaskStatus, readiness parentReadiness) error {
	if parent.Context == nil {
		parent.Context = make(model.JSONField)
	}
	parent.Context["parent_readiness_target"] = string(target)
	parent.Context["parent_readiness_blockers"] = strings.Join(readiness.Blockers, "\n")
	parent.Context["parent_readiness_blocker_count"] = float64(len(readiness.Blockers))
	return o.db.Save(parent).Error
}
