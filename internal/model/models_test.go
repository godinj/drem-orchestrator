package model

import (
	"encoding/json"
	"reflect"
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
	if err := db.AutoMigrate(&Project{}, &Task{}, &TaskSpecification{}, &TaskAcceptanceCriterion{}, &Agent{}, &WorkerAttempt{}, &AttemptEvent{}, &TaskEvent{}, &Memory{}, &TaskComment{}, &BranchAcceptanceRecord{}, &PreliminaryGateRun{}, &DeliveryArtifact{}, &VerificationRecord{}, &VerificationInteraction{}, &IntegrationAuthorization{}, &DeliveryReworkRecord{}, &HostReworkSession{}, &HostReworkSubmission{}, &CodexGoalUsage{}, &MergeIntent{}, &MergeCompletion{}, &TaskMutationRecord{}, &BugReport{}, &BugReportComment{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	// Register UUID callback (mirrors db.registerUUIDCallback).
	db.Callback().Create().Before("gorm:create").Register("generate_uuid", func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Dest == nil {
			return
		}
		val := reflect.ValueOf(tx.Statement.Dest)
		if val.Kind() == reflect.Ptr {
			val = val.Elem()
		}
		if val.Kind() != reflect.Struct {
			return
		}
		idField := val.FieldByName("ID")
		if !idField.IsValid() || idField.Type() != reflect.TypeOf(uuid.UUID{}) {
			return
		}
		if idField.Interface().(uuid.UUID) == uuid.Nil {
			idField.Set(reflect.ValueOf(uuid.New()))
		}
	})
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

	t.Run("requires event type", func(t *testing.T) {
		db := testDB(t)
		_, task := createProjectAndTask(t, db)
		event := TaskEvent{TaskID: task.ID, Actor: "test"}
		if err := db.Create(&event).Error; err == nil {
			t.Fatal("expected missing event_type to fail")
		}
	})

	t.Run("quarantines zero task id", func(t *testing.T) {
		db := testDB(t)
		event := TaskEvent{
			EventType: "container_died",
			Actor:     "docker-events",
			Details:   JSONField{"container_id": "c-stale"},
		}
		if err := db.Create(&event).Error; err != nil {
			t.Fatalf("create quarantined event: %v", err)
		}
		var loaded TaskEvent
		if err := db.First(&loaded, "id = ?", event.ID).Error; err != nil {
			t.Fatalf("load event: %v", err)
		}
		if loaded.EventType != TaskEventQuarantined {
			t.Fatalf("EventType = %q, want %q", loaded.EventType, TaskEventQuarantined)
		}
		if loaded.Details["quarantined"] != true {
			t.Fatalf("expected quarantined detail, got %#v", loaded.Details)
		}
		if loaded.Details["original_event_type"] != "container_died" {
			t.Fatalf("expected original event type in details, got %#v", loaded.Details)
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

func TestSubtaskPlanDepthMetadata(t *testing.T) {
	tests := []struct {
		name string
		plan SubtaskPlan
		// wantBoundaries and wantShapes indicate expected nil-ness after round-trip
		wantBoundariesNil bool
		wantShapesNil     bool
	}{
		{
			name: "with depth metadata round-trips through JSON",
			plan: SubtaskPlan{
				Title:          "Add depth analysis",
				Description:    "Implement depth constraint engine",
				AgentType:      "coder",
				EstimatedFiles: []string{"internal/constraints/depth/depth.go"},
				Phase:          "implementation",
				TestsFor:       []int{0},
				ModuleBoundaries: []ModuleBoundary{
					{
						Package:     "internal/constraints/depth",
						Description: "Module depth analysis and enforcement",
						Exports:     3,
					},
				},
				InterfaceShapes: []InterfaceShape{
					{
						Package:   "internal/constraints/depth",
						Functions: []string{"Analyze(root string) (Report, error)", "Check(report Report, limit int) []Violation"},
						Types:     []string{"Report", "Violation"},
					},
				},
			},
			wantBoundariesNil: false,
			wantShapesNil:     false,
		},
		{
			name: "legacy plan without depth metadata unmarshals with nil slices",
			plan: SubtaskPlan{
				Title:          "Legacy subtask",
				Description:    "A plan from before depth metadata existed",
				AgentType:      "coder",
				EstimatedFiles: []string{"main.go"},
			},
			wantBoundariesNil: true,
			wantShapesNil:     true,
		},
		{
			name: "multiple module boundaries and interface shapes",
			plan: SubtaskPlan{
				Title:          "Multi-module subtask",
				Description:    "A subtask spanning multiple modules",
				AgentType:      "coder",
				EstimatedFiles: []string{"internal/a/a.go", "internal/b/b.go"},
				ModuleBoundaries: []ModuleBoundary{
					{Package: "internal/a", Description: "First module", Exports: 2},
					{Package: "internal/b", Description: "Second module", Exports: 5},
				},
				InterfaceShapes: []InterfaceShape{
					{Package: "internal/a", Functions: []string{"New() *A"}, Types: []string{"A"}},
					{Package: "internal/b", Functions: []string{"Run(ctx context.Context) error"}, Types: []string{"Runner", "Config"}},
				},
			},
			wantBoundariesNil: false,
			wantShapesNil:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.plan)
			if err != nil {
				t.Fatalf("marshal SubtaskPlan: %v", err)
			}

			var got SubtaskPlan
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal SubtaskPlan: %v", err)
			}

			if got.Title != tt.plan.Title {
				t.Errorf("Title = %q, want %q", got.Title, tt.plan.Title)
			}
			if got.Description != tt.plan.Description {
				t.Errorf("Description = %q, want %q", got.Description, tt.plan.Description)
			}

			if tt.wantBoundariesNil {
				if got.ModuleBoundaries != nil {
					t.Errorf("ModuleBoundaries = %v, want nil", got.ModuleBoundaries)
				}
			} else {
				if !reflect.DeepEqual(got.ModuleBoundaries, tt.plan.ModuleBoundaries) {
					t.Errorf("ModuleBoundaries = %v, want %v", got.ModuleBoundaries, tt.plan.ModuleBoundaries)
				}
			}

			if tt.wantShapesNil {
				if got.InterfaceShapes != nil {
					t.Errorf("InterfaceShapes = %v, want nil", got.InterfaceShapes)
				}
			} else {
				if !reflect.DeepEqual(got.InterfaceShapes, tt.plan.InterfaceShapes) {
					t.Errorf("InterfaceShapes = %v, want %v", got.InterfaceShapes, tt.plan.InterfaceShapes)
				}
			}
		})
	}
}

func TestModuleBoundaryJSON(t *testing.T) {
	tests := []struct {
		name     string
		boundary ModuleBoundary
	}{
		{
			name: "full boundary",
			boundary: ModuleBoundary{
				Package:     "internal/constraints/depth",
				Description: "Depth analysis engine",
				Exports:     4,
			},
		},
		{
			name: "zero exports",
			boundary: ModuleBoundary{
				Package:     "internal/util",
				Description: "Internal utilities",
				Exports:     0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.boundary)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var got ModuleBoundary
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if !reflect.DeepEqual(got, tt.boundary) {
				t.Errorf("round-trip = %+v, want %+v", got, tt.boundary)
			}
		})
	}
}

func TestInterfaceShapeJSON(t *testing.T) {
	tests := []struct {
		name  string
		shape InterfaceShape
	}{
		{
			name: "full shape",
			shape: InterfaceShape{
				Package:   "internal/constraints/depth",
				Functions: []string{"Analyze(root string) (Report, error)", "Check(r Report, limit int) []Violation"},
				Types:     []string{"Report", "Violation"},
			},
		},
		{
			name: "types only",
			shape: InterfaceShape{
				Package:   "internal/model",
				Functions: nil,
				Types:     []string{"ModuleBoundary", "InterfaceShape"},
			},
		},
		{
			name: "functions only",
			shape: InterfaceShape{
				Package:   "internal/util",
				Functions: []string{"Must(err error)"},
				Types:     nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.shape)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var got InterfaceShape
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if !reflect.DeepEqual(got, tt.shape) {
				t.Errorf("round-trip = %+v, want %+v", got, tt.shape)
			}
		})
	}
}

