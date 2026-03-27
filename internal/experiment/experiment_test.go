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
