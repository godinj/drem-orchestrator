package orchestrator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// conflictScoreBlockThreshold is the minimum conflict score that blocks dispatch.
// A score at or above this threshold means the candidate has too much file
// overlap with in-progress tasks to run safely in parallel.
const conflictScoreBlockThreshold = 0.3

// ConflictDetail describes the file overlap between a candidate task and
// a single in-progress task.
type ConflictDetail struct {
	BlockingTaskID uuid.UUID // The in-progress task causing the conflict
	OverlapFiles   []string  // Files shared between the two tasks
	Score          float64   // 0.0 = no overlap, 1.0 = complete overlap
}

// DispatchDecision is the result of evaluating whether a candidate task
// can be dispatched. It carries enough information for the caller to log
// why a task was blocked without needing to understand the scoring internals.
type DispatchDecision struct {
	TaskID       uuid.UUID
	Dispatchable bool
	Reason       string           // human-readable explanation
	Conflicts    []ConflictDetail // non-empty when blocked by file conflicts
}

// SchedulingPolicy owns task ordering, conflict scoring, and dependency
// resolution. It is the single entry point for dispatch decisions — callers
// ask "which of these candidates can I dispatch?" and get back a fully
// resolved answer without needing to understand wave groups, file overlaps,
// or dependency chains.
type SchedulingPolicy struct {
	db                *gorm.DB
	disableWaveGating bool
}

// NewSchedulingPolicy creates a SchedulingPolicy backed by the given database.
func NewSchedulingPolicy(db *gorm.DB) *SchedulingPolicy {
	return &SchedulingPolicy{db: db}
}

// WithoutWaveGating returns a policy copy that preserves dependency and file
// conflict checks but ignores parent wave groups. This is used by phase-filtered
// scheduling, where earlier wave tasks may be intentionally outside scope.
func (sp *SchedulingPolicy) WithoutWaveGating() *SchedulingPolicy {
	return &SchedulingPolicy{db: sp.db, disableWaveGating: true}
}

// EvaluateDispatch analyzes each candidate task against the set of currently
// in-progress tasks and returns a dispatch decision for each candidate.
//
// A task is dispatchable only when ALL of the following are true:
//   - Its dependencies (DependencyIDs) are all in status Done
//   - Its wave group is current (all prior groups are complete)
//   - Its file conflict score against every in-progress task is below the
//     block threshold
//
// The returned slice has the same length and order as candidates.
func (sp *SchedulingPolicy) EvaluateDispatch(
	candidates []model.Task,
	inProgress []model.Task,
) []DispatchDecision {
	decisions := make([]DispatchDecision, len(candidates))

	// Pre-extract file lists for in-progress tasks.
	ipFiles := make(map[uuid.UUID][]string, len(inProgress))
	for _, ip := range inProgress {
		ipFiles[ip.ID] = extractEstimatedFiles(ip)
	}

	// Build a set of in-progress task IDs for wave group checking.
	ipIDs := make(map[uuid.UUID]bool, len(inProgress))
	for _, ip := range inProgress {
		ipIDs[ip.ID] = true
	}

	for i, cand := range candidates {
		decisions[i] = sp.evaluateCandidate(cand, inProgress, ipFiles, ipIDs)
	}

	// Mutual conflict pass: check approved candidates against each other.
	// Earlier candidates take priority; later ones are blocked if their file
	// sets overlap with already-approved candidates in this batch.
	accumulated := make(map[string]uuid.UUID) // file -> first approved candidate's task ID
	for i, cand := range candidates {
		if !decisions[i].Dispatchable {
			continue
		}
		candFiles := extractEstimatedFiles(cand)
		if len(candFiles) == 0 {
			continue
		}
		if len(accumulated) > 0 {
			accFiles := make([]string, 0, len(accumulated))
			for f := range accumulated {
				accFiles = append(accFiles, f)
			}
			overlap, score := fileOverlapDetails(candFiles, accFiles)
			if score >= conflictScoreBlockThreshold {
				var blockingID uuid.UUID
				if len(overlap) > 0 {
					blockingID = accumulated[overlap[0]]
				}
				decisions[i].Dispatchable = false
				decisions[i].Reason = fmt.Sprintf("file conflict with co-dispatched task %s", blockingID)
				decisions[i].Conflicts = append(decisions[i].Conflicts, ConflictDetail{
					BlockingTaskID: blockingID,
					OverlapFiles:   overlap,
					Score:          score,
				})
				continue
			}
		}
		for _, f := range candFiles {
			if _, exists := accumulated[f]; !exists {
				accumulated[f] = cand.ID
			}
		}
	}

	return decisions
}