func TestSubtaskPlanDepthMetadataGORMRoundTrip(t *testing.T) {
	db := testDB(t)

	proj := Project{Name: "depth-gorm-test", BareRepoPath: "/tmp/test"}
	if err := db.Create(&proj).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	subtasks := []SubtaskPlan{
		{
			Title:          "With depth metadata",
			Description:    "Has boundaries and shapes",
			AgentType:      "coder",
			EstimatedFiles: []string{"internal/depth/depth.go"},
			Phase:          "implementation",
			ModuleBoundaries: []ModuleBoundary{
				{Package: "internal/depth", Description: "Depth engine", Exports: 3},
			},
			InterfaceShapes: []InterfaceShape{
				{
					Package:   "internal/depth",
					Functions: []string{"Analyze(root string) (Report, error)"},
					Types:     []string{"Report"},
				},
			},
		},
		{
			Title:          "Without depth metadata",
			Description:    "Legacy subtask, no boundaries or shapes",
			AgentType:      "coder",
			EstimatedFiles: []string{"main.go"},
		},
	}

	// Marshal the subtask plan list into a JSONField for storage in Task.Plan.
	planData, err := json.Marshal(map[string]any{"subtasks": subtasks})
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	var planField JSONField
	if err := json.Unmarshal(planData, &planField); err != nil {
		t.Fatalf("unmarshal to JSONField: %v", err)
	}

	task := Task{
		ProjectID:   proj.ID,
		Title:       "Depth metadata GORM round-trip",
		Description: "Verify depth metadata survives GORM serialization",
		Plan:        planField,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Reload from DB.
	var loaded Task
	if err := db.First(&loaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}

	if loaded.Plan == nil {
		t.Fatal("Plan is nil after reload")
	}

	// Re-extract subtasks from the loaded plan.
	rawSubtasks, ok := loaded.Plan["subtasks"]
	if !ok {
		t.Fatal("Plan missing 'subtasks' key after reload")
	}

	// GORM/JSON round-trip goes through map[string]any, so re-marshal and unmarshal
	// to get typed SubtaskPlan values.
	subtaskBytes, err := json.Marshal(rawSubtasks)
	if err != nil {
		t.Fatalf("re-marshal subtasks: %v", err)
	}
	var loadedSubtasks []SubtaskPlan
	if err := json.Unmarshal(subtaskBytes, &loadedSubtasks); err != nil {
		t.Fatalf("unmarshal subtasks: %v", err)
	}

	if len(loadedSubtasks) != 2 {
		t.Fatalf("expected 2 subtasks, got %d", len(loadedSubtasks))
	}

	// Subtask 0: should have depth metadata.
	s0 := loadedSubtasks[0]
	if len(s0.ModuleBoundaries) != 1 {
		t.Errorf("subtask 0: ModuleBoundaries length = %d, want 1", len(s0.ModuleBoundaries))
	} else {
		b := s0.ModuleBoundaries[0]
		if b.Package != "internal/depth" {
			t.Errorf("subtask 0: boundary Package = %q, want %q", b.Package, "internal/depth")
		}
		if b.Exports != 3 {
			t.Errorf("subtask 0: boundary Exports = %d, want 3", b.Exports)
		}
	}
	if len(s0.InterfaceShapes) != 1 {
		t.Errorf("subtask 0: InterfaceShapes length = %d, want 1", len(s0.InterfaceShapes))
	} else {
		iface := s0.InterfaceShapes[0]
		if iface.Package != "internal/depth" {
			t.Errorf("subtask 0: shape Package = %q, want %q", iface.Package, "internal/depth")
		}
		if len(iface.Functions) != 1 || iface.Functions[0] != "Analyze(root string) (Report, error)" {
			t.Errorf("subtask 0: shape Functions = %v, unexpected", iface.Functions)
		}
	}

	// Subtask 1: should have nil depth metadata (legacy).
	s1 := loadedSubtasks[1]
	if s1.ModuleBoundaries != nil {
		t.Errorf("subtask 1: ModuleBoundaries = %v, want nil", s1.ModuleBoundaries)
	}
	if s1.InterfaceShapes != nil {
		t.Errorf("subtask 1: InterfaceShapes = %v, want nil", s1.InterfaceShapes)
	}
}

func TestSubtaskPlanLegacyJSONBackwardCompatible(t *testing.T) {
	// Simulate a legacy JSON payload that predates depth metadata fields.
	legacyJSON := `{
		"title": "Old subtask",
		"description": "Before depth fields existed",
		"agent_type": "coder",
		"estimated_files": ["main.go"]
	}`

	var plan SubtaskPlan
	if err := json.Unmarshal([]byte(legacyJSON), &plan); err != nil {
		t.Fatalf("unmarshal legacy JSON: %v", err)
	}

	if plan.Title != "Old subtask" {
		t.Errorf("Title = %q, want %q", plan.Title, "Old subtask")
	}
	if plan.ModuleBoundaries != nil {
		t.Errorf("ModuleBoundaries = %v, want nil", plan.ModuleBoundaries)
	}
	if plan.InterfaceShapes != nil {
		t.Errorf("InterfaceShapes = %v, want nil", plan.InterfaceShapes)
	}
}

func TestAgentModelIDAndEffortFieldsExist(t *testing.T) {
	t.Run("ModelID field can be set and retrieved", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		modelID := "claude-opus-4-6"
		agent := Agent{
			ProjectID: proj.ID,
			AgentType: AgentCoder,
			Name:      "test-agent",
			Status:    AgentIdle,
			ModelID:   modelID,
		}
		if err := db.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}

		var loaded Agent
		if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
			t.Fatalf("load agent: %v", err)
		}

		if loaded.ModelID != modelID {
			t.Errorf("ModelID = %q, want %q", loaded.ModelID, modelID)
		}
	})

	t.Run("Effort field can be set and retrieved", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		effort := "high"
		agent := Agent{
			ProjectID: proj.ID,
			AgentType: AgentCoder,
			Name:      "test-agent",
			Status:    AgentIdle,
			Effort:    effort,
		}
		if err := db.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}

		var loaded Agent
		if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
			t.Fatalf("load agent: %v", err)
		}

		if loaded.Effort != effort {
			t.Errorf("Effort = %q, want %q", loaded.Effort, effort)
		}
	})

	t.Run("both ModelID and Effort can be set together", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		modelID := "claude-sonnet-4-6"
		effort := "medium"
		agent := Agent{
			ProjectID: proj.ID,
			AgentType: AgentCoder,
			Name:      "test-agent",
			Status:    AgentIdle,
			ModelID:   modelID,
			Effort:    effort,
		}
		if err := db.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}

		var loaded Agent
		if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
			t.Fatalf("load agent: %v", err)
		}

		if loaded.ModelID != modelID {
			t.Errorf("ModelID = %q, want %q", loaded.ModelID, modelID)
		}
		if loaded.Effort != effort {
			t.Errorf("Effort = %q, want %q", loaded.Effort, effort)
		}
	})
}

