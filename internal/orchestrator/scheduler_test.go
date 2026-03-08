package orchestrator

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestBuildSchedule_NoFileOverlap(t *testing.T) {
	// 4 subtasks, no shared files -> single group with all 4.
	subtasks := []model.Task{
		{ID: uuid.New(), Context: model.JSONField{"estimated_files": []any{"a.go"}}},
		{ID: uuid.New(), Context: model.JSONField{"estimated_files": []any{"b.go"}}},
		{ID: uuid.New(), Context: model.JSONField{"estimated_files": []any{"c.go"}}},
		{ID: uuid.New(), Context: model.JSONField{"estimated_files": []any{"d.go"}}},
	}

	schedule := BuildSchedule(subtasks)

	if len(schedule.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(schedule.Groups))
	}
	if len(schedule.Groups[0].TaskIDs) != 4 {
		t.Errorf("expected 4 tasks in group, got %d", len(schedule.Groups[0].TaskIDs))
	}
}

func TestBuildSchedule_FullOverlap(t *testing.T) {
	// 3 subtasks all touching the same file -> 3 groups of 1 each (sequential).
	subtasks := []model.Task{
		{ID: uuid.New(), Context: model.JSONField{"estimated_files": []any{"shared.go"}}},
		{ID: uuid.New(), Context: model.JSONField{"estimated_files": []any{"shared.go"}}},
		{ID: uuid.New(), Context: model.JSONField{"estimated_files": []any{"shared.go"}}},
	}

	schedule := BuildSchedule(subtasks)

	if len(schedule.Groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(schedule.Groups))
	}
	for i, g := range schedule.Groups {
		if len(g.TaskIDs) != 1 {
			t.Errorf("group %d: expected 1 task, got %d", i, len(g.TaskIDs))
		}
	}
}

func TestBuildSchedule_PartialOverlap(t *testing.T) {
	// A overlaps B (shared.go), B overlaps C (other.go), A doesn't overlap C.
	// -> 2 groups: {A, C} and {B} (or similar valid coloring).
	idA := uuid.New()
	idB := uuid.New()
	idC := uuid.New()

	subtasks := []model.Task{
		{ID: idA, Context: model.JSONField{"estimated_files": []any{"shared.go", "a.go"}}},
		{ID: idB, Context: model.JSONField{"estimated_files": []any{"shared.go", "other.go"}}},
		{ID: idC, Context: model.JSONField{"estimated_files": []any{"other.go", "c.go"}}},
	}

	schedule := BuildSchedule(subtasks)

	if len(schedule.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(schedule.Groups))
	}

	// Build a map of task ID -> group order for verification.
	taskGroup := make(map[uuid.UUID]int)
	for _, g := range schedule.Groups {
		for _, id := range g.TaskIDs {
			taskGroup[id] = g.Order
		}
	}

	// A and B overlap and must not be in the same group.
	if taskGroup[idA] == taskGroup[idB] {
		t.Error("A and B overlap and should be in different groups")
	}
	// B and C overlap and must not be in the same group.
	if taskGroup[idB] == taskGroup[idC] {
		t.Error("B and C overlap and should be in different groups")
	}
	// A and C don't overlap, they can be in the same group.
	if taskGroup[idA] != taskGroup[idC] {
		t.Error("A and C don't overlap and should be in the same group")
	}
}

func TestBuildSchedule_NoFileData(t *testing.T) {
	// Subtasks with empty file lists -> single group with all subtasks (fallback).
	subtasks := []model.Task{
		{ID: uuid.New(), Context: model.JSONField{}},
		{ID: uuid.New(), Context: model.JSONField{}},
		{ID: uuid.New(), Context: model.JSONField{}},
	}

	schedule := BuildSchedule(subtasks)

	if len(schedule.Groups) != 1 {
		t.Fatalf("expected 1 group (fallback), got %d", len(schedule.Groups))
	}
	if len(schedule.Groups[0].TaskIDs) != 3 {
		t.Errorf("expected 3 tasks in fallback group, got %d", len(schedule.Groups[0].TaskIDs))
	}
}

func TestBuildSchedule_ExplicitDependencies(t *testing.T) {
	// A has no file overlap with B, but B depends on A -> B must be in a later group.
	idA := uuid.New()
	idB := uuid.New()

	subtasks := []model.Task{
		{ID: idA, Context: model.JSONField{"estimated_files": []any{"a.go"}}},
		{
			ID:            idB,
			DependencyIDs: model.JSONArray{idA.String()},
			Context:       model.JSONField{"estimated_files": []any{"b.go"}},
		},
	}

	schedule := BuildSchedule(subtasks)

	if len(schedule.Groups) != 2 {
		t.Fatalf("expected 2 groups (dependency forces separation), got %d", len(schedule.Groups))
	}

	taskGroup := make(map[uuid.UUID]int)
	for _, g := range schedule.Groups {
		for _, id := range g.TaskIDs {
			taskGroup[id] = g.Order
		}
	}

	if taskGroup[idA] >= taskGroup[idB] {
		t.Errorf("A (group %d) should be before B (group %d) due to dependency",
			taskGroup[idA], taskGroup[idB])
	}
}