// evaluateCandidate checks a single candidate against all dispatch constraints.
func (sp *SchedulingPolicy) evaluateCandidate(
	cand model.Task,
	inProgress []model.Task,
	ipFiles map[uuid.UUID][]string,
	ipIDs map[uuid.UUID]bool,
) DispatchDecision {
	d := DispatchDecision{TaskID: cand.ID}

	// 1. Check explicit dependencies.
	if len(cand.DependencyIDs) > 0 {
		met, err := DependenciesMet(sp.db, cand.DependencyIDs)
		if err != nil {
			d.Reason = fmt.Sprintf("dependency check failed: %v", err)
			return d
		}
		if !met {
			if reason := sp.describeMixedPhaseDependencyBlock(cand); reason != "" {
				d.Reason = reason
				return d
			}
			d.Reason = fmt.Sprintf("unmet dependencies: %s", sp.describeDependencies(cand.DependencyIDs))
			return d
		}
	}

	// 2. Check wave group ordering.
	if !sp.disableWaveGating {
		if blocked, reason := sp.isWaveBlocked(cand, ipIDs); blocked {
			d.Reason = reason
			return d
		}
	}

	// 3. Check file conflicts against in-progress tasks.
	candFiles := extractEstimatedFiles(cand)
	for _, ip := range inProgress {
		files := ipFiles[ip.ID]
		if len(candFiles) == 0 || len(files) == 0 {
			continue
		}
		overlap, score := fileOverlapDetails(candFiles, files)
		if score >= conflictScoreBlockThreshold {
			d.Conflicts = append(d.Conflicts, ConflictDetail{
				BlockingTaskID: ip.ID,
				OverlapFiles:   overlap,
				Score:          score,
			})
		}
	}

	if len(d.Conflicts) > 0 {
		d.Reason = fmt.Sprintf("file conflict with %d in-progress task(s)", len(d.Conflicts))
		return d
	}

	d.Dispatchable = true
	d.Reason = "all checks passed"
	return d
}

// isWaveBlocked checks whether the candidate's wave group is blocked by
// an earlier group that still has active tasks.
func (sp *SchedulingPolicy) isWaveBlocked(
	cand model.Task,
	activeIDs map[uuid.UUID]bool,
) (bool, string) {
	if cand.ParentTaskID == nil {
		return false, ""
	}

	var parent model.Task
	if err := sp.db.Select("id", "context").
		First(&parent, "id = ?", *cand.ParentTaskID).Error; err != nil {
		return false, ""
	}

	if parent.Context == nil {
		return false, ""
	}
	scheduleRaw, ok := parent.Context["schedule"]
	if !ok {
		return false, ""
	}

	scheduleJSON, err := json.Marshal(scheduleRaw)
	if err != nil {
		return false, ""
	}
	var schedule Schedule
	if err := json.Unmarshal(scheduleJSON, &schedule); err != nil {
		return false, ""
	}

	// Find which group the candidate belongs to.
	candGroup := -1
	for _, g := range schedule.Groups {
		for _, id := range g.TaskIDs {
			if id == cand.ID {
				candGroup = g.Order
				break
			}
		}
		if candGroup >= 0 {
			break
		}
	}
	if candGroup < 0 {
		return false, ""
	}

	// Check if any earlier group has active (non-terminal) tasks.
	for _, g := range schedule.Groups {
		if g.Order >= candGroup {
			continue
		}
		for _, id := range g.TaskIDs {
			if activeIDs[id] {
				var task model.Task
				status := model.StatusInProgress
				if err := sp.db.Select("status").First(&task, "id = ?", id).Error; err == nil {
					status = task.Status
				}
				return true, fmt.Sprintf(
					"wave group %d blocked: group %d task %s is %s",
					candGroup, g.Order, id, status)
			}
			var task model.Task
			if err := sp.db.Select("id", "status").
				First(&task, "id = ?", id).Error; err != nil {
				continue
			}
			if !isTerminalWaveStatus(task.Status) {
				return true, fmt.Sprintf(
					"wave group %d blocked: group %d task %s is %s",
					candGroup, g.Order, id, task.Status)
			}
		}
	}

	return false, ""
}