func TestAgentModelIDAndEffortZeroValues(t *testing.T) {
	t.Run("ModelID defaults to empty string when not set", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		agent := Agent{
			ProjectID: proj.ID,
			AgentType: AgentCoder,
			Name:      "test-agent",
			Status:    AgentIdle,
			// ModelID not set
		}
		if err := db.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}

		var loaded Agent
		if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
			t.Fatalf("load agent: %v", err)
		}

		if loaded.ModelID != "" {
			t.Errorf("ModelID = %q, want empty string", loaded.ModelID)
		}
	})

	t.Run("Effort defaults to empty string when not set", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		agent := Agent{
			ProjectID: proj.ID,
			AgentType: AgentCoder,
			Name:      "test-agent",
			Status:    AgentIdle,
			// Effort not set
		}
		if err := db.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}

		var loaded Agent
		if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
			t.Fatalf("load agent: %v", err)
		}

		if loaded.Effort != "" {
			t.Errorf("Effort = %q, want empty string", loaded.Effort)
		}
	})

	t.Run("both ModelID and Effort default to empty strings when not set", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		agent := Agent{
			ProjectID: proj.ID,
			AgentType: AgentCoder,
			Name:      "test-agent",
			Status:    AgentIdle,
			// Neither field set
		}
		if err := db.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}

		var loaded Agent
		if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
			t.Fatalf("load agent: %v", err)
		}

		if loaded.ModelID != "" {
			t.Errorf("ModelID = %q, want empty string", loaded.ModelID)
		}
		if loaded.Effort != "" {
			t.Errorf("Effort = %q, want empty string", loaded.Effort)
		}
	})
}

