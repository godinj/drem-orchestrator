package experiment

import (
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func TestCreateExperimentTwoVariants(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "exp-proj", "/tmp/exp.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Two-arm test", "Compare profiles", []string{"fast", "thorough"}, "fast")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	if exp.Title != "Two-arm test" {
		t.Errorf("expected title 'Two-arm test', got %q", exp.Title)
	}
	if exp.Status != StatusPending {
		t.Errorf("expected status pending, got %q", exp.Status)
	}
	if len(exp.Variants) != 2 {
		t.Fatalf("expected 2 variants, got %d", len(exp.Variants))
	}
}

func TestCreateExperimentThreeVariants(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "exp-proj", "/tmp/exp.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Three-arm test", "Compare three", []string{"alpha", "beta", "gamma"}, "beta")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	if len(exp.Variants) != 3 {
		t.Fatalf("expected 3 variants, got %d", len(exp.Variants))
	}
}

func TestCreateExperimentTooFewProfiles(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "exp-proj", "/tmp/exp.git", "master")

	_, err := CreateExperiment(db, proj.ID, "One arm", "desc", []string{"solo"}, "solo")
	if err == nil {
		t.Fatal("expected error for <2 profiles, got nil")
	}
}

func TestCreateExperimentTooManyProfiles(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "exp-proj", "/tmp/exp.git", "master")

	_, err := CreateExperiment(db, proj.ID, "Four arms", "desc", []string{"a", "b", "c", "d"}, "a")
	if err == nil {
		t.Fatal("expected error for >3 profiles, got nil")
	}
}

func TestCreateExperimentDefaultNotInProfiles(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "exp-proj", "/tmp/exp.git", "master")

	_, err := CreateExperiment(db, proj.ID, "Bad default", "desc", []string{"x", "y"}, "z")
	if err == nil {
		t.Fatal("expected error for default not in profiles, got nil")
	}
}

func TestCreatedTasksAreBacklog(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "exp-proj", "/tmp/exp.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Backlog check", "desc", []string{"p1", "p2"}, "p1")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	for _, v := range exp.Variants {
		var task model.Task
		if err := db.First(&task, "id = ?", v.TaskID).Error; err != nil {
			t.Fatalf("find task for variant %s: %v", v.ProfileName, err)
		}
		if task.Status != model.StatusBacklog {
			t.Errorf("variant %s: expected task status backlog, got %q", v.ProfileName, task.Status)
		}
	}
}

func TestIsDefaultCorrectlySet(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "exp-proj", "/tmp/exp.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Default check", "desc", []string{"primary", "secondary", "tertiary"}, "secondary")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	defaultCount := 0
	for _, v := range exp.Variants {
		if v.IsDefault {
			defaultCount++
			if v.ProfileName != "secondary" {
				t.Errorf("expected default variant to be 'secondary', got %q", v.ProfileName)
			}
		}
	}
	if defaultCount != 1 {
		t.Errorf("expected exactly 1 default variant, got %d", defaultCount)
	}
}

func TestExperimentProjectIDLinked(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "exp-proj", "/tmp/exp.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Project link", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	if exp.ProjectID != proj.ID {
		t.Errorf("expected project ID %s, got %s", proj.ID, exp.ProjectID)
	}

	// Verify tasks also belong to the same project.
	for _, v := range exp.Variants {
		var task model.Task
		if err := db.First(&task, "id = ?", v.TaskID).Error; err != nil {
			t.Fatalf("find task: %v", err)
		}
		if task.ProjectID != proj.ID {
			t.Errorf("task %s: expected project ID %s, got %s", task.ID, proj.ID, task.ProjectID)
		}
	}
}

func TestVariantTaskIDNotNil(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "exp-proj", "/tmp/exp.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "TaskID check", "desc", []string{"m", "n"}, "m")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	for _, v := range exp.Variants {
		if v.TaskID == uuid.Nil {
			t.Errorf("variant %s: expected non-nil TaskID", v.ProfileName)
		}
	}
}

// ---------------------------------------------------------------------------
// CreateFromTask tests
// ---------------------------------------------------------------------------

func TestCreateFromTaskHappyPathNoReusePlan(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "from-task-proj", "/tmp/from-task.git", "master")
	src := testutil.CreateTask(t, db, proj.ID, "Source task", model.StatusDone)

	exp, err := CreateFromTask(db, proj.ID, src.ID, "From-task experiment", "desc", []string{"fast", "thorough"}, "fast", false)
	if err != nil {
		t.Fatalf("CreateFromTask: %v", err)
	}

	// Experiment fields.
	if exp.SourceTaskID == nil || *exp.SourceTaskID != src.ID {
		t.Errorf("expected SourceTaskID=%s, got %v", src.ID, exp.SourceTaskID)
	}
	if exp.ProjectID != proj.ID {
		t.Errorf("expected ProjectID=%s, got %s", proj.ID, exp.ProjectID)
	}
	if len(exp.Variants) != 2 {
		t.Fatalf("expected 2 variants, got %d", len(exp.Variants))
	}

	// Variants and tasks.
	for _, v := range exp.Variants {
		if v.ReusesPlan {
			t.Errorf("variant %s: expected ReusesPlan=false", v.ProfileName)
		}
		var task model.Task
		if err := db.First(&task, "id = ?", v.TaskID).Error; err != nil {
			t.Fatalf("find task for variant %s: %v", v.ProfileName, err)
		}
		if task.Status != model.StatusBacklog {
			t.Errorf("variant %s: expected task status backlog, got %q", v.ProfileName, task.Status)
		}
	}
}

