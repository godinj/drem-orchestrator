package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

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
	if err := Run(db, []string{"tasks"}, &buf, false); err != nil {
		t.Fatalf("tasks: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Task A") || !strings.Contains(out, "Task B") || !strings.Contains(out, "Task C") {
		t.Errorf("expected all 3 tasks in output:\n%s", out)
	}

	// Filter by status
	buf.Reset()
	if err := Run(db, []string{"tasks", "--status=backlog"}, &buf, false); err != nil {
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
	if err := Run(db, []string{"task", parent.ID.String()}, &buf, false); err != nil {
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
	if err := Run(db, []string{"agents"}, &buf, false); err != nil {
		t.Fatalf("agents: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "coder") || !strings.Contains(out, "planner") {
		t.Errorf("expected both agents:\n%s", out)
	}

	// Filter by status
	buf.Reset()
	if err := Run(db, []string{"agents", "--status=working"}, &buf, false); err != nil {
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
	if err := Run(db, []string{"failures"}, &buf, false); err != nil {
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
	if err := Run(db, []string{"failures", "--since=0s"}, &buf, false); err != nil {
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
	if err := Run(db, []string{"stats"}, &buf, false); err != nil {
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
	if err := Run(db, []string{"file-task", "--title=New Feature", "--description=Build it"}, &buf, false); err != nil {
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
	if err := Run(db, []string{"comment", task.ID.String(), "--body=Nice work"}, &buf, false); err != nil {
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
	if err := Run(db, []string{"tasks"}, &buf, true); err != nil {
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
	if err := Run(db, []string{"task", prefix}, &buf, false); err != nil {
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
	err := Run(db, []string{"bogus"}, &bytes.Buffer{}, false)
	if err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Errorf("expected unknown subcommand error, got: %v", err)
	}
}

func TestInvalidTaskStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	err := Run(db, []string{"tasks", "--status=nonexistent"}, &bytes.Buffer{}, false)
	if err == nil || !strings.Contains(err.Error(), "unknown task status") {
		t.Errorf("expected unknown task status error, got: %v", err)
	}
}

func TestCommentRequiresBody(t *testing.T) {
	db := testutil.NewTestDB(t)
	proj := testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, proj.ID, "Task", model.StatusBacklog)

	err := Run(db, []string{"comment", task.ID.String()}, &bytes.Buffer{}, false)
	if err == nil || !strings.Contains(err.Error(), "--body is required") {
		t.Errorf("expected --body required error, got: %v", err)
	}
}

func TestFileTaskRequiresTitle(t *testing.T) {
	db := testutil.NewTestDB(t)
	_ = testutil.CreateProject(t, db, "test-proj", "/tmp/test.git", "master")

	err := Run(db, []string{"file-task", "--description=Desc"}, &bytes.Buffer{}, false)
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
	if err := Run(db, []string{"stats"}, &buf, true); err != nil {
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
		"--project-id=" + proj.ID.String()}, &buf, false); err != nil {
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
	if err := Run(db, []string{"task", task.ID.String()}, &buf, true); err != nil {
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
