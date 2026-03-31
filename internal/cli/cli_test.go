package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/experiment"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func TestTasksList(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	testutil.CreateTask(t, db, proj.ID, "Task A", model.StatusBacklog)
	testutil.CreateTask(t, db, proj.ID, "Task B", model.StatusInProgress)
	testutil.CreateTask(t, db, proj.ID, "Task C", model.StatusBacklog)

	// List all
	var buf bytes.Buffer
	if err := Run(db, []string{"tasks"}, &buf, false, nil); err != nil {
		t.Fatalf("tasks: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Task A") || !strings.Contains(out, "Task B") || !strings.Contains(out, "Task C") {
		t.Errorf("expected all 3 tasks in output:\n%s", out)
	}

	// Filter by status
	buf.Reset()
	if err := Run(db, []string{"tasks", "--status=backlog"}, &buf, false, nil); err != nil {
		t.Fatalf("tasks --status: %v", err)
	}
	out = buf.String()
	if !strings.Contains(out, "Task A") || !strings.Contains(out, "Task C") {
		t.Errorf("expected backlog tasks:\n%s", out)
	}
	if strings.Contains(out, "Task B") {
		t.Errorf("should not contain in_progress task:\n%s", out)
	}
}

func TestTaskDetail(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	parent := testutil.CreateTask(t, db, proj.ID, "Parent Task", model.StatusPlanning)

	// Create subtasks
	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    proj.ID,
		ParentTaskID: &parent.ID,
		Title:        "Subtask 1",
		Description:  "Sub desc",
		Status:       model.StatusBacklog,
		Category:     model.CategoryStandard,
	}
	if err := db.Create(&sub).Error; err != nil {
		t.Fatalf("create subtask: %v", err)
	}

	// Create comment
	comment := model.TaskComment{
		ID:     uuid.New(),
		TaskID: parent.ID,
		Author: "user",
		Body:   "Looks good",
	}
	if err := db.Create(&comment).Error; err != nil {
		t.Fatalf("create comment: %v", err)
	}

	// Create agent assigned to task
	ag := testutil.CreateAgent(t, db, parent.ID, model.AgentPlanner, model.AgentWorking)
	db.Model(&parent).Update("assigned_agent_id", ag.ID)

	var buf bytes.Buffer
	if err := Run(db, []string{"task", parent.ID.String()}, &buf, false, nil); err != nil {
		t.Fatalf("task detail: %v", err)
	}
	out := buf.String()

	checks := []string{
		parent.ID.String(),
		"Parent Task",
		"planning",
		"Subtask 1",
		"planner",
		"working",
		"Looks good",
		"user",
	}
	for _, check := range checks {
		if !strings.Contains(out, check) {
			t.Errorf("expected %q in output:\n%s", check, out)
		}
	}
}