func TestBuildSchedule_MixedOverlapAndDependencies(t *testing.T) {
	// A overlaps B on file, C depends on A, C and B don't overlap.
	// -> A and B in different groups. C must come after A.
	idA := uuid.New()
	idB := uuid.New()
	idC := uuid.New()

	subtasks := []model.Task{
		{ID: idA, Context: model.JSONField{"estimated_files": []any{"shared.go", "a.go"}}},
		{ID: idB, Context: model.JSONField{"estimated_files": []any{"shared.go", "b.go"}}},
		{
			ID:            idC,
			DependencyIDs: model.JSONArray{idA.String()},
			Context:       model.JSONField{"estimated_files": []any{"c.go"}},
		},
	}

	schedule := BuildSchedule(subtasks)

	taskGroup := make(map[uuid.UUID]int)
	for _, g := range schedule.Groups {
		for _, id := range g.TaskIDs {
			taskGroup[id] = g.Order
		}
	}

	// A and B must be in different groups (file overlap).
	if taskGroup[idA] == taskGroup[idB] {
		t.Error("A and B overlap and should be in different groups")
	}

	// C must come after A (dependency).
	if taskGroup[idC] <= taskGroup[idA] {
		t.Errorf("C (group %d) should be after A (group %d) due to dependency",
			taskGroup[idC], taskGroup[idA])
	}
}

func TestBuildSchedule_Empty(t *testing.T) {
	schedule := BuildSchedule(nil)
	if len(schedule.Groups) != 0 {
		t.Errorf("expected 0 groups for empty input, got %d", len(schedule.Groups))
	}
}

func TestBuildSchedule_SingleTask(t *testing.T) {
	subtasks := []model.Task{
		{ID: uuid.New(), Context: model.JSONField{"estimated_files": []any{"a.go"}}},
	}

	schedule := BuildSchedule(subtasks)

	if len(schedule.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(schedule.Groups))
	}
	if len(schedule.Groups[0].TaskIDs) != 1 {
		t.Errorf("expected 1 task in group, got %d", len(schedule.Groups[0].TaskIDs))
	}
}

func TestBuildSchedule_NilContext(t *testing.T) {
	// Subtasks with nil context -> fallback single group.
	subtasks := []model.Task{
		{ID: uuid.New()},
		{ID: uuid.New()},
	}

	schedule := BuildSchedule(subtasks)

	if len(schedule.Groups) != 1 {
		t.Fatalf("expected 1 group (fallback), got %d", len(schedule.Groups))
	}
	if len(schedule.Groups[0].TaskIDs) != 2 {
		t.Errorf("expected 2 tasks in fallback group, got %d", len(schedule.Groups[0].TaskIDs))
	}
}

func TestBuildSchedule_ChainDependencies(t *testing.T) {
	// A -> B -> C chain dependency, no file overlap.
	// Must produce 3 groups in order.
	idA := uuid.New()
	idB := uuid.New()
	idC := uuid.New()

	subtasks := []model.Task{
		{ID: idA, Context: model.JSONField{"estimated_files": []any{"a.go"}}},
		{
			ID:            idB,
			DependencyIDs: model.JSONArray{idA.String()},
			Context:       model.JSONField{"estimated_files": []any{"b.go"}},
		},
		{
			ID:            idC,
			DependencyIDs: model.JSONArray{idB.String()},
			Context:       model.JSONField{"estimated_files": []any{"c.go"}},
		},
	}

	schedule := BuildSchedule(subtasks)

	if len(schedule.Groups) != 3 {
		t.Fatalf("expected 3 groups for chain dependency, got %d", len(schedule.Groups))
	}

	taskGroup := make(map[uuid.UUID]int)
	for _, g := range schedule.Groups {
		for _, id := range g.TaskIDs {
			taskGroup[id] = g.Order
		}
	}

	if taskGroup[idA] >= taskGroup[idB] {
		t.Errorf("A (group %d) should be before B (group %d)", taskGroup[idA], taskGroup[idB])
	}
	if taskGroup[idB] >= taskGroup[idC] {
		t.Errorf("B (group %d) should be before C (group %d)", taskGroup[idB], taskGroup[idC])
	}
}

func TestBuildSchedule_EstimatedFilesAsStrings(t *testing.T) {
	// Test with []string type (as it might come from direct Go construction).
	subtasks := []model.Task{
		{ID: uuid.New(), Context: model.JSONField{"estimated_files": []string{"shared.go"}}},
		{ID: uuid.New(), Context: model.JSONField{"estimated_files": []string{"shared.go"}}},
	}

	schedule := BuildSchedule(subtasks)

	if len(schedule.Groups) != 2 {
		t.Fatalf("expected 2 groups (overlap), got %d", len(schedule.Groups))
	}
}

