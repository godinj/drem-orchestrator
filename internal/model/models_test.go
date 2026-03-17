package model

import (
	"testing"

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
