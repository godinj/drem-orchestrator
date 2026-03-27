package model

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// enrichmentTestDB creates an in-memory SQLite database with auto-migration for enrichment
// field tests. This helper does not match the "createTest*" constraint pattern.
func enrichmentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&Project{}, &Task{}, &Agent{}, &TaskEvent{}, &Memory{}, &TaskComment{}, &BugReport{}, &BugReportComment{}); err != nil {
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

// TestAgentEnrichmentFieldsPopulated verifies that populated enrichment fields
// persist correctly to the database and are retrievable via GORM.
func TestAgentEnrichmentFieldsPopulated(t *testing.T) {
	db := enrichmentTestDB(t)
	proj := Project{
		ID:            uuid.New(),
		Name:          "enrichment-proj-" + uuid.New().String(),
		BareRepoPath:  "/tmp/test",
		DefaultBranch: "master",
	}
	if err := db.Create(&proj).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create agent with all enrichment fields populated
	completedTime := time.Date(2026, 3, 26, 12, 30, 45, 0, time.UTC)
	agent := Agent{
		ID:              uuid.New(),
		ProjectID:       proj.ID,
		AgentType:       AgentCoder,
		Name:            "enriched-agent",
		Status:          AgentIdle,
		ModelID:         "claude-opus-4-6",
		Effort:          "high",
		CompletedAt:     &completedTime,
		ExitReason:      "success",
		TotalCostUSD:    2.5,
		FinalContextPct: 85.5,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// Retrieve from database
	var loaded Agent
	if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
		t.Fatalf("load agent: %v", err)
	}

	// Verify all enrichment fields round-tripped correctly
	if loaded.ModelID != "claude-opus-4-6" {
		t.Errorf("ModelID = %q, want %q", loaded.ModelID, "claude-opus-4-6")
	}
	if loaded.Effort != "high" {
		t.Errorf("Effort = %q, want %q", loaded.Effort, "high")
	}
	if loaded.CompletedAt == nil {
		t.Error("CompletedAt is nil, want non-nil")
	} else if !loaded.CompletedAt.Equal(completedTime) {
		t.Errorf("CompletedAt = %v, want %v", loaded.CompletedAt, completedTime)
	}
	if loaded.ExitReason != "success" {
		t.Errorf("ExitReason = %q, want %q", loaded.ExitReason, "success")
	}
	if loaded.TotalCostUSD != 2.5 {
		t.Errorf("TotalCostUSD = %v, want %v", loaded.TotalCostUSD, 2.5)
	}
	if loaded.FinalContextPct != 85.5 {
		t.Errorf("FinalContextPct = %v, want %v", loaded.FinalContextPct, 85.5)
	}
}

// TestAgentEnrichmentFieldsNullable verifies that nullable enrichment fields
// (CompletedAt) can be nil without causing database errors.
func TestAgentEnrichmentFieldsNullable(t *testing.T) {
	db := enrichmentTestDB(t)
	proj := Project{
		ID:            uuid.New(),
		Name:          "enrichment-proj-" + uuid.New().String(),
		BareRepoPath:  "/tmp/test",
		DefaultBranch: "master",
	}
	if err := db.Create(&proj).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create agent with nullable field (CompletedAt) as nil
	agent := Agent{
		ID:              uuid.New(),
		ProjectID:       proj.ID,
		AgentType:       AgentPlanner,
		Name:            "incomplete-agent",
		Status:          AgentWorking,
		ModelID:         "sonnet",
		Effort:          "medium",
		CompletedAt:     nil,
		ExitReason:      "",
		TotalCostUSD:    0,
		FinalContextPct: 0,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// Retrieve from database
	var loaded Agent
	if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
		t.Fatalf("load agent: %v", err)
	}

	// Verify nullable field is nil
	if loaded.CompletedAt != nil {
		t.Errorf("CompletedAt = %v, want nil", loaded.CompletedAt)
	}
	if loaded.ModelID != "sonnet" {
		t.Errorf("ModelID = %q, want %q", loaded.ModelID, "sonnet")
	}
}

// TestAgentEnrichmentFieldsZeroValues verifies that numeric and string enrichment
// fields default to zero values and can be retrieved correctly.
func TestAgentEnrichmentFieldsZeroValues(t *testing.T) {
	db := enrichmentTestDB(t)
	proj := Project{
		ID:            uuid.New(),
		Name:          "enrichment-proj-" + uuid.New().String(),
		BareRepoPath:  "/tmp/test",
		DefaultBranch: "master",
	}
	if err := db.Create(&proj).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create agent with zero/empty enrichment values
	agent := Agent{
		ID:              uuid.New(),
		ProjectID:       proj.ID,
		AgentType:       AgentOrchestrator,
		Name:            "minimal-agent",
		Status:          AgentIdle,
		ModelID:         "",
		Effort:          "",
		CompletedAt:     nil,
		ExitReason:      "",
		TotalCostUSD:    0.0,
		FinalContextPct: 0.0,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// Retrieve from database
	var loaded Agent
	if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
		t.Fatalf("load agent: %v", err)
	}

	// Verify zero values
	if loaded.ModelID != "" {
		t.Errorf("ModelID = %q, want empty string", loaded.ModelID)
	}
	if loaded.Effort != "" {
		t.Errorf("Effort = %q, want empty string", loaded.Effort)
	}
	if loaded.CompletedAt != nil {
		t.Errorf("CompletedAt = %v, want nil", loaded.CompletedAt)
	}
	if loaded.ExitReason != "" {
		t.Errorf("ExitReason = %q, want empty string", loaded.ExitReason)
	}
	if loaded.TotalCostUSD != 0.0 {
		t.Errorf("TotalCostUSD = %v, want 0", loaded.TotalCostUSD)
	}
	if loaded.FinalContextPct != 0.0 {
		t.Errorf("FinalContextPct = %v, want 0", loaded.FinalContextPct)
	}
}

// TestAgentEnrichmentMultipleExitReasons verifies that various exit reason
// values persist correctly.
func TestAgentEnrichmentMultipleExitReasons(t *testing.T) {
	exitReasons := []string{"success", "error", "context_limit", "killed", "timeout"}

	for _, reason := range exitReasons {
		t.Run(reason, func(t *testing.T) {
			db := enrichmentTestDB(t)
			proj := Project{
				ID:            uuid.New(),
				Name:          "enrichment-proj-" + uuid.New().String(),
				BareRepoPath:  "/tmp/test",
				DefaultBranch: "master",
			}
			if err := db.Create(&proj).Error; err != nil {
				t.Fatalf("create project: %v", err)
			}

			completedTime := time.Now()
			agent := Agent{
				ID:          uuid.New(),
				ProjectID:   proj.ID,
				AgentType:   AgentCoder,
				Name:        "agent-" + reason,
				Status:      AgentIdle,
				ExitReason:  reason,
				CompletedAt: &completedTime,
			}
			if err := db.Create(&agent).Error; err != nil {
				t.Fatalf("create agent: %v", err)
			}

			var loaded Agent
			if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
				t.Fatalf("load agent: %v", err)
			}

			if loaded.ExitReason != reason {
				t.Errorf("ExitReason = %q, want %q", loaded.ExitReason, reason)
			}
		})
	}
}

