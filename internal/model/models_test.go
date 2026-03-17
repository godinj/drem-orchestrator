package model

import (
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// testDB creates an in-memory SQLite database with auto-migration for model tests.
func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&Project{}, &Task{}, &Agent{}, &TaskEvent{}, &Memory{}, &TaskComment{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

// createProjectAndTask is a helper that creates a Project and Task in the DB,
// returning both for use as FK parents.
func createProjectAndTask(t *testing.T, db *gorm.DB) (Project, Task) {
	t.Helper()
	proj := Project{Name: "test-proj", BareRepoPath: "/tmp/test"}
	if err := db.Create(&proj).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := Task{
		ProjectID:   proj.ID,
		Title:       "Test Task",
		Description: "A test task",
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	return proj, task
}

func TestProjectBeforeCreate(t *testing.T) {
	t.Run("generates UUID when nil", func(t *testing.T) {
		db := testDB(t)
		proj := Project{Name: "auto-uuid", BareRepoPath: "/tmp/test"}
		if err := db.Create(&proj).Error; err != nil {
			t.Fatalf("create project: %v", err)
		}
		var loaded Project
		if err := db.First(&loaded, "id = ?", proj.ID).Error; err != nil {
			t.Fatalf("load project: %v", err)
		}
		if loaded.ID == uuid.Nil {
			t.Errorf("expected non-nil UUID, got nil")
		}
	})

	t.Run("preserves pre-set UUID", func(t *testing.T) {
		db := testDB(t)
		presetID := uuid.New()
		proj := Project{ID: presetID, Name: "preset-uuid", BareRepoPath: "/tmp/test"}
		if err := db.Create(&proj).Error; err != nil {
			t.Fatalf("create project: %v", err)
		}
		var loaded Project
		if err := db.First(&loaded, "id = ?", presetID).Error; err != nil {
			t.Fatalf("load project: %v", err)
		}
		if loaded.ID != presetID {
			t.Errorf("ID = %v, want %v", loaded.ID, presetID)
		}
	})
}

func TestTaskBeforeCreate(t *testing.T) {
	t.Run("generates UUID when nil", func(t *testing.T) {
		db := testDB(t)
		proj := Project{Name: "task-auto", BareRepoPath: "/tmp/test"}
		if err := db.Create(&proj).Error; err != nil {
			t.Fatalf("create project: %v", err)
		}
		task := Task{ProjectID: proj.ID, Title: "T", Description: "D"}
		if err := db.Create(&task).Error; err != nil {
			t.Fatalf("create task: %v", err)
		}
		var loaded Task
		if err := db.First(&loaded, "id = ?", task.ID).Error; err != nil {
			t.Fatalf("load task: %v", err)
		}
		if loaded.ID == uuid.Nil {
			t.Errorf("expected non-nil UUID, got nil")
		}
	})

	t.Run("preserves pre-set UUID", func(t *testing.T) {
		db := testDB(t)
		proj := Project{Name: "task-preset", BareRepoPath: "/tmp/test"}
		if err := db.Create(&proj).Error; err != nil {
			t.Fatalf("create project: %v", err)
		}
		presetID := uuid.New()
		task := Task{ID: presetID, ProjectID: proj.ID, Title: "T", Description: "D"}
		if err := db.Create(&task).Error; err != nil {
			t.Fatalf("create task: %v", err)
		}
		var loaded Task
		if err := db.First(&loaded, "id = ?", presetID).Error; err != nil {
			t.Fatalf("load task: %v", err)
		}
		if loaded.ID != presetID {
			t.Errorf("ID = %v, want %v", loaded.ID, presetID)
		}
	})
}

func TestAgentBeforeCreate(t *testing.T) {
	t.Run("generates UUID when nil", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		agent := Agent{
			ProjectID: proj.ID,
			AgentType: AgentCoder,
			Name:      "agent-auto",
			Status:    AgentIdle,
		}
		if err := db.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}
		var loaded Agent
		if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
			t.Fatalf("load agent: %v", err)
		}
		if loaded.ID == uuid.Nil {
			t.Errorf("expected non-nil UUID, got nil")
		}
	})

	t.Run("preserves pre-set UUID", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		presetID := uuid.New()
		agent := Agent{
			ID:        presetID,
			ProjectID: proj.ID,
			AgentType: AgentCoder,
			Name:      "agent-preset",
			Status:    AgentIdle,
		}
		if err := db.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}
		var loaded Agent
		if err := db.First(&loaded, "id = ?", presetID).Error; err != nil {
			t.Fatalf("load agent: %v", err)
		}
		if loaded.ID != presetID {
			t.Errorf("ID = %v, want %v", loaded.ID, presetID)
		}
	})
}

