package model

import (
	"testing"

	"github.com/google/uuid"
)

// --- Enum Parse / String tests ---

func TestParseBugReportCategory(t *testing.T) {
	tests := []struct {
		input string
		want  BugReportCategory
	}{
		{"tooling", BugCategoryTooling},
		{"merge_conflict", BugCategoryMergeConflict},
		{"requirements", BugCategoryRequirements},
		{"constraint_violation", BugCategoryConstraintViolation},
		{"upstream_code", BugCategoryUpstreamCode},
		{"test_failure", BugCategoryTestFailure},
		{"environment", BugCategoryEnvironment},
		{"other", BugCategoryOther},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseBugReportCategory(tc.input)
			if err != nil {
				t.Fatalf("ParseBugReportCategory(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseBugReportCategory(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseBugReportCategory_Error(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"invalid value", "nonexistent"},
		{"empty string", ""},
		{"close but wrong", "Tooling"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseBugReportCategory(tc.input)
			if err == nil {
				t.Errorf("ParseBugReportCategory(%q) expected error, got nil", tc.input)
			}
		})
	}
}

func TestBugReportCategoryString(t *testing.T) {
	tests := []struct {
		category BugReportCategory
		want     string
	}{
		{BugCategoryTooling, "tooling"},
		{BugCategoryMergeConflict, "merge_conflict"},
		{BugCategoryRequirements, "requirements"},
		{BugCategoryConstraintViolation, "constraint_violation"},
		{BugCategoryUpstreamCode, "upstream_code"},
		{BugCategoryTestFailure, "test_failure"},
		{BugCategoryEnvironment, "environment"},
		{BugCategoryOther, "other"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := tc.category.String()
			if got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseBugReportSeverity(t *testing.T) {
	tests := []struct {
		input string
		want  BugReportSeverity
	}{
		{"blocking", BugSeverityBlocking},
		{"degraded", BugSeverityDegraded},
		{"informational", BugSeverityInformational},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseBugReportSeverity(tc.input)
			if err != nil {
				t.Fatalf("ParseBugReportSeverity(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseBugReportSeverity(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseBugReportSeverity_Error(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"invalid value", "critical"},
		{"empty string", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseBugReportSeverity(tc.input)
			if err == nil {
				t.Errorf("ParseBugReportSeverity(%q) expected error, got nil", tc.input)
			}
		})
	}
}

func TestBugReportSeverityString(t *testing.T) {
	tests := []struct {
		severity BugReportSeverity
		want     string
	}{
		{BugSeverityBlocking, "blocking"},
		{BugSeverityDegraded, "degraded"},
		{BugSeverityInformational, "informational"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := tc.severity.String()
			if got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseBugReportStatus(t *testing.T) {
	tests := []struct {
		input string
		want  BugReportStatus
	}{
		{"open", BugStatusOpen},
		{"acknowledged", BugStatusAcknowledged},
		{"promoted", BugStatusPromoted},
		{"dismissed", BugStatusDismissed},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseBugReportStatus(tc.input)
			if err != nil {
				t.Fatalf("ParseBugReportStatus(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseBugReportStatus(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseBugReportStatus_Error(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"invalid value", "closed"},
		{"empty string", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseBugReportStatus(tc.input)
			if err == nil {
				t.Errorf("ParseBugReportStatus(%q) expected error, got nil", tc.input)
			}
		})
	}
}

func TestBugReportStatusString(t *testing.T) {
	tests := []struct {
		status BugReportStatus
		want   string
	}{
		{BugStatusOpen, "open"},
		{BugStatusAcknowledged, "acknowledged"},
		{BugStatusPromoted, "promoted"},
		{BugStatusDismissed, "dismissed"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := tc.status.String()
			if got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- GORM round-trip tests ---

func TestBugReportCreate(t *testing.T) {
	t.Run("generates UUID when nil", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		br := BugReport{
			Title:       "Test bug",
			Description: "Something broke",
			Category:    BugCategoryTooling,
			Severity:    BugSeverityBlocking,
			Status:      BugStatusOpen,
			ProjectID:   proj.ID,
		}
		if err := db.Create(&br).Error; err != nil {
			t.Fatalf("create bug report: %v", err)
		}
		if br.ID == uuid.Nil {
			t.Error("expected non-nil UUID, got nil")
		}
		var loaded BugReport
		if err := db.First(&loaded, "id = ?", br.ID).Error; err != nil {
			t.Fatalf("load bug report: %v", err)
		}
		if loaded.Title != "Test bug" {
			t.Errorf("Title = %q, want %q", loaded.Title, "Test bug")
		}
		if loaded.Category != BugCategoryTooling {
			t.Errorf("Category = %q, want %q", loaded.Category, BugCategoryTooling)
		}
		if loaded.Severity != BugSeverityBlocking {
			t.Errorf("Severity = %q, want %q", loaded.Severity, BugSeverityBlocking)
		}
		if loaded.Status != BugStatusOpen {
			t.Errorf("Status = %q, want %q", loaded.Status, BugStatusOpen)
		}
	})

	t.Run("preserves pre-set UUID", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		presetID := uuid.New()
		br := BugReport{
			ID:          presetID,
			Title:       "Preset ID bug",
			Description: "Description",
			Category:    BugCategoryOther,
			Severity:    BugSeverityInformational,
			ProjectID:   proj.ID,
		}
		if err := db.Create(&br).Error; err != nil {
			t.Fatalf("create bug report: %v", err)
		}
		var loaded BugReport
		if err := db.First(&loaded, "id = ?", presetID).Error; err != nil {
			t.Fatalf("load bug report: %v", err)
		}
		if loaded.ID != presetID {
			t.Errorf("ID = %v, want %v", loaded.ID, presetID)
		}
	})

	t.Run("round-trips all fields", func(t *testing.T) {
		db := testDB(t)
		proj, task := createProjectAndTask(t, db)
		agent := Agent{
			ProjectID: proj.ID,
			AgentType: AgentCoder,
			Name:      "bug-agent",
			Status:    AgentIdle,
		}
		if err := db.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}
		br := BugReport{
			Title:               "Full round-trip",
			Description:         "Full description",
			Category:            BugCategoryTestFailure,
			Severity:            BugSeverityDegraded,
			Status:              BugStatusAcknowledged,
			ReproductionContext: "go test ./... failed with exit code 1",
			AgentID:             &agent.ID,
			TaskID:              &task.ID,
			ProjectID:           proj.ID,
		}
		if err := db.Create(&br).Error; err != nil {
			t.Fatalf("create bug report: %v", err)
		}
		var loaded BugReport
		if err := db.First(&loaded, "id = ?", br.ID).Error; err != nil {
			t.Fatalf("load bug report: %v", err)
		}
		if loaded.Title != br.Title {
			t.Errorf("Title = %q, want %q", loaded.Title, br.Title)
		}
		if loaded.Description != br.Description {
			t.Errorf("Description = %q, want %q", loaded.Description, br.Description)
		}
		if loaded.Category != br.Category {
			t.Errorf("Category = %q, want %q", loaded.Category, br.Category)
		}
		if loaded.Severity != br.Severity {
			t.Errorf("Severity = %q, want %q", loaded.Severity, br.Severity)
		}
		if loaded.Status != br.Status {
			t.Errorf("Status = %q, want %q", loaded.Status, br.Status)
		}
		if loaded.ReproductionContext != br.ReproductionContext {
			t.Errorf("ReproductionContext = %q, want %q", loaded.ReproductionContext, br.ReproductionContext)
		}
		if loaded.AgentID == nil || *loaded.AgentID != agent.ID {
			t.Errorf("AgentID = %v, want %v", loaded.AgentID, agent.ID)
		}
		if loaded.TaskID == nil || *loaded.TaskID != task.ID {
			t.Errorf("TaskID = %v, want %v", loaded.TaskID, task.ID)
		}
		if loaded.ProjectID != proj.ID {
			t.Errorf("ProjectID = %v, want %v", loaded.ProjectID, proj.ID)
		}
	})
}

func TestBugReportNullableFields(t *testing.T) {
	t.Run("nil AgentID TaskID PromotedTaskID", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		br := BugReport{
			Title:       "No optional refs",
			Description: "Only required fields",
			Category:    BugCategoryEnvironment,
			Severity:    BugSeverityInformational,
			ProjectID:   proj.ID,
		}
		if err := db.Create(&br).Error; err != nil {
			t.Fatalf("create bug report: %v", err)
		}
		var loaded BugReport
		if err := db.First(&loaded, "id = ?", br.ID).Error; err != nil {
			t.Fatalf("load bug report: %v", err)
		}
		if loaded.AgentID != nil {
			t.Errorf("AgentID = %v, want nil", loaded.AgentID)
		}
		if loaded.TaskID != nil {
			t.Errorf("TaskID = %v, want nil", loaded.TaskID)
		}
		if loaded.PromotedTaskID != nil {
			t.Errorf("PromotedTaskID = %v, want nil", loaded.PromotedTaskID)
		}
	})

	t.Run("PromotedTaskID set after promotion", func(t *testing.T) {
		db := testDB(t)
		proj, task := createProjectAndTask(t, db)
		br := BugReport{
			Title:       "To be promoted",
			Description: "Will link to a task",
			Category:    BugCategoryRequirements,
			Severity:    BugSeverityDegraded,
			ProjectID:   proj.ID,
		}
		if err := db.Create(&br).Error; err != nil {
			t.Fatalf("create bug report: %v", err)
		}
		// Simulate promotion: set PromotedTaskID and status.
		br.PromotedTaskID = &task.ID
		br.Status = BugStatusPromoted
		if err := db.Save(&br).Error; err != nil {
			t.Fatalf("update bug report: %v", err)
		}
		var loaded BugReport
		if err := db.First(&loaded, "id = ?", br.ID).Error; err != nil {
			t.Fatalf("load bug report: %v", err)
		}
		if loaded.PromotedTaskID == nil || *loaded.PromotedTaskID != task.ID {
			t.Errorf("PromotedTaskID = %v, want %v", loaded.PromotedTaskID, task.ID)
		}
		if loaded.Status != BugStatusPromoted {
			t.Errorf("Status = %q, want %q", loaded.Status, BugStatusPromoted)
		}
	})
}

func TestBugReportStatusDefault(t *testing.T) {
	db := testDB(t)
	proj, _ := createProjectAndTask(t, db)

	// Create without explicitly setting Status — should default to "open".
	br := BugReport{
		Title:       "Default status",
		Description: "Should be open",
		Category:    BugCategoryOther,
		Severity:    BugSeverityInformational,
		ProjectID:   proj.ID,
	}
	if err := db.Create(&br).Error; err != nil {
		t.Fatalf("create bug report: %v", err)
	}
	var loaded BugReport
	if err := db.First(&loaded, "id = ?", br.ID).Error; err != nil {
		t.Fatalf("load bug report: %v", err)
	}
	if loaded.Status != BugStatusOpen {
		t.Errorf("Status = %q, want %q (default)", loaded.Status, BugStatusOpen)
	}
}

// --- BugReportComment tests ---

func TestBugReportCommentCreate(t *testing.T) {
	t.Run("generates UUID when nil", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		br := BugReport{
			Title:       "Comment test",
			Description: "For comment tests",
			Category:    BugCategoryOther,
			Severity:    BugSeverityInformational,
			ProjectID:   proj.ID,
		}
		if err := db.Create(&br).Error; err != nil {
			t.Fatalf("create bug report: %v", err)
		}
		comment := BugReportComment{
			BugReportID: br.ID,
			Author:      "user",
			Body:        "test comment body",
		}
		if err := db.Create(&comment).Error; err != nil {
			t.Fatalf("create comment: %v", err)
		}
		if comment.ID == uuid.Nil {
			t.Error("expected non-nil UUID, got nil")
		}
		var loaded BugReportComment
		if err := db.First(&loaded, "id = ?", comment.ID).Error; err != nil {
			t.Fatalf("load comment: %v", err)
		}
		if loaded.Body != "test comment body" {
			t.Errorf("Body = %q, want %q", loaded.Body, "test comment body")
		}
		if loaded.Author != "user" {
			t.Errorf("Author = %q, want %q", loaded.Author, "user")
		}
	})

	t.Run("preserves pre-set UUID", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		br := BugReport{
			Title:       "Comment preset test",
			Description: "For preset comment tests",
			Category:    BugCategoryOther,
			Severity:    BugSeverityInformational,
			ProjectID:   proj.ID,
		}
		if err := db.Create(&br).Error; err != nil {
			t.Fatalf("create bug report: %v", err)
		}
		presetID := uuid.New()
		comment := BugReportComment{
			ID:          presetID,
			BugReportID: br.ID,
			Author:      "system",
			Body:        "system comment",
		}
		if err := db.Create(&comment).Error; err != nil {
			t.Fatalf("create comment: %v", err)
		}
		var loaded BugReportComment
		if err := db.First(&loaded, "id = ?", presetID).Error; err != nil {
			t.Fatalf("load comment: %v", err)
		}
		if loaded.ID != presetID {
			t.Errorf("ID = %v, want %v", loaded.ID, presetID)
		}
	})
}

// --- Association tests ---

func TestBugReportAssociations(t *testing.T) {
	t.Run("preloads Agent association", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		agent := Agent{
			ProjectID: proj.ID,
			AgentType: AgentCoder,
			Name:      "assoc-agent",
			Status:    AgentIdle,
		}
		if err := db.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}
		br := BugReport{
			Title:       "Agent assoc",
			Description: "Has agent",
			Category:    BugCategoryTooling,
			Severity:    BugSeverityBlocking,
			ProjectID:   proj.ID,
			AgentID:     &agent.ID,
		}
		if err := db.Create(&br).Error; err != nil {
			t.Fatalf("create bug report: %v", err)
		}
		var loaded BugReport
		if err := db.Preload("Agent").First(&loaded, "id = ?", br.ID).Error; err != nil {
			t.Fatalf("load bug report: %v", err)
		}
		if loaded.Agent == nil {
			t.Fatal("Agent association is nil")
		}
		if loaded.Agent.ID != agent.ID {
			t.Errorf("Agent.ID = %v, want %v", loaded.Agent.ID, agent.ID)
		}
	})

	t.Run("preloads Task association", func(t *testing.T) {
		db := testDB(t)
		proj, task := createProjectAndTask(t, db)
		br := BugReport{
			Title:       "Task assoc",
			Description: "Has task",
			Category:    BugCategoryTestFailure,
			Severity:    BugSeverityDegraded,
			ProjectID:   proj.ID,
			TaskID:      &task.ID,
		}
		if err := db.Create(&br).Error; err != nil {
			t.Fatalf("create bug report: %v", err)
		}
		var loaded BugReport
		if err := db.Preload("Task").First(&loaded, "id = ?", br.ID).Error; err != nil {
			t.Fatalf("load bug report: %v", err)
		}
		if loaded.Task == nil {
			t.Fatal("Task association is nil")
		}
		if loaded.Task.ID != task.ID {
			t.Errorf("Task.ID = %v, want %v", loaded.Task.ID, task.ID)
		}
	})

	t.Run("preloads Project association", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		br := BugReport{
			Title:       "Project assoc",
			Description: "Has project",
			Category:    BugCategoryOther,
			Severity:    BugSeverityInformational,
			ProjectID:   proj.ID,
		}
		if err := db.Create(&br).Error; err != nil {
			t.Fatalf("create bug report: %v", err)
		}
		var loaded BugReport
		if err := db.Preload("Project").First(&loaded, "id = ?", br.ID).Error; err != nil {
			t.Fatalf("load bug report: %v", err)
		}
		if loaded.Project.ID != proj.ID {
			t.Errorf("Project.ID = %v, want %v", loaded.Project.ID, proj.ID)
		}
	})

	t.Run("preloads PromotedTask association", func(t *testing.T) {
		db := testDB(t)
		proj, task := createProjectAndTask(t, db)
		br := BugReport{
			Title:          "Promoted assoc",
			Description:    "Has promoted task",
			Category:       BugCategoryRequirements,
			Severity:       BugSeverityBlocking,
			Status:         BugStatusPromoted,
			ProjectID:      proj.ID,
			PromotedTaskID: &task.ID,
		}
		if err := db.Create(&br).Error; err != nil {
			t.Fatalf("create bug report: %v", err)
		}
		var loaded BugReport
		if err := db.Preload("PromotedTask").First(&loaded, "id = ?", br.ID).Error; err != nil {
			t.Fatalf("load bug report: %v", err)
		}
		if loaded.PromotedTask == nil {
			t.Fatal("PromotedTask association is nil")
		}
		if loaded.PromotedTask.ID != task.ID {
			t.Errorf("PromotedTask.ID = %v, want %v", loaded.PromotedTask.ID, task.ID)
		}
	})

	t.Run("preloads Comments association", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		br := BugReport{
			Title:       "Comments assoc",
			Description: "Has comments",
			Category:    BugCategoryOther,
			Severity:    BugSeverityInformational,
			ProjectID:   proj.ID,
		}
		if err := db.Create(&br).Error; err != nil {
			t.Fatalf("create bug report: %v", err)
		}
		c1 := BugReportComment{BugReportID: br.ID, Author: "user", Body: "first"}
		c2 := BugReportComment{BugReportID: br.ID, Author: "system", Body: "second"}
		if err := db.Create(&c1).Error; err != nil {
			t.Fatalf("create comment 1: %v", err)
		}
		if err := db.Create(&c2).Error; err != nil {
			t.Fatalf("create comment 2: %v", err)
		}
		var loaded BugReport
		if err := db.Preload("Comments").First(&loaded, "id = ?", br.ID).Error; err != nil {
			t.Fatalf("load bug report: %v", err)
		}
		if len(loaded.Comments) != 2 {
			t.Fatalf("Comments count = %d, want 2", len(loaded.Comments))
		}
	})
}