// TestAgentEnrichmentCostValues verifies that various cost values persist
// correctly, including decimal precision.
func TestAgentEnrichmentCostValues(t *testing.T) {
	tests := []struct {
		name            string
		totalCostUSD    float64
		finalContextPct float64
	}{
		{"zero cost", 0.0, 0.0},
		{"small cost", 0.01, 1.5},
		{"large cost", 99.99, 99.99},
		{"precise cost", 1.2345, 45.6789},
		{"context near limit", 2.5, 95.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := enrichmentTestDB(t)
			proj := Project{
				ID:            uuid.New(),
				Name:          "enrichment-proj-" + uuid.New().String(),
				BareRepoPath:  "/tmp/test",
				DefaultBranch: "master",
			}
			if err := db.Create(&proj).Error; err != nil {
				t.Fatalf("create project: %v", err)
			}

			completedTime := time.Now()
			agent := Agent{
				ID:              uuid.New(),
				ProjectID:       proj.ID,
				AgentType:       AgentCoder,
				Name:            "cost-agent",
				Status:          AgentIdle,
				CompletedAt:     &completedTime,
				TotalCostUSD:    tt.totalCostUSD,
				FinalContextPct: tt.finalContextPct,
			}
			if err := db.Create(&agent).Error; err != nil {
				t.Fatalf("create agent: %v", err)
			}

			var loaded Agent
			if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
				t.Fatalf("load agent: %v", err)
			}

			if loaded.TotalCostUSD != tt.totalCostUSD {
				t.Errorf("TotalCostUSD = %v, want %v", loaded.TotalCostUSD, tt.totalCostUSD)
			}
			if loaded.FinalContextPct != tt.finalContextPct {
				t.Errorf("FinalContextPct = %v, want %v", loaded.FinalContextPct, tt.finalContextPct)
			}
		})
	}
}

