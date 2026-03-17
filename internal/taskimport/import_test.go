package taskimport

import (
	"strings"
	"testing"
	"testing/iotest"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// --- toJSONArray ---

func TestToJSONArray_Empty(t *testing.T) {
	got := toJSONArray(nil)
	if got != nil {
		t.Errorf("toJSONArray(nil) = %v, want nil", got)
	}

	got = toJSONArray([]string{})
	if got != nil {
		t.Errorf("toJSONArray([]) = %v, want nil", got)
	}
}

func TestToJSONArray_NonEmpty(t *testing.T) {
	got := toJSONArray([]string{"a", "b"})
	want := model.JSONArray{"a", "b"}
	if len(got) != len(want) {
		t.Fatalf("toJSONArray length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("toJSONArray[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// --- Import: happy path ---

func createTestProject(t *testing.T, db *gorm.DB) (uuid.UUID, model.Project) {
	t.Helper()
	proj := model.Project{
		ID:           uuid.New(),
		Name:         "test",
		BareRepoPath: "/tmp/test.git",
	}
	if err := db.Create(&proj).Error; err != nil {
		t.Fatalf("create test project: %v", err)
	}
	return proj.ID, proj
}

func TestImport_BasicParentTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID, _ := createTestProject(t, db)

	md := `# My Parent Task

A description of the parent task.
`
	count, err := Import(strings.NewReader(md), db, projectID)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if count != 1 {
		t.Errorf("created = %d, want 1", count)
	}

	var task model.Task
	if err := db.Where("project_id = ? AND title = ?", projectID, "My Parent Task").First(&task).Error; err != nil {
		t.Fatalf("task not found: %v", err)
	}
	if task.Description != "A description of the parent task." {
		t.Errorf("description = %q", task.Description)
	}
	if task.Status != model.StatusBacklog {
		t.Errorf("status = %q, want %q", task.Status, model.StatusBacklog)
	}
	if task.ProjectID != projectID {
		t.Errorf("project_id = %v, want %v", task.ProjectID, projectID)
	}
}

func TestImport_ParentWithSubtasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID, _ := createTestProject(t, db)

	md := `# Parent Task

Parent description.

## Subtask One

First subtask description.

## Subtask Two

Second subtask description.
`
	count, err := Import(strings.NewReader(md), db, projectID)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if count != 3 {
		t.Errorf("created = %d, want 3", count)
	}

	var parent model.Task
	if err := db.Where("project_id = ? AND title = ?", projectID, "Parent Task").First(&parent).Error; err != nil {
		t.Fatalf("parent not found: %v", err)
	}
	if parent.ParentTaskID != nil {
		t.Errorf("parent.ParentTaskID = %v, want nil", parent.ParentTaskID)
	}

	var subtasks []model.Task
	if err := db.Where("parent_task_id = ?", parent.ID).Find(&subtasks).Error; err != nil {
		t.Fatalf("find subtasks: %v", err)
	}
	if len(subtasks) != 2 {
		t.Fatalf("subtask count = %d, want 2", len(subtasks))
	}

	titles := map[string]bool{}
	for _, s := range subtasks {
		titles[s.Title] = true
		if *s.ParentTaskID != parent.ID {
			t.Errorf("subtask %q parent_task_id = %v, want %v", s.Title, *s.ParentTaskID, parent.ID)
		}
	}
	if !titles["Subtask One"] || !titles["Subtask Two"] {
		t.Errorf("expected subtask titles, got %v", titles)
	}
}

func TestImport_MultipleParents(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID, _ := createTestProject(t, db)

	md := `# First Parent

First parent description.

## First Subtask

Belongs to first parent.

# Second Parent

Second parent description.

## Second Subtask

Belongs to second parent.
`
	count, err := Import(strings.NewReader(md), db, projectID)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if count != 4 {
		t.Errorf("created = %d, want 4", count)
	}

	var firstParent, secondParent model.Task
	if err := db.Where("project_id = ? AND title = ?", projectID, "First Parent").First(&firstParent).Error; err != nil {
		t.Fatalf("first parent not found: %v", err)
	}
	if err := db.Where("project_id = ? AND title = ?", projectID, "Second Parent").First(&secondParent).Error; err != nil {
		t.Fatalf("second parent not found: %v", err)
	}

	var firstSub model.Task
	if err := db.Where("project_id = ? AND title = ?", projectID, "First Subtask").First(&firstSub).Error; err != nil {
		t.Fatalf("first subtask not found: %v", err)
	}
	if *firstSub.ParentTaskID != firstParent.ID {
		t.Errorf("first subtask parent = %v, want %v", *firstSub.ParentTaskID, firstParent.ID)
	}

	var secondSub model.Task
	if err := db.Where("project_id = ? AND title = ?", projectID, "Second Subtask").First(&secondSub).Error; err != nil {
		t.Fatalf("second subtask not found: %v", err)
	}
	if *secondSub.ParentTaskID != secondParent.ID {
		t.Errorf("second subtask parent = %v, want %v", *secondSub.ParentTaskID, secondParent.ID)
	}
}

// --- Import: deduplication ---

func TestImport_SkipsExistingParent(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID, _ := createTestProject(t, db)

	// Pre-create a task with the same title.
	existing := model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       "Existing Task",
		Description: "original description",
		Status:      model.StatusBacklog,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("pre-create task: %v", err)
	}

	md := `# Existing Task

New description that should be ignored.
`
	count, err := Import(strings.NewReader(md), db, projectID)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if count != 0 {
		t.Errorf("created = %d, want 0", count)
	}

	// Verify the original task is unchanged.
	var task model.Task
	if err := db.Where("id = ?", existing.ID).First(&task).Error; err != nil {
		t.Fatalf("find original task: %v", err)
	}
	if task.Description != "original description" {
		t.Errorf("description changed to %q", task.Description)
	}
}

func TestImport_SkipsExistingSubtask(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID, _ := createTestProject(t, db)

	// Pre-create only the subtask (not the parent). The Import function
	// loads all existing tasks in the project and uses their titles for
	// dedup. When the parent is new it will be created, but the subtask
	// whose title already exists should be skipped.
	existingSub := model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       "Existing Sub",
		Description: "existing sub desc",
		Status:      model.StatusBacklog,
	}
	if err := db.Create(&existingSub).Error; err != nil {
		t.Fatalf("pre-create subtask: %v", err)
	}

	md := `# Fresh Parent

Parent description.

## Existing Sub

This description should be ignored.

## New Sub

Brand new subtask.
`
	count, err := Import(strings.NewReader(md), db, projectID)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	// Fresh Parent is new (created), Existing Sub is skipped, New Sub is created.
	if count != 2 {
		t.Errorf("created = %d, want 2 (parent + new sub)", count)
	}

	// Verify existing subtask was not overwritten.
	var sub model.Task
	if err := db.Where("id = ?", existingSub.ID).First(&sub).Error; err != nil {
		t.Fatalf("find existing sub: %v", err)
	}
	if sub.Description != "existing sub desc" {
		t.Errorf("existing sub description changed to %q", sub.Description)
	}

	// Verify the new subtask was created.
	var newSub model.Task
	if err := db.Where("project_id = ? AND title = ?", projectID, "New Sub").First(&newSub).Error; err != nil {
		t.Fatalf("new sub not found: %v", err)
	}
}

// --- Import: dependency resolution ---

func TestImport_ResolvesDependencies(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID, _ := createTestProject(t, db)

	md := `# First Task

First task description.

# Second Task

Depends-on: First Task

Second task that depends on first.
`
	count, err := Import(strings.NewReader(md), db, projectID)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if count != 2 {
		t.Errorf("created = %d, want 2", count)
	}

	var first, second model.Task
	if err := db.Where("project_id = ? AND title = ?", projectID, "First Task").First(&first).Error; err != nil {
		t.Fatalf("first task not found: %v", err)
	}
	if err := db.Where("project_id = ? AND title = ?", projectID, "Second Task").First(&second).Error; err != nil {
		t.Fatalf("second task not found: %v", err)
	}

	if len(second.DependencyIDs) != 1 {
		t.Fatalf("dependency_ids length = %d, want 1", len(second.DependencyIDs))
	}
	if second.DependencyIDs[0] != first.ID.String() {
		t.Errorf("dependency_ids[0] = %q, want %q", second.DependencyIDs[0], first.ID.String())
	}
}

func TestImport_UnknownDependencyError(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID, _ := createTestProject(t, db)

	md := `# Some Task

Depends-on: Nonexistent Task

A task depending on something that does not exist.
`
	_, err := Import(strings.NewReader(md), db, projectID)
	if err == nil {
		t.Fatal("expected error for unknown dependency")
	}
	if !strings.Contains(err.Error(), "unknown task") {
		t.Errorf("error = %q, want it to contain 'unknown task'", err.Error())
	}
}

// --- Import: metadata ---

func TestImport_Priority(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID, _ := createTestProject(t, db)

	md := `# Priority Task

Priority: 3

Task with priority three.
`
	count, err := Import(strings.NewReader(md), db, projectID)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if count != 1 {
		t.Errorf("created = %d, want 1", count)
	}

	var task model.Task
	if err := db.Where("project_id = ? AND title = ?", projectID, "Priority Task").First(&task).Error; err != nil {
		t.Fatalf("task not found: %v", err)
	}
	if task.Priority != 3 {
		t.Errorf("priority = %d, want 3", task.Priority)
	}
}

func TestImport_Labels(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID, _ := createTestProject(t, db)

	md := `# Labeled Task

Labels: backend, api

Task with labels.
`
	count, err := Import(strings.NewReader(md), db, projectID)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if count != 1 {
		t.Errorf("created = %d, want 1", count)
	}

	var task model.Task
	if err := db.Where("project_id = ? AND title = ?", projectID, "Labeled Task").First(&task).Error; err != nil {
		t.Fatalf("task not found: %v", err)
	}
	if len(task.Labels) != 2 {
		t.Fatalf("labels length = %d, want 2", len(task.Labels))
	}
	if task.Labels[0] != "backend" || task.Labels[1] != "api" {
		t.Errorf("labels = %v, want [backend api]", task.Labels)
	}
}

// --- Import: error handling ---

func TestImport_ParseError(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID, _ := createTestProject(t, db)

	_, err := Import(iotest.ErrReader(iotest.ErrTimeout), db, projectID)
	if err == nil {
		t.Fatal("expected error from bad reader")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error = %q, want it to contain 'parse'", err.Error())
	}
}