func TestAgentsList(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Some Task", model.StatusInProgress)
	testutil.CreateAgent(t, db, task.ID, model.AgentCoder, model.AgentWorking)
	testutil.CreateAgent(t, db, uuid.Nil, model.AgentPlanner, model.AgentDead)

	// List all
	var buf bytes.Buffer
	if err := Run(db, []string{"agents"}, &buf, false, nil); err != nil {
		t.Fatalf("agents: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "coder") || !strings.Contains(out, "planner") {
		t.Errorf("expected both agents:\n%s", out)
	}

	// Filter by status
	buf.Reset()
	if err := Run(db, []string{"agents", "--status=working"}, &buf, false, nil); err != nil {
		t.Fatalf("agents --status: %v", err)
	}
	out = buf.String()
	if !strings.Contains(out, "coder") {
		t.Errorf("expected working agent:\n%s", out)
	}
	if strings.Contains(out, "planner") {
		t.Errorf("should not contain dead agent:\n%s", out)
	}
}

func TestFailures(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	testutil.CreateTask(t, db, proj.ID, "Failed Task", model.StatusFailed)
	testutil.CreateAgent(t, db, uuid.Nil, model.AgentCoder, model.AgentDead)

	// All failures (default 24h)
	var buf bytes.Buffer
	if err := Run(db, []string{"failures"}, &buf, false, nil); err != nil {
		t.Fatalf("failures: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Failed Task") {
		t.Errorf("expected failed task:\n%s", out)
	}
	if !strings.Contains(out, "agent") {
		t.Errorf("expected dead agent:\n%s", out)
	}

	// Zero duration = nothing should match
	buf.Reset()
	if err := Run(db, []string{"failures", "--since=0s"}, &buf, false, nil); err != nil {
		t.Fatalf("failures --since=0s: %v", err)
	}
	out = buf.String()
	if strings.Contains(out, "Failed Task") {
		t.Errorf("expected no failures with --since=0s:\n%s", out)
	}
}

func TestStats(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	testutil.CreateTask(t, db, proj.ID, "Task A", model.StatusBacklog)
	testutil.CreateTask(t, db, proj.ID, "Task B", model.StatusDone)
	testutil.CreateTask(t, db, proj.ID, "Task C", model.StatusFailed)
	testutil.CreateAgent(t, db, uuid.Nil, model.AgentCoder, model.AgentWorking)

	var buf bytes.Buffer
	if err := Run(db, []string{"stats"}, &buf, false, nil); err != nil {
		t.Fatalf("stats: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "3") { // total tasks
		t.Errorf("expected total tasks 3:\n%s", out)
	}
	if !strings.Contains(out, "33.3%") { // 1/3 failure rate
		t.Errorf("expected 33.3%% failure rate:\n%s", out)
	}
}

func TestFileTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	_ = proj

	var buf bytes.Buffer
	if err := Run(db, []string{"file-task", "--title=New Feature", "--description=Build it"}, &buf, false, nil); err != nil {
		t.Fatalf("file-task: %v", err)
	}

	// Verify task in DB
	var task model.Task
	if err := db.Where("title = ?", "New Feature").First(&task).Error; err != nil {
		t.Fatalf("task not found in DB: %v", err)
	}
	if task.Status != model.StatusClassifying {
		t.Errorf("expected status classifying, got %s", task.Status)
	}
	if task.Description != "Build it" {
		t.Errorf("expected description 'Build it', got %q", task.Description)
	}
}

func TestComment(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Commentable Task", model.StatusBacklog)

	var buf bytes.Buffer
	if err := Run(db, []string{"comment", task.ID.String(), "--body=Nice work"}, &buf, false, nil); err != nil {
		t.Fatalf("comment: %v", err)
	}

	// Verify comment in DB
	var comment model.TaskComment
	if err := db.Where("task_id = ?", task.ID).First(&comment).Error; err != nil {
		t.Fatalf("comment not found in DB: %v", err)
	}
	if comment.Author != "csuite" {
		t.Errorf("expected author 'csuite', got %q", comment.Author)
	}
	if comment.Body != "Nice work" {
		t.Errorf("expected body 'Nice work', got %q", comment.Body)
	}
}

func TestJSONOutput(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	testutil.CreateTask(t, db, proj.ID, "JSON Task", model.StatusBacklog)

	var buf bytes.Buffer
	if err := Run(db, []string{"tasks"}, &buf, true, nil); err != nil {
		t.Fatalf("tasks --json: %v", err)
	}

	var tasks []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &tasks); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, buf.String())
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(tasks))
	}
	if title, ok := tasks[0]["Title"].(string); !ok || title != "JSON Task" {
		t.Errorf("expected Title 'JSON Task', got %v", tasks[0]["Title"])
	}
}

func TestTaskIDPrefix(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Prefix Task", model.StatusBacklog)

	prefix := task.ID.String()[:8]

	var buf bytes.Buffer
	if err := Run(db, []string{"task", prefix}, &buf, false, nil); err != nil {
		t.Fatalf("task prefix: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, task.ID.String()) {
		t.Errorf("expected full ID in output:\n%s", out)
	}
	if !strings.Contains(out, "Prefix Task") {
		t.Errorf("expected title in output:\n%s", out)
	}
}

func TestUnknownSubcommand(t *testing.T) {
	db := testutil.NewTestDB(t)
	err := Run(db, []string{"bogus"}, &bytes.Buffer{}, false, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Errorf("expected unknown subcommand error, got: %v", err)
	}
}

func TestInvalidTaskStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	err := Run(db, []string{"tasks", "--status=nonexistent"}, &bytes.Buffer{}, false, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown task status") {
		t.Errorf("expected unknown task status error, got: %v", err)
	}
}