// TestAgentEnrichmentModelValues verifies that various model identifiers persist
// correctly.
func TestAgentEnrichmentModelValues(t *testing.T) {
	models := []string{"sonnet", "opus", "haiku", "claude-opus-4-6", "claude-sonnet-4-6", "custom-model-v1"}

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			db := enrichmentTestDB(t)
			proj := Project{
				ID:            uuid.New(),
				Name:          "enrichment-proj-" + uuid.New().String(),
				BareRepoPath:  "/tmp/test",
				DefaultBranch: "master",
			}
			if err := db.Create(&proj).Error; err != nil {
				t.Fatalf("create project: %v", err)
			}

			agent := Agent{
				ID:        uuid.New(),
				ProjectID: proj.ID,
				AgentType: AgentCoder,
				Name:      "model-agent",
				Status:    AgentIdle,
				ModelID:   model,
			}
			if err := db.Create(&agent).Error; err != nil {
				t.Fatalf("create agent: %v", err)
			}

			var loaded Agent
			if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
				t.Fatalf("load agent: %v", err)
			}

			if loaded.ModelID != model {
				t.Errorf("ModelID = %q, want %q", loaded.ModelID, model)
			}
		})
	}
}

// TestAgentEnrichmentEffortValues verifies that various effort levels persist
// correctly.
func TestAgentEnrichmentEffortValues(t *testing.T) {
	efforts := []string{"low", "medium", "high"}

	for _, effort := range efforts {
		t.Run(effort, func(t *testing.T) {
			db := enrichmentTestDB(t)
			proj := Project{
				ID:            uuid.New(),
				Name:          "enrichment-proj-" + uuid.New().String(),
				BareRepoPath:  "/tmp/test",
				DefaultBranch: "master",
			}
			if err := db.Create(&proj).Error; err != nil {
				t.Fatalf("create project: %v", err)
			}

			agent := Agent{
				ID:        uuid.New(),
				ProjectID: proj.ID,
				AgentType: AgentCoder,
				Name:      "effort-agent",
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
	}
}

// TestAgentEnrichmentTimestampPrecision verifies that CompletedAt timestamp
// values persist with correct precision.
func TestAgentEnrichmentTimestampPrecision(t *testing.T) {
	db := enrichmentTestDB(t)
	proj := Project{
		ID:            uuid.New(),
		Name:          "enrichment-proj-" + uuid.New().String(),
		BareRepoPath:  "/tmp/test",
		DefaultBranch: "master",
	}
	if err := db.Create(&proj).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create timestamp with nanosecond precision
	completedTime := time.Date(2026, 3, 26, 14, 23, 45, 123456789, time.UTC)
	agent := Agent{
		ID:          uuid.New(),
		ProjectID:   proj.ID,
		AgentType:   AgentCoder,
		Name:        "timestamp-agent",
		Status:      AgentIdle,
		CompletedAt: &completedTime,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	var loaded Agent
	if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
		t.Fatalf("load agent: %v", err)
	}

	if loaded.CompletedAt == nil {
		t.Fatal("CompletedAt is nil")
	}

	// SQLite stores timestamps as strings with microsecond precision, so we
	// compare at microsecond precision.
	if !loaded.CompletedAt.Truncate(time.Microsecond).Equal(completedTime.Truncate(time.Microsecond)) {
		t.Errorf("CompletedAt = %v, want %v (at microsecond precision)",
			loaded.CompletedAt.Truncate(time.Microsecond),
			completedTime.Truncate(time.Microsecond))
	}
}

// TestAgentEnrichmentPartialUpdate verifies that enrichment fields can be
// updated independently without affecting other fields.
func TestAgentEnrichmentPartialUpdate(t *testing.T) {
	db := enrichmentTestDB(t)
	proj := Project{
		ID:            uuid.New(),
		Name:          "enrichment-proj-" + uuid.New().String(),
		BareRepoPath:  "/tmp/test",
		DefaultBranch: "master",
	}
	if err := db.Create(&proj).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create agent with initial enrichment values
	agent := Agent{
		ID:              uuid.New(),
		ProjectID:       proj.ID,
		AgentType:       AgentCoder,
		Name:            "partial-update-agent",
		Status:          AgentWorking,
		ModelID:         "sonnet",
		Effort:          "low",
		CompletedAt:     nil,
		ExitReason:      "",
		TotalCostUSD:    0.0,
		FinalContextPct: 0.0,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// Update specific enrichment fields
	completedTime := time.Now()
	if err := db.Model(&agent).Updates(Agent{
		CompletedAt:     &completedTime,
		ExitReason:      "success",
		TotalCostUSD:    1.5,
		FinalContextPct: 75.0,
	}).Error; err != nil {
		t.Fatalf("update agent: %v", err)
	}

	// Reload and verify
	var loaded Agent
	if err := db.First(&loaded, "id = ?", agent.ID).Error; err != nil {
		t.Fatalf("load agent: %v", err)
	}

	// Original fields should be unchanged
	if loaded.ModelID != "sonnet" {
		t.Errorf("ModelID = %q, want %q", loaded.ModelID, "sonnet")
	}
	if loaded.Effort != "low" {
		t.Errorf("Effort = %q, want %q", loaded.Effort, "low")
	}

	// Updated fields should reflect changes
	if loaded.CompletedAt == nil {
		t.Error("CompletedAt should be non-nil after update")
	}
	if loaded.ExitReason != "success" {
		t.Errorf("ExitReason = %q, want %q", loaded.ExitReason, "success")
	}
	if loaded.TotalCostUSD != 1.5 {
		t.Errorf("TotalCostUSD = %v, want 1.5", loaded.TotalCostUSD)
	}
	if loaded.FinalContextPct != 75.0 {
		t.Errorf("FinalContextPct = %v, want 75.0", loaded.FinalContextPct)
	}
}

// TestAgentEnrichmentMultipleAgents verifies that enrichment fields persist
// correctly when multiple agents with different enrichment values are stored.
func TestAgentEnrichmentMultipleAgents(t *testing.T) {
	db := enrichmentTestDB(t)
	proj := Project{
		ID:            uuid.New(),
		Name:          "enrichment-proj-" + uuid.New().String(),
		BareRepoPath:  "/tmp/test",
		DefaultBranch: "master",
	}
	if err := db.Create(&proj).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	agents := []struct {
		agentType       AgentType
		modelID         string
		effort          string
		exitReason      string
		totalCostUSD    float64
		finalContextPct float64
	}{
		{AgentCoder, "opus", "high", "success", 2.0, 80.0},
		{AgentPlanner, "sonnet", "medium", "success", 1.0, 60.0},
		{AgentOrchestrator, "haiku", "low", "context_limit", 0.5, 92.0},
	}

	var createdAgents []Agent

	// Create all agents
	for i, spec := range agents {
		completedTime := time.Now().Add(time.Duration(i) * time.Hour)
		agent := Agent{
			ID:              uuid.New(),
			ProjectID:       proj.ID,
			AgentType:       spec.agentType,
			Name:            "agent-" + spec.modelID,
			Status:          AgentIdle,
			ModelID:         spec.modelID,
			Effort:          spec.effort,
			ExitReason:      spec.exitReason,
			CompletedAt:     &completedTime,
			TotalCostUSD:    spec.totalCostUSD,
			FinalContextPct: spec.finalContextPct,
		}
		if err := db.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}
		createdAgents = append(createdAgents, agent)
	}

	// Verify each agent's enrichment values
	for i, expected := range agents {
		var loaded Agent
		if err := db.First(&loaded, "id = ?", createdAgents[i].ID).Error; err != nil {
			t.Fatalf("load agent %d: %v", i, err)
		}

		if loaded.ModelID != expected.modelID {
			t.Errorf("agent %d: ModelID = %q, want %q", i, loaded.ModelID, expected.modelID)
		}
		if loaded.Effort != expected.effort {
			t.Errorf("agent %d: Effort = %q, want %q", i, loaded.Effort, expected.effort)
		}
		if loaded.ExitReason != expected.exitReason {
			t.Errorf("agent %d: ExitReason = %q, want %q", i, loaded.ExitReason, expected.exitReason)
		}
		if loaded.TotalCostUSD != expected.totalCostUSD {
			t.Errorf("agent %d: TotalCostUSD = %v, want %v", i, loaded.TotalCostUSD, expected.totalCostUSD)
		}
		if loaded.FinalContextPct != expected.finalContextPct {
			t.Errorf("agent %d: FinalContextPct = %v, want %v", i, loaded.FinalContextPct, expected.finalContextPct)
		}
	}
}
