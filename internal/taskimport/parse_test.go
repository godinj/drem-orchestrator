package taskimport

import (
	"strings"
	"testing"
)

func TestParse_basic(t *testing.T) {
	input := `# Add authentication

Priority: 10
Labels: auth, backend

Implement OAuth2 login flow with Google and GitHub providers.
Must support session persistence.

## OAuth2 provider integration

Priority: 10

Set up OAuth2 client credentials and callback handling.

## Session management

Priority: 8
Depends-on: OAuth2 provider integration

Implement server-side session storage.

# Add search

Priority: 5
Depends-on: Add authentication

Full-text search across all entities.
`

	tasks, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}

	// First task.
	auth := tasks[0]
	if auth.Title != "Add authentication" {
		t.Errorf("title = %q, want %q", auth.Title, "Add authentication")
	}
	if auth.Priority != 10 {
		t.Errorf("priority = %d, want 10", auth.Priority)
	}
	if len(auth.Labels) != 2 || auth.Labels[0] != "auth" || auth.Labels[1] != "backend" {
		t.Errorf("labels = %v, want [auth backend]", auth.Labels)
	}
	if !strings.Contains(auth.Description, "OAuth2 login flow") {
		t.Errorf("description missing expected text: %q", auth.Description)
	}
	if strings.Contains(auth.Description, "Priority:") {
		t.Errorf("description should not contain metadata: %q", auth.Description)
	}

	// Subtasks.
	if len(auth.Subtasks) != 2 {
		t.Fatalf("got %d subtasks, want 2", len(auth.Subtasks))
	}
	oauth := auth.Subtasks[0]
	if oauth.Title != "OAuth2 provider integration" {
		t.Errorf("subtask title = %q", oauth.Title)
	}
	session := auth.Subtasks[1]
	if len(session.DependsOn) != 1 || session.DependsOn[0] != "OAuth2 provider integration" {
		t.Errorf("subtask depends-on = %v", session.DependsOn)
	}

	// Second task.
	search := tasks[1]
	if search.Title != "Add search" {
		t.Errorf("title = %q", search.Title)
	}
	if len(search.DependsOn) != 1 || search.DependsOn[0] != "Add authentication" {
		t.Errorf("depends-on = %v", search.DependsOn)
	}
}

func TestParse_noMeta(t *testing.T) {
	input := `# Simple task

Just a description with no metadata.
`

	tasks, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if tasks[0].Priority != 0 {
		t.Errorf("priority = %d, want 0", tasks[0].Priority)
	}
	if tasks[0].Description != "Just a description with no metadata." {
		t.Errorf("description = %q", tasks[0].Description)
	}
}

func TestParse_subtaskWithoutParent(t *testing.T) {
	input := `## Orphan subtask

This should fail.
`
	_, err := Parse(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for subtask without parent")
	}
}

func TestParse_multipleDependencies(t *testing.T) {
	input := `# Task A

Description A.

# Task B

Description B.

# Task C

Depends-on: Task A, Task B

Description C.
`

	tasks, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	c := tasks[2]
	if len(c.DependsOn) != 2 {
		t.Fatalf("depends-on length = %d, want 2", len(c.DependsOn))
	}
	if c.DependsOn[0] != "Task A" || c.DependsOn[1] != "Task B" {
		t.Errorf("depends-on = %v", c.DependsOn)
	}
}
