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

	opts := FromTaskOpts{
		Title:          "From-task experiment",
		Description:    "desc",
		Profiles:       []string{"fast", "thorough"},
		DefaultProfile: "fast",
		ReusePlan:      false,
	}
	exp, err := CreateFromTask(db, proj.ID, src.ID, opts)
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

	opts := FromTaskOpts{
		Title:          "Reuse-plan experiment",
		Description:    "desc",
		Profiles:       []string{"alpha", "beta"},
		DefaultProfile: "alpha",
		ReusePlan:      true,
	}
	exp, err := CreateFromTask(db, proj.ID, src.ID, opts)
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

	opts := FromTaskOpts{
		Title:          "Should fail",
		Description:    "desc",
		Profiles:       []string{"a", "b"},
		DefaultProfile: "a",
		ReusePlan:      false,
	}
	_, err := CreateFromTask(db, proj.ID, src.ID, opts)
	if err == nil {
		t.Fatal("expected error for source task not in StatusDone, got nil")
	}
}

func TestCreateFromTaskSourceNotFound(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "from-task-notfound-proj", "/tmp/notfound.git", "master")
	nonexistent := uuid.New()

	opts := FromTaskOpts{
		Title:          "Should fail",
		Description:    "desc",
		Profiles:       []string{"a", "b"},
		DefaultProfile: "a",
		ReusePlan:      false,
	}
	_, err := CreateFromTask(db, proj.ID, nonexistent, opts)
	if err == nil {
		t.Fatal("expected error for nonexistent source task, got nil")
	}
}

func TestCreateFromTaskTooFewProfiles(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "from-task-fewprof-proj", "/tmp/fewprof.git", "master")
	src := testutil.CreateTask(t, db, proj.ID, "Done task", model.StatusDone)

	opts := FromTaskOpts{
		Title:          "One profile",
		Description:    "desc",
		Profiles:       []string{"solo"},
		DefaultProfile: "solo",
		ReusePlan:      false,
	}
	_, err := CreateFromTask(db, proj.ID, src.ID, opts)
	if err == nil {
		t.Fatal("expected error for <2 profiles, got nil")
	}
}

func TestCreateFromTaskTooManyProfiles(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "from-task-manyprof-proj", "/tmp/manyprof.git", "master")
	src := testutil.CreateTask(t, db, proj.ID, "Done task", model.StatusDone)

	opts := FromTaskOpts{
		Title:          "Four profiles",
		Description:    "desc",
		Profiles:       []string{"a", "b", "c", "d"},
		DefaultProfile: "a",
		ReusePlan:      false,
	}
	_, err := CreateFromTask(db, proj.ID, src.ID, opts)
	if err == nil {
		t.Fatal("expected error for >3 profiles, got nil")
	}
}