func TestExtractEstimatedFiles(t *testing.T) {
	tests := []struct {
		name     string
		task     model.Task
		expected int
	}{
		{
			name:     "nil context",
			task:     model.Task{},
			expected: 0,
		},
		{
			name:     "no estimated_files key",
			task:     model.Task{Context: model.JSONField{"other": "data"}},
			expected: 0,
		},
		{
			name:     "interface slice",
			task:     model.Task{Context: model.JSONField{"estimated_files": []any{"a.go", "b.go"}}},
			expected: 2,
		},
		{
			name:     "string slice",
			task:     model.Task{Context: model.JSONField{"estimated_files": []string{"a.go"}}},
			expected: 1,
		},
		{
			name:     "non-slice value",
			task:     model.Task{Context: model.JSONField{"estimated_files": "not-a-slice"}},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := extractEstimatedFiles(tt.task)
			if len(files) != tt.expected {
				t.Errorf("expected %d files, got %d: %v", tt.expected, len(files), files)
			}
		})
	}
}

func TestScheduleSerializationRoundTrip(t *testing.T) {
	// Verify the schedule can be serialized and deserialized correctly,
	// which is critical for storage in parent.Context and retrieval
	// by findCurrentGroup.
	id1 := uuid.New()
	id2 := uuid.New()
	id3 := uuid.New()

	subtasks := []model.Task{
		{ID: id1, Context: model.JSONField{"estimated_files": []any{"shared.go"}}},
		{ID: id2, Context: model.JSONField{"estimated_files": []any{"shared.go"}}},
		{ID: id3, Context: model.JSONField{"estimated_files": []any{"other.go"}}},
	}

	schedule := BuildSchedule(subtasks)

	data, err := json.Marshal(schedule)
	if err != nil {
		t.Fatalf("marshal schedule: %v", err)
	}

	var parsed Schedule
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal schedule: %v", err)
	}

	if len(parsed.Groups) != len(schedule.Groups) {
		t.Fatalf("expected %d groups after round-trip, got %d",
			len(schedule.Groups), len(parsed.Groups))
	}

	// Verify all task IDs are preserved.
	totalTasks := 0
	for _, g := range parsed.Groups {
		totalTasks += len(g.TaskIDs)
	}
	if totalTasks != 3 {
		t.Errorf("expected 3 total tasks after round-trip, got %d", totalTasks)
	}

	// Verify group ordering is preserved.
	for i, g := range parsed.Groups {
		if g.Order != schedule.Groups[i].Order {
			t.Errorf("group %d: order mismatch, got %d want %d",
				i, g.Order, schedule.Groups[i].Order)
		}
	}
}

func TestScheduleContextRoundTrip(t *testing.T) {
	// Simulate the full round-trip through JSONField (as used in orchestrator.go):
	// BuildSchedule -> json.Marshal -> json.Unmarshal into any -> store in JSONField
	// -> json.Marshal from JSONField -> json.Unmarshal back to Schedule
	id1 := uuid.New()
	id2 := uuid.New()

	subtasks := []model.Task{
		{ID: id1, Context: model.JSONField{"estimated_files": []any{"shared.go"}}},
		{ID: id2, Context: model.JSONField{"estimated_files": []any{"shared.go"}}},
	}

	schedule := BuildSchedule(subtasks)

	// Marshal to JSON (as done in HandlePlanApproved).
	scheduleJSON, err := json.Marshal(schedule)
	if err != nil {
		t.Fatalf("marshal schedule: %v", err)
	}

	// Unmarshal into generic any (as stored in JSONField).
	var scheduleField any
	if err := json.Unmarshal(scheduleJSON, &scheduleField); err != nil {
		t.Fatalf("unmarshal to any: %v", err)
	}

	// Store in a JSONField context map.
	ctx := model.JSONField{"schedule": scheduleField}

	// Re-marshal from context (as done in scheduleSubtasks).
	raw := ctx["schedule"]
	reJSON, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("re-marshal from context: %v", err)
	}

	// Parse back to Schedule.
	var parsed Schedule
	if err := json.Unmarshal(reJSON, &parsed); err != nil {
		t.Fatalf("unmarshal back to Schedule: %v", err)
	}

	if len(parsed.Groups) != 2 {
		t.Fatalf("expected 2 groups after context round-trip, got %d", len(parsed.Groups))
	}

	// Verify each group has exactly 1 task (full overlap -> sequential).
	for i, g := range parsed.Groups {
		if len(g.TaskIDs) != 1 {
			t.Errorf("group %d: expected 1 task, got %d", i, len(g.TaskIDs))
		}
	}
}