func TestCommentRequiresBody(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Task", model.StatusBacklog)

	err := Run(db, []string{"comment", task.ID.String()}, &bytes.Buffer{}, false, nil)
	if err == nil || !strings.Contains(err.Error(), "--body is required") {
		t.Errorf("expected --body required error, got: %v", err)
	}
}

func TestFileTaskRequiresTitle(t *testing.T) {
	db := testutil.NewTestDB(t)
	_ = testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")

	err := Run(db, []string{"file-task", "--description=Desc"}, &bytes.Buffer{}, false, nil)
	if err == nil || !strings.Contains(err.Error(), "--title is required") {
		t.Errorf("expected --title required error, got: %v", err)
	}
}

func TestStatsJSONOutput(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	testutil.CreateTask(t, db, proj.ID, "Task A", model.StatusBacklog)
	testutil.CreateTask(t, db, proj.ID, "Task B", model.StatusFailed)

	var buf bytes.Buffer
	if err := Run(db, []string{"stats"}, &buf, true, nil); err != nil {
		t.Fatalf("stats --json: %v", err)
	}

	var stats statsJSON
	if err := json.Unmarshal(buf.Bytes(), &stats); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, buf.String())
	}
	if stats.TotalTasks != 2 {
		t.Errorf("expected 2 total tasks, got %d", stats.TotalTasks)
	}
	if stats.FailureRate != 50.0 {
		t.Errorf("expected 50.0%% failure rate, got %.1f%%", stats.FailureRate)
	}
}

func TestFileTaskWithProjectID(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")

	var buf bytes.Buffer
	if err := Run(db, []string{"file-task", "--title=ProjTask", "--description=D",
		"--project-id=" + proj.ID.String()}, &buf, false, nil); err != nil {
		t.Fatalf("file-task with project-id: %v", err)
	}

	var task model.Task
	if err := db.Where("title = ?", "ProjTask").First(&task).Error; err != nil {
		t.Fatalf("task not found: %v", err)
	}
	if task.ProjectID != proj.ID {
		t.Errorf("expected project ID %s, got %s", proj.ID, task.ProjectID)
	}
}

func TestTaskDetailJSON(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "JSON Detail Task", model.StatusBacklog)

	var buf bytes.Buffer
	if err := Run(db, []string{"task", task.ID.String()}, &buf, true, nil); err != nil {
		t.Fatalf("task --json: %v", err)
	}

	var detail taskDetailJSON
	if err := json.Unmarshal(buf.Bytes(), &detail); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if detail.Title != "JSON Detail Task" {
		t.Errorf("expected title 'JSON Detail Task', got %q", detail.Title)
	}
	if detail.ID != task.ID.String() {
		t.Errorf("expected ID %s, got %s", task.ID, detail.ID)
	}
}

// TestExperimentFromTask_HappyPath verifies that 'experiment from-task' creates
// an experiment with SourceTaskID set and one variant task per profile.
func TestExperimentFromTask_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := db.AutoMigrate(&experiment.Experiment{}, &experiment.Variant{}); err != nil {
		t.Fatalf("migrate experiment tables: %v", err)
	}
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	sourceTask := testutil.CreateTask(t, db, proj.ID, "Source Task", model.StatusDone)

	var buf bytes.Buffer
	err := Run(db, []string{
		"experiment", "from-task",
		"--task=" + sourceTask.ID.String(),
		"--title=My Experiment",
		"--profiles=fast,quality",
		"--default=fast",
	}, &buf, false, nil)
	if err != nil {
		t.Fatalf("experiment from-task: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "My Experiment") {
		t.Errorf("expected experiment title in output:\n%s", out)
	}

	// Verify an experiment was created with SourceTaskID set.
	var exp experiment.Experiment
	if err := db.Preload("Variants").First(&exp, "source_task_id = ?", sourceTask.ID).Error; err != nil {
		t.Fatalf("experiment not found by source_task_id: %v", err)
	}
	if exp.Title != "My Experiment" {
		t.Errorf("expected title 'My Experiment', got %q", exp.Title)
	}
	if exp.SourceTaskID == nil || *exp.SourceTaskID != sourceTask.ID {
		t.Errorf("expected SourceTaskID %s, got %v", sourceTask.ID, exp.SourceTaskID)
	}
	if exp.DefaultVariant != "fast" {
		t.Errorf("expected DefaultVariant 'fast', got %q", exp.DefaultVariant)
	}
	if len(exp.Variants) != 2 {
		t.Errorf("expected 2 variants, got %d", len(exp.Variants))
	}
}