func TestCreateFromTaskDefaultNotInProfiles(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "from-task-baddefault-proj", "/tmp/baddefault.git", "master")
	src := testutil.CreateTask(t, db, proj.ID, "Done task", model.StatusDone)

	opts := FromTaskOpts{
		Title:          "Bad default",
		Description:    "desc",
		Profiles:       []string{"x", "y"},
		DefaultProfile: "z",
		ReusePlan:      false,
	}
	_, err := CreateFromTask(db, proj.ID, src.ID, opts)
	if err == nil {
		t.Fatal("expected error for default not in profiles, got nil")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle State Machine Tests
// ---------------------------------------------------------------------------

func TestStartExperiment(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "lifecycle-proj", "/tmp/lifecycle.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Lifecycle test", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	err = StartExperiment(db, exp.ID)
	if err != nil {
		t.Fatalf("StartExperiment: %v", err)
	}

	var reloaded Experiment
	if err := db.Preload("Variants").First(&reloaded, "id = ?", exp.ID).Error; err != nil {
		t.Fatalf("reload experiment: %v", err)
	}

	if reloaded.Status != StatusRunning {
		t.Errorf("expected experiment status running, got %q", reloaded.Status)
	}

	for _, v := range reloaded.Variants {
		if v.Status != VariantRunning {
			t.Errorf("variant %s: expected status running, got %q", v.ProfileName, v.Status)
		}
	}
}

func TestStartExperimentNotPending(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "not-pending-proj", "/tmp/notpending.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Already running", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	StartExperiment(db, exp.ID)

	err = StartExperiment(db, exp.ID)
	if err == nil {
		t.Fatal("expected error for non-pending experiment, got nil")
	}
}

func TestMoveToReview(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "review-proj", "/tmp/review.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Review test", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	StartExperiment(db, exp.ID)

	var expWithVariants Experiment
	db.Preload("Variants").First(&expWithVariants, "id = ?", exp.ID)

	for _, v := range expWithVariants.Variants {
		PassVariant(db, v.ID)
	}

	err = MoveToReview(db, exp.ID)
	if err != nil {
		t.Fatalf("MoveToReview: %v", err)
	}

	var reloaded Experiment
	db.First(&reloaded, "id = ?", exp.ID)
	if reloaded.Status != StatusReview {
		t.Errorf("expected experiment status review, got %q", reloaded.Status)
	}
}

func TestMoveToReviewVariantsNotTerminal(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "not-terminal-proj", "/tmp/notterminal.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Not terminal", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	StartExperiment(db, exp.ID)

	err = MoveToReview(db, exp.ID)
	if err == nil {
		t.Fatal("expected error for non-terminal variants, got nil")
	}
}

func TestCompleteExperiment(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "complete-proj", "/tmp/complete.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Complete test", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	exp.Status = StatusReview
	db.Save(&exp)

	err = CompleteExperiment(db, exp.ID)
	if err != nil {
		t.Fatalf("CompleteExperiment: %v", err)
	}

	var reloaded Experiment
	db.First(&reloaded, "id = ?", exp.ID)
	if reloaded.Status != StatusCompleted {
		t.Errorf("expected experiment status completed, got %q", reloaded.Status)
	}
}

func TestCompleteExperimentNotReview(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "not-review-proj", "/tmp/notreview.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Not review", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	err = CompleteExperiment(db, exp.ID)
	if err == nil {
		t.Fatal("expected error for non-review experiment, got nil")
	}
}

func TestCancelExperiment(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "cancel-proj", "/tmp/cancel.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Cancel test", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	StartExperiment(db, exp.ID)

	err = CancelExperiment(db, exp.ID)
	if err != nil {
		t.Fatalf("CancelExperiment: %v", err)
	}

	var reloaded Experiment
	if err := db.Preload("Variants").First(&reloaded, "id = ?", exp.ID).Error; err != nil {
		t.Fatalf("reload experiment: %v", err)
	}

	if reloaded.Status != StatusCancelled {
		t.Errorf("expected experiment status cancelled, got %q", reloaded.Status)
	}

	for _, v := range reloaded.Variants {
		if v.Status != VariantFailed {
			t.Errorf("variant %s: expected status failed, got %q", v.ProfileName, v.Status)
		}
	}
}

func TestCancelExperimentCompleted(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "completed-proj", "/tmp/completed.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Completed", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	exp.Status = StatusCompleted
	db.Save(&exp)

	err = CancelExperiment(db, exp.ID)
	if err == nil {
		t.Fatal("expected error for completed experiment, got nil")
	}
}

func TestPassVariant(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "pass-proj", "/tmp/pass.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Pass test", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	StartExperiment(db, exp.ID)

	var expWithVariants Experiment
	db.Preload("Variants").First(&expWithVariants, "id = ?", exp.ID)

	v := expWithVariants.Variants[0]
	err = PassVariant(db, v.ID)
	if err != nil {
		t.Fatalf("PassVariant: %v", err)
	}

	var reloaded Variant
	db.First(&reloaded, "id = ?", v.ID)
	if reloaded.Status != VariantPassed {
		t.Errorf("expected variant status passed, got %q", reloaded.Status)
	}
}

func TestPassVariantAlreadyTerminal(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "already-terminal-proj", "/tmp/alreadyterminal.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Already terminal", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	StartExperiment(db, exp.ID)

	var expWithVariants Experiment
	db.Preload("Variants").First(&expWithVariants, "id = ?", exp.ID)

	v := expWithVariants.Variants[0]
	PassVariant(db, v.ID)

	err = PassVariant(db, v.ID)
	if err == nil {
		t.Fatal("expected error for already passed variant, got nil")
	}
}

func TestFailVariant(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "fail-proj", "/tmp/fail.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Fail test", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	StartExperiment(db, exp.ID)

	var expWithVariants Experiment
	db.Preload("Variants").First(&expWithVariants, "id = ?", exp.ID)

	v := expWithVariants.Variants[0]
	err = FailVariant(db, v.ID)
	if err != nil {
		t.Fatalf("FailVariant: %v", err)
	}

	var reloaded Variant
	db.First(&reloaded, "id = ?", v.ID)
	if reloaded.Status != VariantFailed {
		t.Errorf("expected variant status failed, got %q", reloaded.Status)
	}
}

func TestFailVariantAlreadyTerminal(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "fail-terminal-proj", "/tmp/failterminal.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Already terminal", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	StartExperiment(db, exp.ID)

	var expWithVariants Experiment
	db.Preload("Variants").First(&expWithVariants, "id = ?", exp.ID)

	v := expWithVariants.Variants[0]
	FailVariant(db, v.ID)

	err = FailVariant(db, v.ID)
	if err == nil {
		t.Fatal("expected error for already failed variant, got nil")
	}
}

func TestPromoteVariant(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "promote-proj", "/tmp/promote.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Promote test", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	StartExperiment(db, exp.ID)

	var expWithVariants Experiment
	db.Preload("Variants").First(&expWithVariants, "id = ?", exp.ID)

	v := expWithVariants.Variants[0]
	PassVariant(db, v.ID)

	err = PromoteVariant(db, v.ID)
	if err != nil {
		t.Fatalf("PromoteVariant: %v", err)
	}

	var reloaded Variant
	db.First(&reloaded, "id = ?", v.ID)
	if reloaded.Status != VariantWinner {
		t.Errorf("expected variant status winner, got %q", reloaded.Status)
	}
}

func TestPromoteVariantNotPassed(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "not-passed-proj", "/tmp/notpassed.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Not passed", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	StartExperiment(db, exp.ID)

	var expWithVariants Experiment
	db.Preload("Variants").First(&expWithVariants, "id = ?", exp.ID)

	v := expWithVariants.Variants[0]
	err = PromoteVariant(db, v.ID)
	if err == nil {
		t.Fatal("expected error for non-passed variant, got nil")
	}
}

// ---------------------------------------------------------------------------
// Query Helper Tests
// ---------------------------------------------------------------------------

func TestListActiveExperiments(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "list-proj", "/tmp/list.git", "master")

	exp1, err := CreateExperiment(db, proj.ID, "Active 1", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	StartExperiment(db, exp1.ID)

	exp2, err := CreateExperiment(db, proj.ID, "Active 2", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	StartExperiment(db, exp2.ID)

	exp3, err := CreateExperiment(db, proj.ID, "Active 3", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	StartExperiment(db, exp3.ID)

	exp4, err := CreateExperiment(db, proj.ID, "Completed", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	exp4.Status = StatusCompleted
	db.Save(&exp4)

	active, err := ListActiveExperiments(db)
	if err != nil {
		t.Fatalf("ListActiveExperiments: %v", err)
	}

	if len(active) != 3 {
		t.Errorf("expected 3 active experiments, got %d", len(active))
	}
}

func TestGetExperimentByID(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "get-proj", "/tmp/get.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Get test", "desc", []string{"a", "b", "c"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	got, err := GetExperimentByID(db, exp.ID)
	if err != nil {
		t.Fatalf("GetExperimentByID: %v", err)
	}

	if got.ID != exp.ID {
		t.Errorf("expected ID %s, got %s", exp.ID, got.ID)
	}
	if len(got.Variants) != 3 {
		t.Errorf("expected 3 variants, got %d", len(got.Variants))
	}
}

func TestGetVariantByTaskID(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "variant-proj", "/tmp/variant.git", "master")

	exp, err := CreateExperiment(db, proj.ID, "Variant test", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	taskID := exp.Variants[0].TaskID
	v, err := GetVariantByTaskID(db, taskID)
	if err != nil {
		t.Fatalf("GetVariantByTaskID: %v", err)
	}

	if v.TaskID != taskID {
		t.Errorf("expected task ID %s, got %s", taskID, v.TaskID)
	}
	if v.ProfileName != "a" {
		t.Errorf("expected profile name 'a', got %q", v.ProfileName)
	}
}

func TestCountActiveExperiments(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &Experiment{}, &Variant{})
	proj := testutil.CreateProject(t, db, "count-proj", "/tmp/count.git", "master")

	exp1, err := CreateExperiment(db, proj.ID, "Active 1", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	StartExperiment(db, exp1.ID)

	exp2, err := CreateExperiment(db, proj.ID, "Active 2", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	StartExperiment(db, exp2.ID)

	exp3, err := CreateExperiment(db, proj.ID, "Completed", "desc", []string{"a", "b"}, "a")
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	exp3.Status = StatusCompleted
	db.Save(&exp3)

	count, err := CountActiveExperiments(db)
	if err != nil {
		t.Fatalf("CountActiveExperiments: %v", err)
	}

	if count != 2 {
		t.Errorf("expected 2 active experiments, got %d", count)
	}
}