func TestAgentModelIDAndEffortGORMRoundTrip(t *testing.T) {
	t.Run("multiple agents with different ModelID and Effort values", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)

		testCases := []struct {
			name    string
			modelID string
			effort  string
		}{
			{
				name:    "default model, low effort",
				modelID: "",
				effort:  "low",
			},
			{
				name:    "opus model, high effort",
				modelID: "claude-opus-4-6",
				effort:  "high",
			},
			{
				name:    "sonnet model, medium effort",
				modelID: "claude-sonnet-4-6",
				effort:  "medium",
			},
			{
				name:    "haiku model, low effort",
				modelID: "claude-haiku-4-5",
				effort:  "low",
			},
			{
				name:    "both empty",
				modelID: "",
				effort:  "",
			},
		}

		agents := make([]Agent, 0, len(testCases))
		for _, tc := range testCases {
			agent := Agent{
				ProjectID: proj.ID,
				AgentType: AgentCoder,
				Name:      "agent-" + tc.name,
				Status:    AgentIdle,
				ModelID:   tc.modelID,
				Effort:    tc.effort,
			}
			if err := db.Create(&agent).Error; err != nil {
				t.Fatalf("create agent for %s: %v", tc.name, err)
			}
			agents = append(agents, agent)
		}

		// Verify all agents round-trip correctly
		for i, tc := range testCases {
			var loaded Agent
			if err := db.First(&loaded, "id = ?", agents[i].ID).Error; err != nil {
				t.Fatalf("load agent for %s: %v", tc.name, err)
			}

			if loaded.ModelID != tc.modelID {
				t.Errorf("agent %s: ModelID = %q, want %q", tc.name, loaded.ModelID, tc.modelID)
			}
			if loaded.Effort != tc.effort {
				t.Errorf("agent %s: Effort = %q, want %q", tc.name, loaded.Effort, tc.effort)
			}
		}
	})

	t.Run("updating ModelID and Effort persists to database", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)
		agent := Agent{
			ProjectID: proj.ID,
			AgentType: AgentCoder,
			Name:      "test-agent",
			Status:    AgentIdle,
			ModelID:   "claude-haiku-4-5",
			Effort:    "low",
		}
		if err := db.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}

		// Update fields
		agent.ModelID = "claude-opus-4-6"
		agent.Effort = "high"
		if err := db.Save(&agent).Error; err != nil {
			t.Fatalf("update agent: %v", err)
		}

		// Reload and verify
		var loaded Agent
		if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
			t.Fatalf("load agent: %v", err)
		}

		if loaded.ModelID != "claude-opus-4-6" {
			t.Errorf("ModelID = %q, want %q", loaded.ModelID, "claude-opus-4-6")
		}
		if loaded.Effort != "high" {
			t.Errorf("Effort = %q, want %q", loaded.Effort, "high")
		}
	})
}

