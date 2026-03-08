package orchestrator

import (
	"fmt"
	"sort"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ScheduleSummary provides an overview of the scheduling state for a project.
type ScheduleSummary struct {
	TasksByStatus  map[model.TaskStatus]int
	AgentsByStatus map[model.AgentStatus]int
	BlockedTasks   []BlockedTask
	QueueDepth     int // assignable tasks waiting for agents
}

// BlockedTask describes a task waiting on unfinished dependencies.
type BlockedTask struct {
	TaskID      uuid.UUID
	BlockingIDs []uuid.UUID
}

// GetScheduleSummary returns an overview of the project's scheduling state.
func GetScheduleSummary(db *gorm.DB, projectID uuid.UUID) (*ScheduleSummary, error) {
	summary := &ScheduleSummary{
		TasksByStatus:  make(map[model.TaskStatus]int),
		AgentsByStatus: make(map[model.AgentStatus]int),
	}

	// Count tasks by status.
	var taskCounts []struct {
		Status model.TaskStatus
		Count  int
	}
	if err := db.Model(&model.Task{}).
		Select("status, count(*) as count").
		Where("project_id = ?", projectID).
		Group("status").
		Find(&taskCounts).Error; err != nil {
		return nil, fmt.Errorf("get schedule summary: count tasks: %w", err)
	}
	for _, tc := range taskCounts {
		summary.TasksByStatus[tc.Status] = tc.Count
	}

	// Count agents by status.
	var agentCounts []struct {
		Status model.AgentStatus
		Count  int
	}
	if err := db.Model(&model.Agent{}).
		Select("status, count(*) as count").
		Where("project_id = ?", projectID).
		Group("status").
		Find(&agentCounts).Error; err != nil {
		return nil, fmt.Errorf("get schedule summary: count agents: %w", err)
	}
	for _, ac := range agentCounts {
		summary.AgentsByStatus[ac.Status] = ac.Count
	}

	// Find assignable tasks and blocked tasks.
	assignable, err := GetAssignableTasks(db, projectID)
	if err != nil {
		return nil, fmt.Errorf("get schedule summary: assignable: %w", err)
	}
	summary.QueueDepth = len(assignable)

	// Find blocked tasks: BACKLOG tasks (root or subtask) with unmet dependencies.
	var backlogSubtasks []model.Task
	if err := db.Where(
		"project_id = ? AND status = ? AND dependency_ids IS NOT NULL",
		projectID, model.StatusBacklog,
	).Find(&backlogSubtasks).Error; err != nil {
		return nil, fmt.Errorf("get schedule summary: blocked: %w", err)
	}

	for _, sub := range backlogSubtasks {
		if len(sub.DependencyIDs) == 0 {
			continue
		}
		blocking, err := GetBlockingTasks(db, sub.DependencyIDs)
		if err != nil {
			continue
		}
		if len(blocking) > 0 {
			summary.BlockedTasks = append(summary.BlockedTasks, BlockedTask{
				TaskID:      sub.ID,
				BlockingIDs: blocking,
			})
		}
	}

	return summary, nil
}

// GetAssignableTasks returns BACKLOG subtasks whose dependencies are all met
// and whose parent task is IN_PROGRESS. These are ready to be picked up by
// agents.
func GetAssignableTasks(db *gorm.DB, projectID uuid.UUID) ([]model.Task, error) {
	// Get BACKLOG subtasks that have an IN_PROGRESS parent.
	var candidates []model.Task
	if err := db.Where(
		"project_id = ? AND status = ? AND parent_task_id IS NOT NULL",
		projectID, model.StatusBacklog,
	).Find(&candidates).Error; err != nil {
		return nil, fmt.Errorf("get assignable tasks: query: %w", err)
	}

	// Filter to those with IN_PROGRESS parents.
	parentIDs := make(map[uuid.UUID]bool)
	for _, c := range candidates {
		if c.ParentTaskID != nil {
			parentIDs[*c.ParentTaskID] = false // placeholder
		}
	}

	if len(parentIDs) > 0 {
		ids := make([]uuid.UUID, 0, len(parentIDs))
		for id := range parentIDs {
			ids = append(ids, id)
		}

		var inProgressParents []model.Task
		if err := db.Where("id IN ? AND status = ?", ids, model.StatusInProgress).
			Find(&inProgressParents).Error; err != nil {
			return nil, fmt.Errorf("get assignable tasks: check parents: %w", err)
		}
		for _, p := range inProgressParents {
			parentIDs[p.ID] = true
		}
	}

	var assignable []model.Task
	for _, c := range candidates {
		// Parent must be IN_PROGRESS.
		if c.ParentTaskID == nil || !parentIDs[*c.ParentTaskID] {
			continue
		}

		// Dependencies must be met.
		if len(c.DependencyIDs) > 0 {
			met, err := DependenciesMet(db, c.DependencyIDs)
			if err != nil || !met {
				continue
			}
		}

		assignable = append(assignable, c)
	}

	return assignable, nil
}

// DependenciesMet checks if all tasks in dependencyIDs have status DONE.
func DependenciesMet(db *gorm.DB, dependencyIDs []string) (bool, error) {
	if len(dependencyIDs) == 0 {
		return true, nil
	}

	var doneCount int64
	if err := db.Model(&model.Task{}).
		Where("id IN ? AND status = ?", dependencyIDs, model.StatusDone).
		Count(&doneCount).Error; err != nil {
		return false, fmt.Errorf("dependencies met: %w", err)
	}

	return int(doneCount) == len(dependencyIDs), nil
}

// GetBlockingTasks returns the subset of dependencyIDs that are not DONE.
func GetBlockingTasks(db *gorm.DB, dependencyIDs []string) ([]uuid.UUID, error) {
	if len(dependencyIDs) == 0 {
		return nil, nil
	}

	var notDone []model.Task
	if err := db.Where("id IN ? AND status != ?", dependencyIDs, model.StatusDone).
		Select("id").
		Find(&notDone).Error; err != nil {
		return nil, fmt.Errorf("get blocking tasks: %w", err)
	}

	blocking := make([]uuid.UUID, 0, len(notDone))
	for _, t := range notDone {
		blocking = append(blocking, t.ID)
	}
	return blocking, nil
}

// SubtaskGroup represents a set of subtasks that can run concurrently.
// All subtasks within a group have no file overlap.
type SubtaskGroup struct {
	Order   int         `json:"order"`
	TaskIDs []uuid.UUID `json:"task_ids"`
}

// Schedule represents the ordered execution plan for a set of subtasks.
type Schedule struct {
	Groups []SubtaskGroup `json:"groups"`
}

// BuildSchedule analyzes file overlap between subtasks and produces
// an ordered list of groups. Subtasks within a group have no file
// overlap and can run concurrently. Groups run sequentially — group
// N+1 starts only after all subtasks in group N are merged.
//
// If no subtasks have file data, returns a single group containing
// all subtasks (backward-compatible behavior).
func BuildSchedule(subtasks []model.Task) Schedule {
	if len(subtasks) == 0 {
		return Schedule{}
	}

	n := len(subtasks)

	// Extract file lists from each subtask's Context["estimated_files"].
	fileLists := make([][]string, n)
	anyFiles := false
	for i, sub := range subtasks {
		fileLists[i] = extractEstimatedFiles(sub)
		if len(fileLists[i]) > 0 {
			anyFiles = true
		}
	}

	// Fallback: if no subtask has file data, return a single group with all.
	if !anyFiles {
		ids := make([]uuid.UUID, n)
		for i, sub := range subtasks {
			ids[i] = sub.ID
		}
		return Schedule{
			Groups: []SubtaskGroup{{Order: 0, TaskIDs: ids}},
		}
	}

	// Build index from subtask ID to position for dependency lookup.
	idToIdx := make(map[uuid.UUID]int, n)
	for i, sub := range subtasks {
		idToIdx[sub.ID] = i
	}

	// Build undirected conflict graph (adjacency list).
	// Edges connect subtasks whose file lists overlap.
	adj := make([]map[int]bool, n)
	for i := range adj {
		adj[i] = make(map[int]bool)
	}

	// File overlap edges.
	for i := 0; i < n; i++ {
		if len(fileLists[i]) == 0 {
			continue
		}
		setI := make(map[string]bool, len(fileLists[i]))
		for _, f := range fileLists[i] {
			setI[f] = true
		}
		for j := i + 1; j < n; j++ {
			for _, f := range fileLists[j] {
				if setI[f] {
					adj[i][j] = true
					adj[j][i] = true
					break
				}
			}
		}
	}

	// Build directed dependency edges: if subtask[j] depends on subtask[i],
	// then j must come after i (j's color > i's color).
	// depsBefore[j] contains indices that must be in an earlier group than j.
	depsBefore := make([][]int, n)
	for i, sub := range subtasks {
		for _, depIDStr := range sub.DependencyIDs {
			depID, err := uuid.Parse(depIDStr)
			if err != nil {
				continue
			}
			if depIdx, ok := idToIdx[depID]; ok {
				depsBefore[i] = append(depsBefore[i], depIdx)
				// Also add conflict edge so they cannot share a color.
				adj[i][depIdx] = true
				adj[depIdx][i] = true
			}
		}
	}

	// Topological sort respecting dependencies, breaking ties by degree
	// descending. This ensures that when we color a node, all of its
	// dependencies have already been colored, so the minColor constraint
	// is always accurate.
	order := topologicalSortByDegree(n, depsBefore, adj)

	// Greedy graph coloring with dependency ordering constraint.
	colors := make([]int, n)
	for i := range colors {
		colors[i] = -1
	}

	maxColor := 0
	for _, node := range order {
		// Find colors used by neighbors.
		usedColors := make(map[int]bool)
		for neighbor := range adj[node] {
			if colors[neighbor] >= 0 {
				usedColors[colors[neighbor]] = true
			}
		}

		// Find the minimum color required by dependency ordering:
		// this node's color must be > all colors of its dependencies.
		minColor := 0
		for _, depIdx := range depsBefore[node] {
			if colors[depIdx] >= 0 && colors[depIdx]+1 > minColor {
				minColor = colors[depIdx] + 1
			}
		}

		// Assign the lowest color >= minColor that doesn't conflict.
		color := minColor
		for usedColors[color] {
			color++
		}
		colors[node] = color
		if color > maxColor {
			maxColor = color
		}
	}

	// Build groups from colors.
	groupMap := make(map[int][]uuid.UUID)
	for i, c := range colors {
		groupMap[c] = append(groupMap[c], subtasks[i].ID)
	}

	groups := make([]SubtaskGroup, 0, len(groupMap))
	for c := 0; c <= maxColor; c++ {
		if ids, ok := groupMap[c]; ok {
			groups = append(groups, SubtaskGroup{
				Order:   c,
				TaskIDs: ids,
			})
		}
	}

	return Schedule{Groups: groups}
}

// topologicalSortByDegree returns a processing order that respects dependency
// constraints (dependencies are processed before dependents) while breaking
// ties by conflict graph degree descending (nodes with more conflicts first).
// This ensures greedy coloring always has accurate minColor constraints.
func topologicalSortByDegree(n int, depsBefore [][]int, adj []map[int]bool) []int {
	// Compute in-degree for topological sort.
	inDegree := make([]int, n)
	// depsAfter[i] = nodes that depend on i (i.e., i is in their depsBefore).
	depsAfter := make([][]int, n)
	for i := 0; i < n; i++ {
		for _, dep := range depsBefore[i] {
			depsAfter[dep] = append(depsAfter[dep], i)
			inDegree[i]++
		}
	}

	// Initialize queue with nodes that have no dependencies.
	// Use a sorted insertion to break ties by degree descending.
	var queue []int
	for i := 0; i < n; i++ {
		if inDegree[i] == 0 {
			queue = append(queue, i)
		}
	}
	sort.Slice(queue, func(a, b int) bool {
		return len(adj[queue[a]]) > len(adj[queue[b]])
	})

	var order []int
	for len(queue) > 0 {
		// Pop highest-degree node from front.
		node := queue[0]
		queue = queue[1:]
		order = append(order, node)

		// Reduce in-degree for dependents.
		for _, dependent := range depsAfter[node] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				queue = append(queue, dependent)
				// Re-sort to maintain degree ordering.
				sort.Slice(queue, func(a, b int) bool {
					return len(adj[queue[a]]) > len(adj[queue[b]])
				})
			}
		}
	}

	// If there are nodes not in order (cycle — shouldn't happen if validated),
	// append them to avoid missing nodes.
	if len(order) < n {
		inOrder := make(map[int]bool, len(order))
		for _, idx := range order {
			inOrder[idx] = true
		}
		for i := 0; i < n; i++ {
			if !inOrder[i] {
				order = append(order, i)
			}
		}
	}

	return order
}

// extractEstimatedFiles pulls the estimated_files list from a task's Context.
func extractEstimatedFiles(task model.Task) []string {
	if task.Context == nil {
		return nil
	}
	raw, ok := task.Context["estimated_files"]
	if !ok {
		return nil
	}

	// Context is map[string]any from JSON deserialization, so the value
	// is typically []interface{}.
	switch v := raw.(type) {
	case []any:
		files := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				files = append(files, s)
			}
		}
		return files
	case []string:
		return v
	default:
		return nil
	}
}