func TestTaskEventBeforeCreate(t *testing.T) {
	t.Run("generates UUID when nil", func(t *testing.T) {
		db := testDB(t)
		_, task := createProjectAndTask(t, db)
		event := TaskEvent{
			TaskID:    task.ID,
			EventType: "status_change",
			Actor:     "test",
		}
		if err := db.Create(&event).Error; err != nil {
			t.Fatalf("create event: %v", err)
		}
		var loaded TaskEvent
		if err := db.First(&loaded, "id = ?", event.ID).Error; err != nil {
			t.Fatalf("load event: %v", err)
		}
		if loaded.ID == uuid.Nil {
			t.Errorf("expected non-nil UUID, got nil")
		}
	})

	t.Run("preserves pre-set UUID", func(t *testing.T) {
		db := testDB(t)
		_, task := createProjectAndTask(t, db)
		presetID := uuid.New()
		event := TaskEvent{
			ID:        presetID,
			TaskID:    task.ID,
			EventType: "status_change",
			Actor:     "test",
		}
		if err := db.Create(&event).Error; err != nil {
			t.Fatalf("create event: %v", err)
		}
		var loaded TaskEvent
		if err := db.First(&loaded, "id = ?", presetID).Error; err != nil {
			t.Fatalf("load event: %v", err)
		}
		if loaded.ID != presetID {
			t.Errorf("ID = %v, want %v", loaded.ID, presetID)
		}
	})
}

func TestMemoryBeforeCreate(t *testing.T) {
	t.Run("generates UUID when nil", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		agent := Agent{
			ProjectID: proj.ID,
			AgentType: AgentCoder,
			Name:      "mem-agent",
			Status:    AgentIdle,
		}
		if err := db.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}
		mem := Memory{
			AgentID:    agent.ID,
			Content:    "test memory",
			MemoryType: "summary",
		}
		if err := db.Create(&mem).Error; err != nil {
			t.Fatalf("create memory: %v", err)
		}
		var loaded Memory
		if err := db.First(&loaded, "id = ?", mem.ID).Error; err != nil {
			t.Fatalf("load memory: %v", err)
		}
		if loaded.ID == uuid.Nil {
			t.Errorf("expected non-nil UUID, got nil")
		}
	})

	t.Run("preserves pre-set UUID", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		agent := Agent{
			ProjectID: proj.ID,
			AgentType: AgentCoder,
			Name:      "mem-agent-preset",
			Status:    AgentIdle,
		}
		if err := db.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}
		presetID := uuid.New()
		mem := Memory{
			ID:         presetID,
			AgentID:    agent.ID,
			Content:    "test memory",
			MemoryType: "summary",
		}
		if err := db.Create(&mem).Error; err != nil {
			t.Fatalf("create memory: %v", err)
		}
		var loaded Memory
		if err := db.First(&loaded, "id = ?", presetID).Error; err != nil {
			t.Fatalf("load memory: %v", err)
		}
		if loaded.ID != presetID {
			t.Errorf("ID = %v, want %v", loaded.ID, presetID)
		}
	})
}

