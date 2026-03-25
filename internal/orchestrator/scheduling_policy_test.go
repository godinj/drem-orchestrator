package orchestrator

import (
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// ComputeConflictScore tests
// ---------------------------------------------------------------------------

func TestComputeConflictScore(t *testing.T) {
	tests := []struct {
		name  string
		a     []string
		b     []string
		want  float64
		delta float64 // tolerance for float comparison
	}{
		{
			name:  "no overlap",
			a:     []string{"a.go", "b.go"},
			b:     []string{"c.go", "d.go"},
			want:  0.0,
			delta: 0.001,
		},
		{
			name:  "complete overlap identical lists",
			a:     []string{"a.go", "b.go"},
			b:     []string{"a.go", "b.go"},
			want:  1.0,
			delta: 0.001,
		},
		{
			name:  "complete overlap subset",
			a:     []string{"a.go"},
			b:     []string{"a.go", "b.go", "c.go"},
			want:  1.0, // 1/min(1,3) = 1.0
			delta: 0.001,
		},
		{
			name:  "partial overlap",
			a:     []string{"a.go", "b.go", "c.go"},
			b:     []string{"b.go", "d.go", "e.go"},
			want:  1.0 / 3.0, // 1/min(3,3) ≈ 0.333
			delta: 0.001,
		},
		{
			name:  "both empty",
			a:     []string{},
			b:     []string{},
			want:  0.0,
			delta: 0.001,
		},
		{
			name:  "one empty",
			a:     []string{"a.go"},
			b:     []string{},
			want:  0.0,
			delta: 0.001,
		},
		{
			name:  "nil slices",
			a:     nil,
			b:     nil,
			want:  0.0,
			delta: 0.001,
		},
		{
			name:  "half overlap in smaller list",
			a:     []string{"a.go", "b.go"},
			b:     []string{"a.go", "c.go", "d.go", "e.go"},
			want:  0.5, // 1/min(2,4) = 0.5
			delta: 0.001,
		},
		{
			name:  "duplicate files in one list counted once",
			a:     []string{"a.go", "a.go", "b.go"},
			b:     []string{"a.go"},
			want:  1.0, // 1/min(1,1) = 1.0 (unique files: a has {a.go, b.go}, b has {a.go})
			delta: 0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeConflictScore(tt.a, tt.b)
			if got < tt.want-tt.delta || got > tt.want+tt.delta {
				t.Errorf("ComputeConflictScore(%v, %v) = %f, want %f (±%f)",
					tt.a, tt.b, got, tt.want, tt.delta)
			}
		})
	}
}

func TestComputeConflictScore_Symmetry(t *testing.T) {
	a := []string{"x.go", "y.go", "z.go"}
	b := []string{"y.go", "w.go"}

	scoreAB := ComputeConflictScore(a, b)
	scoreBA := ComputeConflictScore(b, a)

	if scoreAB != scoreBA {
		t.Errorf("score is not symmetric: (%v,%v)=%f vs (%v,%v)=%f",
			a, b, scoreAB, b, a, scoreBA)
	}
}

// ---------------------------------------------------------------------------
// fileOverlapDetails tests
// ---------------------------------------------------------------------------