// TestExperimentFromTask_JSON verifies JSON output mode returns a parseable
// experiment struct with SourceTaskID populated.
func TestExperimentFromTask_JSON(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := db.AutoMigrate(&experiment.Experiment{}, &experiment.Variant{}); err != nil {
		t.Fatalf("migrate experiment tables: %v", err)
	}
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	sourceTask := testutil.CreateTask(t, db, proj.ID, "Source Task", model.StatusDone)

	var buf bytes.Buffer
	err := Run(db, []string{
		"experiment", "from-task",
		"--task=" + sourceTask.ID.String(),
		"--title=JSON Experiment",
		"--profiles=fast,quality",
		"--default=quality",
	}, &buf, true, nil)
	if err != nil {
		t.Fatalf("experiment from-task --json: %v", err)
	}

	var exp experiment.Experiment
	if err := json.Unmarshal(buf.Bytes(), &exp); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, buf.String())
	}
	if exp.Title != "JSON Experiment" {
		t.Errorf("expected title 'JSON Experiment', got %q", exp.Title)
	}
	if exp.SourceTaskID == nil || *exp.SourceTaskID != sourceTask.ID {
		t.Errorf("expected SourceTaskID %s, got %v", sourceTask.ID, exp.SourceTaskID)
	}
}

// TestExperimentFromTask_ReusePlan verifies that --reuse-plan causes variant
// tasks to start at plan_review status and have ReusesPlan=true on variants.
func TestExperimentFromTask_ReusePlan(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := db.AutoMigrate(&experiment.Experiment{}, &experiment.Variant{}); err != nil {
		t.Fatalf("migrate experiment tables: %v", err)
	}
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	sourceTask := testutil.CreateTask(t, db, proj.ID, "Source Task", model.StatusDone)

	var buf bytes.Buffer
	err := Run(db, []string{
		"experiment", "from-task",
		"--task=" + sourceTask.ID.String(),
		"--title=Reuse Experiment",
		"--profiles=fast,quality",
		"--default=fast",
		"--reuse-plan",
	}, &buf, false, nil)
	if err != nil {
		t.Fatalf("experiment from-task --reuse-plan: %v", err)
	}

	// Verify all variant tasks start at plan_review and have ReusesPlan=true.
	var exp experiment.Experiment
	if err := db.Preload("Variants").First(&exp, "source_task_id = ?", sourceTask.ID).Error; err != nil {
		t.Fatalf("experiment not found: %v", err)
	}
	for _, v := range exp.Variants {
		if !v.ReusesPlan {
			t.Errorf("variant %s: expected ReusesPlan=true", v.ProfileName)
		}
		var task model.Task
		if err := db.First(&task, "id = ?", v.TaskID).Error; err != nil {
			t.Fatalf("variant task not found for profile %s: %v", v.ProfileName, err)
		}
		if task.Status != model.StatusPlanReview {
			t.Errorf("variant %s: expected task status plan_review, got %s", v.ProfileName, task.Status)
		}
	}
}