func TestTaskTDDFields(t *testing.T) {
	tests := []struct {
		name             string
		phase            string
		testsFor         JSONArray
		tddExceptions    JSONField
		needsHumanReview bool
	}{
		{
			name:             "test phase with tests_for",
			phase:            "test",
			testsFor:         JSONArray{"0", "1"},
			tddExceptions:    nil,
			needsHumanReview: false,
		},
		{
			name:             "implementation phase with no tests_for",
			phase:            "implementation",
			testsFor:         nil,
			tddExceptions:    nil,
			needsHumanReview: false,
		},
		{
			name:     "parent task with TDD exceptions",
			phase:    "",
			testsFor: nil,
			tddExceptions: JSONField{
				"exceptions": []any{
					map[string]any{"subtask_index": float64(2), "reason": "Integration wiring only"},
				},
			},
			needsHumanReview: false,
		},
		{
			name:             "needs human review set to true",
			phase:            "",
			testsFor:         nil,
			tddExceptions:    nil,
			needsHumanReview: true,
		},
		{
			name:             "empty phase backward compatible",
			phase:            "",
			testsFor:         nil,
			tddExceptions:    nil,
			needsHumanReview: false,
		},
		{
			name:             "integration phase",
			phase:            "integration",
			testsFor:         nil,
			tddExceptions:    nil,
			needsHumanReview: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := testDB(t)

			// Create a project first (foreign key).
			proj := Project{Name: "test-proj-" + tt.name, BareRepoPath: "/tmp/test"}
			if err := db.Create(&proj).Error; err != nil {
				t.Fatalf("create project: %v", err)
			}

			task := Task{
				ProjectID:        proj.ID,
				Title:            "Test Task",
				Description:      "A test task",
				Phase:            tt.phase,
				TestsFor:         tt.testsFor,
				TDDExceptions:    tt.tddExceptions,
				NeedsHumanReview: tt.needsHumanReview,
			}

			if err := db.Create(&task).Error; err != nil {
				t.Fatalf("create task: %v", err)
			}

			// Reload from DB to verify round-trip.
			var loaded Task
			if err := db.First(&loaded, "id = ?", task.ID).Error; err != nil {
				t.Fatalf("load task: %v", err)
			}

			if loaded.Phase != tt.phase {
				t.Errorf("Phase = %q, want %q", loaded.Phase, tt.phase)
			}

			if len(tt.testsFor) == 0 {
				if len(loaded.TestsFor) != 0 {
					t.Errorf("TestsFor = %v, want nil/empty", loaded.TestsFor)
				}
			} else {
				if len(loaded.TestsFor) != len(tt.testsFor) {
					t.Errorf("TestsFor length = %d, want %d", len(loaded.TestsFor), len(tt.testsFor))
				} else {
					for i, v := range loaded.TestsFor {
						if v != tt.testsFor[i] {
							t.Errorf("TestsFor[%d] = %q, want %q", i, v, tt.testsFor[i])
						}
					}
				}
			}

			if tt.tddExceptions == nil {
				if loaded.TDDExceptions != nil {
					t.Errorf("TDDExceptions = %v, want nil", loaded.TDDExceptions)
				}
			} else {
				if loaded.TDDExceptions == nil {
					t.Errorf("TDDExceptions = nil, want %v", tt.tddExceptions)
				}
			}

			if loaded.NeedsHumanReview != tt.needsHumanReview {
				t.Errorf("NeedsHumanReview = %v, want %v", loaded.NeedsHumanReview, tt.needsHumanReview)
			}
		})
	}
}

func TestTaskNeedsHumanReviewDefaultsFalse(t *testing.T) {
	db := testDB(t)

	proj := Project{Name: "test-proj-default", BareRepoPath: "/tmp/test"}
	if err := db.Create(&proj).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create task without setting NeedsHumanReview — should default to false.
	task := Task{
		ProjectID:   proj.ID,
		Title:       "Default Task",
		Description: "Check default",
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	var loaded Task
	if err := db.First(&loaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}

	if loaded.NeedsHumanReview {
		t.Error("NeedsHumanReview should default to false")
	}
	if loaded.Phase != "" {
		t.Errorf("Phase should default to empty string, got %q", loaded.Phase)
	}
}

func TestTaskCommentBeforeCreate(t *testing.T) {
	t.Run("generates UUID when nil", func(t *testing.T) {
		db := testDB(t)
		_, task := createProjectAndTask(t, db)
		comment := TaskComment{
			TaskID: task.ID,
			Author: "user",
			Body:   "test comment",
		}
		if err := db.Create(&comment).Error; err != nil {
			t.Fatalf("create comment: %v", err)
		}
		var loaded TaskComment
		if err := db.First(&loaded, "id = ?", comment.ID).Error; err != nil {
			t.Fatalf("load comment: %v", err)
		}
		if loaded.ID == uuid.Nil {
			t.Errorf("expected non-nil UUID, got nil")
		}
	})

	t.Run("preserves pre-set UUID", func(t *testing.T) {
		db := testDB(t)
		_, task := createProjectAndTask(t, db)
		presetID := uuid.New()
		comment := TaskComment{
			ID:     presetID,
			TaskID: task.ID,
			Author: "system",
			Body:   "test comment",
		}
		if err := db.Create(&comment).Error; err != nil {
			t.Fatalf("create comment: %v", err)
		}
		var loaded TaskComment
		if err := db.First(&loaded, "id = ?", presetID).Error; err != nil {
			t.Fatalf("load comment: %v", err)
		}
		if loaded.ID != presetID {
			t.Errorf("ID = %v, want %v", loaded.ID, presetID)
		}
	})
}