func TestAgentModelIDAndEffortWithTestutil(t *testing.T) {
	t.Run("CreateAgentWithOptions sets ModelID and Effort", func(t *testing.T) {
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)

		// This test documents how CreateAgent should be enhanced to support ModelID/Effort
		// The test will fail until testutil.CreateAgent is updated to accept these fields.
		// For now, we test that we can manually set them on created agents.
		agent := Agent{
			ProjectID: proj.ID,
			AgentType: AgentCoder,
			Name:      "test-agent",
			Status:    AgentIdle,
			ModelID:   "claude-opus-4-6",
			Effort:    "high",
		}
		if err := db.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}

		var loaded Agent
		if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
			t.Fatalf("load agent: %v", err)
		}

		if loaded.ModelID != "claude-opus-4-6" {
			t.Errorf("ModelID = %q, want %q", loaded.ModelID, "claude-opus-4-6")
		}
		if loaded.Effort != "high" {
			t.Errorf("Effort = %q, want %q", loaded.Effort, "high")
		}
	})

	t.Run("agent with config default model uses empty ModelID", func(t *testing.T) {
		// When config has Model="", Agent.ModelID should also be ""
		// (meaning the Claude CLI will use its default)
		db := testDB(t)
		proj, _ := createProjectAndTask(t, db)

		agent := Agent{
			ProjectID: proj.ID,
			AgentType: AgentCoder,
			Name:      "test-agent",
			Status:    AgentIdle,
			ModelID:   "", // Inherits CLI default
			Effort:    "medium",
		}
		if err := db.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}

		var loaded Agent
		if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
			t.Fatalf("load agent: %v", err)
		}

		if loaded.ModelID != "" {
			t.Errorf("ModelID = %q, want empty string (inherits CLI default)", loaded.ModelID)
		}
		if loaded.Effort != "medium" {
			t.Errorf("Effort = %q, want %q", loaded.Effort, "medium")
		}
	})
}