// TestExperimentFromTask_SourceTaskNotFound verifies that an error is returned
// when the --task ID does not match any existing task.
func TestExperimentFromTask_SourceTaskNotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := db.AutoMigrate(&experiment.Experiment{}, &experiment.Variant{}); err != nil {
		t.Fatalf("migrate experiment tables: %v", err)
	}
	_ = testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")

	missingID := uuid.New()
	err := Run(db, []string{
		"experiment", "from-task",
		"--task=" + missingID.String(),
		"--title=Ghost Experiment",
		"--profiles=fast,quality",
		"--default=fast",
	}, &bytes.Buffer{}, false, nil)
	if err == nil {
		t.Fatal("expected error for non-existent source task, got nil")
	}
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "source task") {
		t.Errorf("expected 'not found' or 'source task' in error, got: %v", err)
	}
}

// TestExperimentFromTask_MissingProfiles verifies that --profiles is required.
func TestExperimentFromTask_MissingProfiles(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := db.AutoMigrate(&experiment.Experiment{}, &experiment.Variant{}); err != nil {
		t.Fatalf("migrate experiment tables: %v", err)
	}
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	sourceTask := testutil.CreateTask(t, db, proj.ID, "Source Task", model.StatusDone)

	err := Run(db, []string{
		"experiment", "from-task",
		"--task=" + sourceTask.ID.String(),
		"--title=No Profiles",
		"--default=fast",
	}, &bytes.Buffer{}, false, nil)
	if err == nil {
		t.Fatal("expected error for missing --profiles, got nil")
	}
	if !strings.Contains(err.Error(), "--profiles") {
		t.Errorf("expected '--profiles' in error message, got: %v", err)
	}
}

// TestExperimentFromTask_MissingDefault verifies that --default is required.
func TestExperimentFromTask_MissingDefault(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := db.AutoMigrate(&experiment.Experiment{}, &experiment.Variant{}); err != nil {
		t.Fatalf("migrate experiment tables: %v", err)
	}
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	sourceTask := testutil.CreateTask(t, db, proj.ID, "Source Task", model.StatusDone)

	err := Run(db, []string{
		"experiment", "from-task",
		"--task=" + sourceTask.ID.String(),
		"--title=No Default",
		"--profiles=fast,quality",
	}, &bytes.Buffer{}, false, nil)
	if err == nil {
		t.Fatal("expected error for missing --default, got nil")
	}
	if !strings.Contains(err.Error(), "--default") {
		t.Errorf("expected '--default' in error message, got: %v", err)
	}
}

// TestExperimentFromTask_MissingTask verifies that --task is required.
func TestExperimentFromTask_MissingTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := db.AutoMigrate(&experiment.Experiment{}, &experiment.Variant{}); err != nil {
		t.Fatalf("migrate experiment tables: %v", err)
	}

	err := Run(db, []string{
		"experiment", "from-task",
		"--title=No Task Flag",
		"--profiles=fast,quality",
		"--default=fast",
	}, &bytes.Buffer{}, false, nil)
	if err == nil {
		t.Fatal("expected error for missing --task, got nil")
	}
	if !strings.Contains(err.Error(), "--task") {
		t.Errorf("expected '--task' in error message, got: %v", err)
	}
}

// TestExperimentFromTask_SourceTaskNotDone verifies that a source task must be
// in StatusDone; tasks in other states should be rejected.
func TestExperimentFromTask_SourceTaskNotDone(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := db.AutoMigrate(&experiment.Experiment{}, &experiment.Variant{}); err != nil {
		t.Fatalf("migrate experiment tables: %v", err)
	}
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	inProgressTask := testutil.CreateTask(t, db, proj.ID, "Still Running", model.StatusInProgress)

	err := Run(db, []string{
		"experiment", "from-task",
		"--task=" + inProgressTask.ID.String(),
		"--title=From Incomplete Task",
		"--profiles=fast,quality",
		"--default=fast",
	}, &bytes.Buffer{}, false, nil)
	if err == nil {
		t.Fatal("expected error for source task not in StatusDone, got nil")
	}
	if !strings.Contains(err.Error(), "done") && !strings.Contains(err.Error(), "status") {
		t.Errorf("expected 'done' or 'status' in error, got: %v", err)
	}
}