func TestCreateFromTaskHappyPathReusePlan(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "from-task-reuse-proj", "/tmp/from-task-reuse.git", "master")
	src := testutil.CreateTask(t, db, proj.ID, "Source task with plan", model.StatusDone)

	// Set a plan on the source task.
	planData := model.JSONField{"steps": []any{"step1", "step2"}}
	if err := db.Model(&src).Update("plan", planData).Error; err != nil {
		t.Fatalf("update source task plan: %v", err)
	}
	src.Plan = planData

	exp, err := CreateFromTask(db, proj.ID, src.ID, "Reuse-plan experiment", "desc", []string{"alpha", "beta"}, "alpha", true)
	if err != nil {
		t.Fatalf("CreateFromTask with reuse-plan: %v", err)
	}

	if exp.SourceTaskID == nil || *exp.SourceTaskID != src.ID {
		t.Errorf("expected SourceTaskID=%s, got %v", src.ID, exp.SourceTaskID)
	}
	if len(exp.Variants) != 2 {
		t.Fatalf("expected 2 variants, got %d", len(exp.Variants))
	}

	for _, v := range exp.Variants {
		if !v.ReusesPlan {
			t.Errorf("variant %s: expected ReusesPlan=true", v.ProfileName)
		}
		var task model.Task
		if err := db.First(&task, "id = ?", v.TaskID).Error; err != nil {
			t.Fatalf("find task for variant %s: %v", v.ProfileName, err)
		}
		if task.Status != model.StatusPlanReview {
			t.Errorf("variant %s: expected task status plan_review, got %q", v.ProfileName, task.Status)
		}
		// Verify the plan was copied: both should have the same "steps" key.
		if len(task.Plan) != len(src.Plan) {
			t.Errorf("variant %s: expected plan copied from source (len=%d), got len=%d", v.ProfileName, len(src.Plan), len(task.Plan))
		}
	}
}

func TestCreateFromTaskSourceNotDone(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "from-task-nodone-proj", "/tmp/nodone.git", "master")
	src := testutil.CreateTask(t, db, proj.ID, "In-progress task", model.StatusInProgress)

	_, err := CreateFromTask(db, proj.ID, src.ID, "Should fail", "desc", []string{"a", "b"}, "a", false)
	if err == nil {
		t.Fatal("expected error for source task not in StatusDone, got nil")
	}
}

func TestCreateFromTaskSourceNotFound(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "from-task-notfound-proj", "/tmp/notfound.git", "master")
	nonexistent := uuid.New()

	_, err := CreateFromTask(db, proj.ID, nonexistent, "Should fail", "desc", []string{"a", "b"}, "a", false)
	if err == nil {
		t.Fatal("expected error for nonexistent source task, got nil")
	}
}

func TestCreateFromTaskTooFewProfiles(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "from-task-fewprof-proj", "/tmp/fewprof.git", "master")
	src := testutil.CreateTask(t, db, proj.ID, "Done task", model.StatusDone)

	_, err := CreateFromTask(db, proj.ID, src.ID, "One profile", "desc", []string{"solo"}, "solo", false)
	if err == nil {
		t.Fatal("expected error for <2 profiles, got nil")
	}
}

func TestCreateFromTaskTooManyProfiles(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "from-task-manyprof-proj", "/tmp/manyprof.git", "master")
	src := testutil.CreateTask(t, db, proj.ID, "Done task", model.StatusDone)

	_, err := CreateFromTask(db, proj.ID, src.ID, "Four profiles", "desc", []string{"a", "b", "c", "d"}, "a", false)
	if err == nil {
		t.Fatal("expected error for >3 profiles, got nil")
	}
}

func TestCreateFromTaskDefaultNotInProfiles(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "from-task-baddefault-proj", "/tmp/baddefault.git", "master")
	src := testutil.CreateTask(t, db, proj.ID, "Done task", model.StatusDone)

	_, err := CreateFromTask(db, proj.ID, src.ID, "Bad default", "desc", []string{"x", "y"}, "z", false)
	if err == nil {
		t.Fatal("expected error for default not in profiles, got nil")
	}
}