func (sp *SchedulingPolicy) describeDependencies(dependencyIDs []string) string {
	if len(dependencyIDs) == 0 {
		return "none"
	}

	var tasks []model.Task
	if err := sp.db.Select("id", "status").Where("id IN ?", dependencyIDs).Find(&tasks).Error; err != nil {
		return fmt.Sprintf("%v (status lookup failed: %v)", dependencyIDs, err)
	}

	statusByID := make(map[string]model.TaskStatus, len(tasks))
	for _, task := range tasks {
		statusByID[task.ID.String()] = task.Status
	}

	parts := make([]string, 0, len(dependencyIDs))
	for _, id := range dependencyIDs {
		status, ok := statusByID[id]
		if !ok {
			parts = append(parts, fmt.Sprintf("%s(missing)", id))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s(%s)", id, status))
	}
	return strings.Join(parts, ", ")
}

func (sp *SchedulingPolicy) describeMixedPhaseDependencyBlock(cand model.Task) string {
	if cand.Phase != "test" || len(cand.DependencyIDs) == 0 {
		return ""
	}

	var blockers []string
	for _, depID := range cand.DependencyIDs {
		var dep model.Task
		if err := sp.db.First(&dep, "id = ?", depID).Error; err != nil {
			continue
		}
		if dep.Phase == "implementation" && dep.Status != model.StatusDone {
			blockers = append(blockers, fmt.Sprintf("%s(%s: %s)", dep.ID, dep.Phase, dep.Status))
		}
	}
	if len(blockers) == 0 {
		return ""
	}
	return fmt.Sprintf("mixed-phase dependency blocker: test-phase task depends on implementation-phase work that cannot run during test_writing: %s", strings.Join(blockers, ", "))
}

func isTerminalWaveStatus(status model.TaskStatus) bool {
	switch status {
	case model.StatusDone, model.StatusFailed, model.StatusRejected, model.StatusCancelled:
		return true
	default:
		return false
	}
}

// ComputeConflictScore calculates the overlap severity between two file lists.
// Returns 0.0 when there is no overlap and 1.0 when every file in the smaller
// list appears in the larger list. The score is defined as:
//
//	|intersection| / min(|a|, |b|)
//
// This makes the score independent of list size — a task touching 2 files that
// are both in the other task's 100-file list still scores 1.0.
func ComputeConflictScore(a, b []string) float64 {
	setA := uniqueSet(a)
	setB := uniqueSet(b)

	if len(setA) == 0 || len(setB) == 0 {
		return 0.0
	}

	intersection := 0
	for f := range setA {
		if setB[f] {
			intersection++
		}
	}

	minSize := len(setA)
	if len(setB) < minSize {
		minSize = len(setB)
	}

	return float64(intersection) / float64(minSize)
}

// fileOverlapDetails returns the list of files shared between two file lists
// and the conflict score. This is the internal workhorse behind
// EvaluateDispatch's per-task conflict analysis.
func fileOverlapDetails(a, b []string) ([]string, float64) {
	setB := uniqueSet(b)
	seen := make(map[string]bool)
	var overlap []string

	for _, f := range a {
		if setB[f] && !seen[f] {
			overlap = append(overlap, f)
			seen[f] = true
		}
	}

	score := ComputeConflictScore(a, b)
	return overlap, score
}

// activeTaskFiles queries the database for all tasks currently in-progress
// (status in_progress or merging) and returns their estimated file lists
// keyed by task ID.
func (sp *SchedulingPolicy) activeTaskFiles() (map[uuid.UUID][]string, error) {
	var tasks []model.Task
	if err := sp.db.Where("status IN ?",
		[]model.TaskStatus{model.StatusInProgress, model.StatusMerging},
	).Find(&tasks).Error; err != nil {
		return nil, fmt.Errorf("active task files: %w", err)
	}

	result := make(map[uuid.UUID][]string, len(tasks))
	for _, t := range tasks {
		files := extractEstimatedFiles(t)
		if len(files) > 0 {
			result[t.ID] = files
		}
	}
	return result, nil
}

// uniqueSet deduplicates a string slice into a set.
func uniqueSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
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