func TestFileOverlapDetails(t *testing.T) {
	tests := []struct {
		name         string
		a            []string
		b            []string
		wantFiles    []string
		wantMinScore float64
	}{
		{
			name:         "shared files returned",
			a:            []string{"a.go", "b.go", "c.go"},
			b:            []string{"b.go", "c.go", "d.go"},
			wantFiles:    []string{"b.go", "c.go"},
			wantMinScore: 0.6, // 2/min(3,3) ≈ 0.667
		},
		{
			name:         "no overlap returns empty",
			a:            []string{"a.go"},
			b:            []string{"b.go"},
			wantFiles:    nil,
			wantMinScore: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, score := fileOverlapDetails(tt.a, tt.b)

			if tt.wantFiles == nil {
				if len(files) != 0 {
					t.Errorf("expected no overlap files, got %v", files)
				}
			} else {
				gotSet := make(map[string]bool, len(files))
				for _, f := range files {
					gotSet[f] = true
				}
				for _, want := range tt.wantFiles {
					if !gotSet[want] {
						t.Errorf("expected overlap file %q not found in %v", want, files)
					}
				}
			}

			if score < tt.wantMinScore {
				t.Errorf("score %f below expected minimum %f", score, tt.wantMinScore)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// EvaluateDispatch tests
// ---------------------------------------------------------------------------

func TestEvaluateDispatch_NoCandidates(t *testing.T) {
	db := testutil.NewTestDB(t)
	sp := NewSchedulingPolicy(db)

	decisions := sp.EvaluateDispatch(nil, nil)
	if len(decisions) != 0 {
		t.Errorf("expected 0 decisions for nil candidates, got %d", len(decisions))
	}
}

func TestEvaluateDispatch_NoInProgress_AllDispatchable(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/fake", "master")

	// Create two candidate tasks with no overlap, no dependencies, no in-progress tasks.
	taskA := testutil.CreateTask(t, db, proj.ID, "task-a", model.StatusBacklog)
	taskB := testutil.CreateTask(t, db, proj.ID, "task-b", model.StatusBacklog)

	// Set estimated_files on both.
	db.Model(&taskA).Update("context", model.JSONField{"estimated_files": []any{"a.go"}})
	db.Model(&taskB).Update("context", model.JSONField{"estimated_files": []any{"b.go"}})
	db.First(&taskA, "id = ?", taskA.ID)
	db.First(&taskB, "id = ?", taskB.ID)

	sp := NewSchedulingPolicy(db)
	decisions := sp.EvaluateDispatch([]model.Task{taskA, taskB}, nil)

	if len(decisions) != 2 {
		t.Fatalf("expected 2 decisions, got %d", len(decisions))
	}
	for _, d := range decisions {
		if !d.Dispatchable {
			t.Errorf("task %s should be dispatchable with no in-progress tasks, reason: %s",
				d.TaskID, d.Reason)
		}
	}
}

func TestEvaluateDispatch_FileConflictBlocks(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/fake", "master")

	// In-progress task touching shared.go
	inProgressTask := testutil.CreateTask(t, db, proj.ID, "in-progress", model.StatusInProgress)
	db.Model(&inProgressTask).Update("context", model.JSONField{
		"estimated_files": []any{"shared.go", "common.go"},
	})
	db.First(&inProgressTask, "id = ?", inProgressTask.ID)

	// Candidate task also touching shared.go -> should be blocked
	candidate := testutil.CreateTask(t, db, proj.ID, "candidate", model.StatusBacklog)
	db.Model(&candidate).Update("context", model.JSONField{
		"estimated_files": []any{"shared.go", "other.go"},
	})
	db.First(&candidate, "id = ?", candidate.ID)

	sp := NewSchedulingPolicy(db)
	decisions := sp.EvaluateDispatch(
		[]model.Task{candidate},
		[]model.Task{inProgressTask},
	)

	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}

	d := decisions[0]
	if d.Dispatchable {
		t.Error("task with file overlap should NOT be dispatchable")
	}
	if len(d.Conflicts) == 0 {
		t.Fatal("blocked task should have conflict details")
	}
	if d.Conflicts[0].BlockingTaskID != inProgressTask.ID {
		t.Errorf("conflict should reference blocking task %s, got %s",
			inProgressTask.ID, d.Conflicts[0].BlockingTaskID)
	}
}

func TestEvaluateDispatch_LowOverlapAllowed(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/fake", "master")

	// In-progress task touching many files.
	inProgressTask := testutil.CreateTask(t, db, proj.ID, "in-progress", model.StatusInProgress)
	db.Model(&inProgressTask).Update("context", model.JSONField{
		"estimated_files": []any{"a.go", "b.go", "c.go", "d.go", "e.go"},
	})
	db.First(&inProgressTask, "id = ?", inProgressTask.ID)

	// Candidate shares only 1 of 5 files -> score = 1/5 = 0.2, below threshold.
	candidate := testutil.CreateTask(t, db, proj.ID, "candidate", model.StatusBacklog)
	db.Model(&candidate).Update("context", model.JSONField{
		"estimated_files": []any{"a.go", "x.go", "y.go", "z.go", "w.go"},
	})
	db.First(&candidate, "id = ?", candidate.ID)

	sp := NewSchedulingPolicy(db)
	decisions := sp.EvaluateDispatch(
		[]model.Task{candidate},
		[]model.Task{inProgressTask},
	)

	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if !decisions[0].Dispatchable {
		t.Errorf("low overlap (1/5) should be dispatchable, reason: %s", decisions[0].Reason)
	}
}

func TestEvaluateDispatch_DependencyNotMet(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/fake", "master")

	// Dependency task not done yet.
	depTask := testutil.CreateTask(t, db, proj.ID, "dependency", model.StatusInProgress)

	// Candidate depends on depTask.
	candidate := testutil.CreateTask(t, db, proj.ID, "candidate", model.StatusBacklog)
	db.Model(&candidate).Updates(map[string]any{
		"dependency_ids": model.JSONArray{depTask.ID.String()},
		"context":        model.JSONField{"estimated_files": []any{"unique.go"}},
	})
	db.First(&candidate, "id = ?", candidate.ID)

	sp := NewSchedulingPolicy(db)
	decisions := sp.EvaluateDispatch([]model.Task{candidate}, nil)

	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if decisions[0].Dispatchable {
		t.Error("task with unmet dependency should NOT be dispatchable")
	}
}

func TestEvaluateDispatch_DependencyMet(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/fake", "master")

	// Dependency task is done.
	depTask := testutil.CreateTask(t, db, proj.ID, "dependency", model.StatusDone)

	// Candidate depends on depTask, no file conflicts.
	candidate := testutil.CreateTask(t, db, proj.ID, "candidate", model.StatusBacklog)
	db.Model(&candidate).Updates(map[string]any{
		"dependency_ids": model.JSONArray{depTask.ID.String()},
		"context":        model.JSONField{"estimated_files": []any{"unique.go"}},
	})
	db.First(&candidate, "id = ?", candidate.ID)

	sp := NewSchedulingPolicy(db)
	decisions := sp.EvaluateDispatch([]model.Task{candidate}, nil)

	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if !decisions[0].Dispatchable {
		t.Errorf("task with met dependency should be dispatchable, reason: %s",
			decisions[0].Reason)
	}
}

func TestEvaluateDispatch_MultipleConflicts_WorstScoreWins(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/fake", "master")

	// Two in-progress tasks.
	ipA := testutil.CreateTask(t, db, proj.ID, "ip-a", model.StatusInProgress)
	db.Model(&ipA).Update("context", model.JSONField{
		"estimated_files": []any{"shared.go"},
	})
	db.First(&ipA, "id = ?", ipA.ID)

	ipB := testutil.CreateTask(t, db, proj.ID, "ip-b", model.StatusInProgress)
	db.Model(&ipB).Update("context", model.JSONField{
		"estimated_files": []any{"other.go"},
	})
	db.First(&ipB, "id = ?", ipB.ID)

	// Candidate overlaps fully with ipA (shared.go) but not with ipB.
	candidate := testutil.CreateTask(t, db, proj.ID, "candidate", model.StatusBacklog)
	db.Model(&candidate).Update("context", model.JSONField{
		"estimated_files": []any{"shared.go"},
	})
	db.First(&candidate, "id = ?", candidate.ID)

	sp := NewSchedulingPolicy(db)
	decisions := sp.EvaluateDispatch(
		[]model.Task{candidate},
		[]model.Task{ipA, ipB},
	)

	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if decisions[0].Dispatchable {
		t.Error("candidate with high overlap against any in-progress task should be blocked")
	}
	// Should report conflict with ipA specifically.
	foundConflict := false
	for _, c := range decisions[0].Conflicts {
		if c.BlockingTaskID == ipA.ID {
			foundConflict = true
			if c.Score < conflictScoreBlockThreshold {
				t.Errorf("conflict score %f should be >= threshold %f",
					c.Score, conflictScoreBlockThreshold)
			}
		}
	}
	if !foundConflict {
		t.Errorf("expected conflict with task %s in details", ipA.ID)
	}
}

func TestEvaluateDispatch_MixedDecisions(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/fake", "master")

	// One in-progress task touching shared.go.
	ipTask := testutil.CreateTask(t, db, proj.ID, "in-progress", model.StatusInProgress)
	db.Model(&ipTask).Update("context", model.JSONField{
		"estimated_files": []any{"shared.go"},
	})
	db.First(&ipTask, "id = ?", ipTask.ID)

	// Candidate A: conflicts with in-progress (shared.go).
	candA := testutil.CreateTask(t, db, proj.ID, "cand-a", model.StatusBacklog)
	db.Model(&candA).Update("context", model.JSONField{
		"estimated_files": []any{"shared.go"},
	})
	db.First(&candA, "id = ?", candA.ID)

	// Candidate B: no conflicts (unique files).
	candB := testutil.CreateTask(t, db, proj.ID, "cand-b", model.StatusBacklog)
	db.Model(&candB).Update("context", model.JSONField{
		"estimated_files": []any{"unique.go"},
	})
	db.First(&candB, "id = ?", candB.ID)

	sp := NewSchedulingPolicy(db)
	decisions := sp.EvaluateDispatch(
		[]model.Task{candA, candB},
		[]model.Task{ipTask},
	)

	if len(decisions) != 2 {
		t.Fatalf("expected 2 decisions, got %d", len(decisions))
	}

	decisionMap := make(map[uuid.UUID]DispatchDecision)
	for _, d := range decisions {
		decisionMap[d.TaskID] = d
	}

	if decisionMap[candA.ID].Dispatchable {
		t.Error("candidate A (conflict) should NOT be dispatchable")
	}
	if !decisionMap[candB.ID].Dispatchable {
		t.Errorf("candidate B (no conflict) should be dispatchable, reason: %s",
			decisionMap[candB.ID].Reason)
	}
}

func TestEvaluateDispatch_EmptyFileLists_Conservative(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/fake", "master")

	// In-progress task with no file data.
	ipTask := testutil.CreateTask(t, db, proj.ID, "in-progress", model.StatusInProgress)

	// Candidate with no file data.
	candidate := testutil.CreateTask(t, db, proj.ID, "candidate", model.StatusBacklog)

	sp := NewSchedulingPolicy(db)
	decisions := sp.EvaluateDispatch(
		[]model.Task{candidate},
		[]model.Task{ipTask},
	)

	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	// When we have no file data, the task should still be dispatchable
	// (backward compat — same as BuildSchedule's single-group fallback).
	if !decisions[0].Dispatchable {
		t.Errorf("task with no file data should be dispatchable (backward compat), reason: %s",
			decisions[0].Reason)
	}
}

func TestEvaluateDispatch_PreservesOrder(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/fake", "master")

	tasks := make([]model.Task, 5)
	for i := range tasks {
		tasks[i] = testutil.CreateTask(t, db, proj.ID, "task", model.StatusBacklog)
		db.Model(&tasks[i]).Update("context", model.JSONField{
			"estimated_files": []any{"unique_" + tasks[i].ID.String() + ".go"},
		})
		db.First(&tasks[i], "id = ?", tasks[i].ID)
	}

	sp := NewSchedulingPolicy(db)
	decisions := sp.EvaluateDispatch(tasks, nil)

	if len(decisions) != 5 {
		t.Fatalf("expected 5 decisions, got %d", len(decisions))
	}
	for i, d := range decisions {
		if d.TaskID != tasks[i].ID {
			t.Errorf("decision[%d] task ID %s != input task ID %s",
				i, d.TaskID, tasks[i].ID)
		}
	}
}

func TestEvaluateDispatch_WaveGroupBlocking(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/fake", "master")

	// Parent task with a schedule: group 0 = [taskA], group 1 = [taskB].
	parent := testutil.CreateTask(t, db, proj.ID, "parent", model.StatusInProgress)

	taskA := testutil.CreateTask(t, db, proj.ID, "task-a", model.StatusInProgress)
	taskB := testutil.CreateTask(t, db, proj.ID, "task-b", model.StatusBacklog)

	// Set parent IDs.
	db.Model(&taskA).Update("parent_task_id", parent.ID)
	db.Model(&taskB).Update("parent_task_id", parent.ID)

	// Store wave schedule in parent context.
	schedule := Schedule{
		Groups: []SubtaskGroup{
			{Order: 0, TaskIDs: []uuid.UUID{taskA.ID}},
			{Order: 1, TaskIDs: []uuid.UUID{taskB.ID}},
		},
	}
	db.Model(&parent).Update("context", model.JSONField{
		"schedule": marshalScheduleForContext(schedule),
	})

	db.First(&taskA, "id = ?", taskA.ID)
	db.First(&taskB, "id = ?", taskB.ID)

	sp := NewSchedulingPolicy(db)

	// taskB is in group 1, but taskA (group 0) is still in-progress (not done).
	// taskB should be blocked.
	decisions := sp.EvaluateDispatch(
		[]model.Task{taskB},
		[]model.Task{taskA},
	)

	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if decisions[0].Dispatchable {
		t.Error("task in later wave group should NOT be dispatchable while earlier group is active")
	}
}

func TestEvaluateDispatch_ConflictDetailContainsFiles(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/fake", "master")

	ipTask := testutil.CreateTask(t, db, proj.ID, "in-progress", model.StatusInProgress)
	db.Model(&ipTask).Update("context", model.JSONField{
		"estimated_files": []any{"shared1.go", "shared2.go", "only_ip.go"},
	})
	db.First(&ipTask, "id = ?", ipTask.ID)

	candidate := testutil.CreateTask(t, db, proj.ID, "candidate", model.StatusBacklog)
	db.Model(&candidate).Update("context", model.JSONField{
		"estimated_files": []any{"shared1.go", "shared2.go", "only_cand.go"},
	})
	db.First(&candidate, "id = ?", candidate.ID)

	sp := NewSchedulingPolicy(db)
	decisions := sp.EvaluateDispatch(
		[]model.Task{candidate},
		[]model.Task{ipTask},
	)

	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if decisions[0].Dispatchable {
		t.Fatal("expected task to be blocked due to high overlap")
	}

	if len(decisions[0].Conflicts) == 0 {
		t.Fatal("expected conflict details")
	}

	conflict := decisions[0].Conflicts[0]
	overlapSet := make(map[string]bool, len(conflict.OverlapFiles))
	for _, f := range conflict.OverlapFiles {
		overlapSet[f] = true
	}

	if !overlapSet["shared1.go"] || !overlapSet["shared2.go"] {
		t.Errorf("expected overlap files to contain shared1.go and shared2.go, got %v",
			conflict.OverlapFiles)
	}
	if overlapSet["only_ip.go"] || overlapSet["only_cand.go"] {
		t.Errorf("overlap files should not contain non-shared files, got %v",
			conflict.OverlapFiles)
	}
}

// ---------------------------------------------------------------------------
// activeTaskFiles tests
// ---------------------------------------------------------------------------

func TestActiveTaskFiles_ReturnsInProgressTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/fake", "master")

	// In-progress task with files.
	ipTask := testutil.CreateTask(t, db, proj.ID, "in-progress", model.StatusInProgress)
	db.Model(&ipTask).Update("context", model.JSONField{
		"estimated_files": []any{"a.go", "b.go"},
	})

	// Done task (should NOT appear).
	testutil.CreateTask(t, db, proj.ID, "done", model.StatusDone)

	// Backlog task (should NOT appear).
	testutil.CreateTask(t, db, proj.ID, "backlog", model.StatusBacklog)

	sp := NewSchedulingPolicy(db)
	files, err := sp.activeTaskFiles()
	if err != nil {
		t.Fatalf("activeTaskFiles error: %v", err)
	}

	if _, ok := files[ipTask.ID]; !ok {
		t.Errorf("expected in-progress task %s in results", ipTask.ID)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 active task, got %d", len(files))
	}
}

func TestActiveTaskFiles_IncludesMergingTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/fake", "master")

	mergingTask := testutil.CreateTask(t, db, proj.ID, "merging", model.StatusMerging)
	db.Model(&mergingTask).Update("context", model.JSONField{
		"estimated_files": []any{"x.go"},
	})

	sp := NewSchedulingPolicy(db)
	files, err := sp.activeTaskFiles()
	if err != nil {
		t.Fatalf("activeTaskFiles error: %v", err)
	}

	if _, ok := files[mergingTask.ID]; !ok {
		t.Error("merging tasks should be included in active task files")
	}
}

// ---------------------------------------------------------------------------
// Integration: SchedulingPolicy + BuildSchedule coherence
// ---------------------------------------------------------------------------

func TestSchedulingPolicy_CoherentWithBuildSchedule(t *testing.T) {
	// Verify that EvaluateDispatch respects the same file-overlap semantics
	// as BuildSchedule: if BuildSchedule puts two tasks in different groups,
	// EvaluateDispatch should block the later one while the earlier runs.
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/fake", "master")

	idA := uuid.New()
	idB := uuid.New()

	subtasks := []model.Task{
		{ID: idA, Context: model.JSONField{"estimated_files": []any{"shared.go"}}},
		{ID: idB, Context: model.JSONField{"estimated_files": []any{"shared.go"}}},
	}

	schedule := BuildSchedule(subtasks)
	if len(schedule.Groups) < 2 {
		t.Fatal("BuildSchedule should separate overlapping tasks into 2+ groups")
	}

	// Create real tasks in DB to simulate runtime state.
	taskA := testutil.CreateTask(t, db, proj.ID, "task-a", model.StatusInProgress)
	db.Model(&taskA).Update("context", model.JSONField{
		"estimated_files": []any{"shared.go"},
	})
	db.First(&taskA, "id = ?", taskA.ID)

	taskB := testutil.CreateTask(t, db, proj.ID, "task-b", model.StatusBacklog)
	db.Model(&taskB).Update("context", model.JSONField{
		"estimated_files": []any{"shared.go"},
	})
	db.First(&taskB, "id = ?", taskB.ID)

	sp := NewSchedulingPolicy(db)
	decisions := sp.EvaluateDispatch(
		[]model.Task{taskB},
		[]model.Task{taskA},
	)

	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if decisions[0].Dispatchable {
		t.Error("EvaluateDispatch should block task with file overlap, consistent with BuildSchedule")
	}
}

// ---------------------------------------------------------------------------
// Helper: marshalScheduleForContext
// ---------------------------------------------------------------------------

// marshalScheduleForContext converts a Schedule to the any representation
// stored in JSONField, simulating what HandlePlanApproved does.
func marshalScheduleForContext(s Schedule) any {
	// Convert to map[string]any for JSONField storage.
	groups := make([]any, len(s.Groups))
	for i, g := range s.Groups {
		ids := make([]any, len(g.TaskIDs))
		for j, id := range g.TaskIDs {
			ids[j] = id.String()
		}
		groups[i] = map[string]any{
			"order":    float64(g.Order),
			"task_ids": ids,
		}
	}
	return map[string]any{"groups": groups}
}
