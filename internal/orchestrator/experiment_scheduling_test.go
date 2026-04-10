package orchestrator

import (
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/experiment"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// setupTestDB creates an in-memory database with a project for testing.
func setupTestDB(t *testing.T) (*gorm.DB, uuid.UUID) {
	t.Helper()
	database := testutil.NewTestDBWithModels(t, &experiment.Experiment{}, &experiment.Variant{})

	project := model.Project{
		ID:            uuid.New(),
		Name:          "test-project",
		BareRepoPath:  "/tmp/test-repo.git",
		DefaultBranch: "main",
	}
	if err := database.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	return database, project.ID
}

// TestExperimentScheduler_IsActive_NoExperiments verifies that IsActive returns
// false when there are no experiments.
func TestExperimentScheduler_IsActive_NoExperiments(t *testing.T) {
	database, _ := setupTestDB(t)
	scheduler := NewExperimentScheduler(database, 6)

	active, err := scheduler.IsActive()
	if err != nil {
		t.Fatalf("IsActive failed: %v", err)
	}
	if active {
		t.Error("should not be active when no experiments exist")
	}
}

// TestExperimentScheduler_IsActive_WithPendingExperiment verifies that IsActive
// returns false when experiments exist but are not running.
func TestExperimentScheduler_IsActive_WithPendingExperiment(t *testing.T) {
	database, projectID := setupTestDB(t)
	scheduler := NewExperimentScheduler(database, 6)

	// Create a pending experiment
	exp := experiment.Experiment{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "Test Experiment",
		Status:    experiment.StatusPending,
	}
	if err := database.Create(&exp).Error; err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	active, err := scheduler.IsActive()
	if err != nil {
		t.Fatalf("IsActive failed: %v", err)
	}
	if active {
		t.Error("should not be active when experiment is pending")
	}
}

// TestExperimentScheduler_IsActive_WithRunningExperiment verifies that IsActive
// returns true when at least one experiment is running.
func TestExperimentScheduler_IsActive_WithRunningExperiment(t *testing.T) {
	database, projectID := setupTestDB(t)
	scheduler := NewExperimentScheduler(database, 6)

	// Create a running experiment
	exp := experiment.Experiment{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "Test Experiment",
		Status:    experiment.StatusRunning,
	}
	if err := database.Create(&exp).Error; err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	active, err := scheduler.IsActive()
	if err != nil {
		t.Fatalf("IsActive failed: %v", err)
	}
	if !active {
		t.Error("should be active when experiment is running")
	}
}

// TestExperimentScheduler_IsExperimentTask verifies task classification.
func TestExperimentScheduler_IsExperimentTask(t *testing.T) {
	database, projectID := setupTestDB(t)
	scheduler := NewExperimentScheduler(database, 6)

	// Create a running experiment with a variant
	exp := experiment.Experiment{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "Test Experiment",
		Status:    experiment.StatusRunning,
	}
	if err := database.Create(&exp).Error; err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	taskID := uuid.New()
	variant := experiment.Variant{
		ID:           uuid.New(),
		ExperimentID: exp.ID,
		ProfileName:  "default",
		TaskID:       taskID,
		Status:       experiment.VariantRunning,
	}
	if err := database.Create(&variant).Error; err != nil {
		t.Fatalf("create variant: %v", err)
	}

	// Check that the variant task is recognized as an experiment task
	isExperiment, variantResult, err := scheduler.IsExperimentTask(taskID)
	if err != nil {
		t.Fatalf("IsExperimentTask failed: %v", err)
	}
	if !isExperiment {
		t.Error("variant task should be recognized as experiment task")
	}
	if variantResult.ID != variant.ID {
		t.Errorf("should return the correct variant, got %v", variantResult.ID)
	}

	// Check that a non-experiment task is not recognized
	normalTaskID := uuid.New()
	isExperiment, variantResult, err = scheduler.IsExperimentTask(normalTaskID)
	if err != nil {
		t.Fatalf("IsExperimentTask failed: %v", err)
	}
	if isExperiment {
		t.Error("normal task should not be recognized as experiment task")
	}
	if variantResult != nil {
		t.Error("should return nil variant for normal task")
	}
}

// TestExperimentScheduler_AgentsPerVariant verifies agent allocation calculation.
func TestExperimentScheduler_AgentsPerVariant(t *testing.T) {
	database, projectID := setupTestDB(t)
	scheduler := NewExperimentScheduler(database, 6)

	// Create a running experiment with 3 variants
	exp := experiment.Experiment{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "Test Experiment",
		Status:    experiment.StatusRunning,
	}
	if err := database.Create(&exp).Error; err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	profiles := []string{"default", "fast", "careful"}
	for _, profile := range profiles {
		variant := experiment.Variant{
			ID:           uuid.New(),
			ExperimentID: exp.ID,
			ProfileName:  profile,
			TaskID:       uuid.New(),
			Status:       experiment.VariantRunning,
		}
		if err := database.Create(&variant).Error; err != nil {
			t.Fatalf("create variant: %v", err)
		}
	}

	agentsPerVariant, totalVariants, err := scheduler.AgentsPerVariant()
	if err != nil {
		t.Fatalf("AgentsPerVariant failed: %v", err)
	}
	if totalVariants != 3 {
		t.Errorf("should count 3 variants, got %d", totalVariants)
	}
	if agentsPerVariant != 2 {
		t.Errorf("should allocate 2 agents per variant (6/3), got %d", agentsPerVariant)
	}
}

// TestExperimentScheduler_AgentsPerVariant_MultipleExperiments verifies allocation
// across multiple experiments.
func TestExperimentScheduler_AgentsPerVariant_MultipleExperiments(t *testing.T) {
	database, projectID := setupTestDB(t)
	scheduler := NewExperimentScheduler(database, 6)

	// Create two running experiments with 2 variants each
	for i := 1; i <= 2; i++ {
		exp := experiment.Experiment{
			ID:        uuid.New(),
			ProjectID: projectID,
			Title:     "Test Experiment " + string(rune(i+'0')),
			Status:    experiment.StatusRunning,
		}
		if err := database.Create(&exp).Error; err != nil {
			t.Fatalf("create experiment: %v", err)
		}

		for j := 1; j <= 2; j++ {
			variant := experiment.Variant{
				ID:           uuid.New(),
				ExperimentID: exp.ID,
				ProfileName:  "profile-" + string(rune(i+'0')) + "-" + string(rune(j+'0')),
				TaskID:       uuid.New(),
				Status:       experiment.VariantRunning,
			}
			if err := database.Create(&variant).Error; err != nil {
				t.Fatalf("create variant: %v", err)
			}
		}
	}

	agentsPerVariant, totalVariants, err := scheduler.AgentsPerVariant()
	if err != nil {
		t.Fatalf("AgentsPerVariant failed: %v", err)
	}
	if totalVariants != 4 {
		t.Errorf("should count 4 variants across 2 experiments, got %d", totalVariants)
	}
	if agentsPerVariant != 1 {
		t.Errorf("should allocate 1 agent per variant (6/4=1), got %d", agentsPerVariant)
	}
}

// TestExperimentScheduler_ShouldBlockNormalTasks verifies blocking behavior.
func TestExperimentScheduler_ShouldBlockNormalTasks(t *testing.T) {
	database, projectID := setupTestDB(t)
	scheduler := NewExperimentScheduler(database, 6)

	// No experiments — should not block
	block, err := scheduler.ShouldBlockNormalTasks()
	if err != nil {
		t.Fatalf("ShouldBlockNormalTasks failed: %v", err)
	}
	if block {
		t.Error("should not block when no experiments active")
	}

	// Add a running experiment — should block
	exp := experiment.Experiment{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "Test Experiment",
		Status:    experiment.StatusRunning,
	}
	if err := database.Create(&exp).Error; err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	block, err = scheduler.ShouldBlockNormalTasks()
	if err != nil {
		t.Fatalf("ShouldBlockNormalTasks failed: %v", err)
	}
	if !block {
		t.Error("should block when experiment is active")
	}
}

// TestExperimentScheduler_CanScheduleTask verifies task scheduling decisions.
func TestExperimentScheduler_CanScheduleTask(t *testing.T) {
	database, projectID := setupTestDB(t)
	scheduler := NewExperimentScheduler(database, 6)

	// Normal task with no experiments — should be schedulable
	normalTaskID := uuid.New()
	canSchedule, reason, err := scheduler.CanScheduleTask(normalTaskID)
	if err != nil {
		t.Fatalf("CanScheduleTask failed: %v", err)
	}
	if !canSchedule {
		t.Error("normal task should be schedulable with no experiments")
	}
	if reason != "no active experiments" {
		t.Errorf("unexpected reason: %s", reason)
	}

	// Add a running experiment — normal task should be blocked
	exp := experiment.Experiment{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "Test Experiment",
		Status:    experiment.StatusRunning,
	}
	if err := database.Create(&exp).Error; err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	variantTaskID := uuid.New()
	variant := experiment.Variant{
		ID:           uuid.New(),
		ExperimentID: exp.ID,
		ProfileName:  "default",
		TaskID:       variantTaskID,
		Status:       experiment.VariantRunning,
	}
	if err := database.Create(&variant).Error; err != nil {
		t.Fatalf("create variant: %v", err)
	}

	// Normal task should now be blocked
	canSchedule, reason, err = scheduler.CanScheduleTask(normalTaskID)
	if err != nil {
		t.Fatalf("CanScheduleTask failed: %v", err)
	}
	if canSchedule {
		t.Error("normal task should be blocked when experiment is active")
	}
	if reason != "experiments active, normal tasks paused" {
		t.Errorf("unexpected reason: %s", reason)
	}

	// Variant task should be schedulable
	canSchedule, reason, err = scheduler.CanScheduleTask(variantTaskID)
	if err != nil {
		t.Fatalf("CanScheduleTask failed: %v", err)
	}
	if !canSchedule {
		t.Error("variant task should be schedulable")
	}
	if reason == "" {
		t.Error("reason should not be empty")
	}
}

// TestExperimentScheduler_CanScheduleTask_VariantAtLimit verifies that variant
// tasks are blocked when their variant has reached the agent limit.
func TestExperimentScheduler_CanScheduleTask_VariantAtLimit(t *testing.T) {
	database, projectID := setupTestDB(t)
	scheduler := NewExperimentScheduler(database, 6)

	// Create a running experiment with 3 variants (2 agents each)
	exp := experiment.Experiment{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "Test Experiment",
		Status:    experiment.StatusRunning,
	}
	if err := database.Create(&exp).Error; err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	// Create 3 variants
	variantTaskID := uuid.New()
	variant := experiment.Variant{
		ID:           uuid.New(),
		ExperimentID: exp.ID,
		ProfileName:  "default",
		TaskID:       variantTaskID,
		Status:       experiment.VariantRunning,
	}
	if err := database.Create(&variant).Error; err != nil {
		t.Fatalf("create variant: %v", err)
	}

	// Create 2 additional variants to get 6/3 = 2 agents per variant
	for i := 1; i < 3; i++ {
		otherVariant := experiment.Variant{
			ID:           uuid.New(),
			ExperimentID: exp.ID,
			ProfileName:  "profile-" + string(rune(i+'0')),
			TaskID:       uuid.New(),
			Status:       experiment.VariantRunning,
		}
		if err := database.Create(&otherVariant).Error; err != nil {
			t.Fatalf("create variant: %v", err)
		}
	}

	// Create 2 working agents for the first variant task (at limit)
	for i := 0; i < 2; i++ {
		agent := model.Agent{
			ID:            uuid.New(),
			ProjectID:     projectID,
			AgentType:     model.AgentCoder,
			Name:          "test-agent-" + string(rune(i+'0')),
			Status:        model.AgentWorking,
			CurrentTaskID: &variantTaskID,
		}
		if err := database.Create(&agent).Error; err != nil {
			t.Fatalf("create agent: %v", err)
		}
	}

	// Variant task should now be blocked (at limit)
	canSchedule, reason, err := scheduler.CanScheduleTask(variantTaskID)
	if err != nil {
		t.Fatalf("CanScheduleTask failed: %v", err)
	}
	if canSchedule {
		t.Errorf("variant task should be blocked when at agent limit, reason: %s", reason)
	}
	if reason == "" {
		t.Error("reason should not be empty")
	}
}

// TestExperimentScheduler_GetUnderAllocatedVariants verifies variant ordering.
func TestExperimentScheduler_GetUnderAllocatedVariants(t *testing.T) {
	database, projectID := setupTestDB(t)
	scheduler := NewExperimentScheduler(database, 6)

	// Create a running experiment with 3 variants (2 agents each)
	exp := experiment.Experiment{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "Test Experiment",
		Status:    experiment.StatusRunning,
	}
	if err := database.Create(&exp).Error; err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	variants := make([]experiment.Variant, 3)
	for i := 0; i < 3; i++ {
		taskID := uuid.New()
		variants[i] = experiment.Variant{
			ID:           uuid.New(),
			ExperimentID: exp.ID,
			ProfileName:  "profile-" + string(rune(i+'0')),
			TaskID:       taskID,
			Status:       experiment.VariantRunning,
		}
		if err := database.Create(&variants[i]).Error; err != nil {
			t.Fatalf("create variant: %v", err)
		}

		// Add agents: variant 0 gets 0, variant 1 gets 1, variant 2 gets 2
		for j := 0; j < i; j++ {
			agent := model.Agent{
				ID:            uuid.New(),
				ProjectID:     projectID,
				AgentType:     model.AgentCoder,
				Name:          "test-agent",
				Status:        model.AgentWorking,
				CurrentTaskID: &taskID,
			}
			if err := database.Create(&agent).Error; err != nil {
				t.Fatalf("create agent: %v", err)
			}
		}
	}

	underAllocated, err := scheduler.GetUnderAllocatedVariants()
	if err != nil {
		t.Fatalf("GetUnderAllocatedVariants failed: %v", err)
	}

	// Should return 2 under-allocated variants (variant 0 and 1)
	if len(underAllocated) != 2 {
		t.Errorf("should return 2 under-allocated variants, got %d", len(underAllocated))
	}

	// Most under-allocated (variant 0 with 0 agents) should be first
	if underAllocated[0].CurrentAgents != 0 {
		t.Errorf("first should have 0 agents, got %d", underAllocated[0].CurrentAgents)
	}
	if underAllocated[0].UnderAllocation != 2 {
		t.Errorf("first should be under-allocated by 2, got %d", underAllocated[0].UnderAllocation)
	}

	// Second should be variant 1 with 1 agent
	if underAllocated[1].CurrentAgents != 1 {
		t.Errorf("second should have 1 agent, got %d", underAllocated[1].CurrentAgents)
	}
	if underAllocated[1].UnderAllocation != 1 {
		t.Errorf("second should be under-allocated by 1, got %d", underAllocated[1].UnderAllocation)
	}
}

// TestProcessBacklog_ExperimentBlocking verifies that processBacklog blocks
// normal tasks when experiments are active.
func TestProcessBacklog_ExperimentBlocking(t *testing.T) {
	database, projectID := setupTestDB(t)

	// Create orchestrator with experiment scheduling
	orch := &Orchestrator{
		db:                  database,
		projectID:           projectID,
		experimentScheduler: NewExperimentScheduler(database, 6),
		events:              make(chan Event, 16),
		logger:              slog.Default().With("component", "test-orchestrator"),
	}

	// Create a normal task in backlog
	normalTask := model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       "Normal Task",
		Description: "A normal task",
		Status:      model.StatusBacklog,
		Category:    model.CategoryStandard,
	}
	if err := database.Create(&normalTask).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	// No experiments — task should transition to planning
	err := orch.processBacklog(&normalTask)
	if err != nil {
		t.Fatalf("processBacklog failed: %v", err)
	}
	if err := database.First(&normalTask, "id = ?", normalTask.ID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	if normalTask.Status != model.StatusPlanning {
		t.Errorf("normal task should transition to planning with no experiments, got %s", normalTask.Status)
	}

	// Reset task status
	normalTask.Status = model.StatusBacklog
	if err := database.Save(&normalTask).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}

	// Add a running experiment
	exp := experiment.Experiment{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "Test Experiment",
		Status:    experiment.StatusRunning,
	}
	if err := database.Create(&exp).Error; err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	// Task should now be blocked (remain in backlog)
	err = orch.processBacklog(&normalTask)
	if err != nil {
		t.Fatalf("processBacklog failed: %v", err)
	}
	if err := database.First(&normalTask, "id = ?", normalTask.ID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	if normalTask.Status != model.StatusBacklog {
		t.Errorf("normal task should remain in backlog when experiment is active, got %s", normalTask.Status)
	}
}

// TestProcessBacklog_ExperimentTaskAllowed verifies that experiment variant
// tasks can still transition to planning when experiments are active.
func TestProcessBacklog_ExperimentTaskAllowed(t *testing.T) {
	database, projectID := setupTestDB(t)

	// Create orchestrator with experiment scheduling
	orch := &Orchestrator{
		db:                  database,
		projectID:           projectID,
		experimentScheduler: NewExperimentScheduler(database, 6),
		events:              make(chan Event, 16),
		logger:              slog.Default().With("component", "test-orchestrator"),
	}

	// Create a running experiment with a variant task
	exp := experiment.Experiment{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "Test Experiment",
		Status:    experiment.StatusRunning,
	}
	if err := database.Create(&exp).Error; err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	variantTaskID := uuid.New()
	variant := experiment.Variant{
		ID:           uuid.New(),
		ExperimentID: exp.ID,
		ProfileName:  "default",
		TaskID:       variantTaskID,
		Status:       experiment.VariantRunning,
	}
	if err := database.Create(&variant).Error; err != nil {
		t.Fatalf("create variant: %v", err)
	}

	// Create the variant task in backlog
	variantTask := model.Task{
		ID:          variantTaskID,
		ProjectID:   projectID,
		Title:       "[default] Experiment Task",
		Description: "An experiment variant task",
		Status:      model.StatusBacklog,
		Category:    model.CategoryStandard,
	}
	if err := database.Create(&variantTask).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Variant task should transition to planning
	err := orch.processBacklog(&variantTask)
	if err != nil {
		t.Fatalf("processBacklog failed: %v", err)
	}
	if err := database.First(&variantTask, "id = ?", variantTaskID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	if variantTask.Status != model.StatusPlanning {
		t.Errorf("variant task should transition to planning, got %s", variantTask.Status)
	}
}

// TestOrderCandidatesByExperimentPriority verifies candidate reordering.
func TestOrderCandidatesByExperimentPriority(t *testing.T) {
	database, projectID := setupTestDB(t)

	// Create orchestrator with experiment scheduling
	orch := &Orchestrator{
		db:                  database,
		projectID:           projectID,
		experimentScheduler: NewExperimentScheduler(database, 6),
		events:              make(chan Event, 16),
		logger:              slog.Default().With("component", "test-orchestrator"),
	}

	// Create a running experiment with 2 variants
	exp := experiment.Experiment{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "Test Experiment",
		Status:    experiment.StatusRunning,
	}
	if err := database.Create(&exp).Error; err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	variant1TaskID := uuid.New()
	variant1 := experiment.Variant{
		ID:           uuid.New(),
		ExperimentID: exp.ID,
		ProfileName:  "default",
		TaskID:       variant1TaskID,
		Status:       experiment.VariantRunning,
	}
	if err := database.Create(&variant1).Error; err != nil {
		t.Fatalf("create variant: %v", err)
	}

	variant2TaskID := uuid.New()
	variant2 := experiment.Variant{
		ID:           uuid.New(),
		ExperimentID: exp.ID,
		ProfileName:  "fast",
		TaskID:       variant2TaskID,
		Status:       experiment.VariantRunning,
	}
	if err := database.Create(&variant2).Error; err != nil {
		t.Fatalf("create variant: %v", err)
	}

	// Add 1 agent to variant2 (making variant1 more under-allocated)
	agent := model.Agent{
		ID:            uuid.New(),
		ProjectID:     projectID,
		AgentType:     model.AgentCoder,
		Name:          "test-agent",
		Status:        model.AgentWorking,
		CurrentTaskID: &variant2TaskID,
	}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// Create candidates: normal task, variant2 task, variant1 task
	normalTask := model.Task{ID: uuid.New(), ProjectID: projectID, Title: "Normal Task", Status: model.StatusBacklog}
	variant1Task := model.Task{ID: variant1TaskID, ProjectID: projectID, Title: "[default] Variant 1", Status: model.StatusBacklog}
	variant2Task := model.Task{ID: variant2TaskID, ProjectID: projectID, Title: "[fast] Variant 2", Status: model.StatusBacklog}

	candidates := []model.Task{normalTask, variant2Task, variant1Task}
	ordered := orch.orderCandidatesByExperimentPriority(candidates)

	// Should be ordered: variant1 (most under-allocated), variant2, normal
	if ordered[0].ID != variant1TaskID {
		t.Errorf("most under-allocated variant should be first, got %v", ordered[0].ID)
	}
	if ordered[1].ID != variant2TaskID {
		t.Errorf("less under-allocated variant should be second, got %v", ordered[1].ID)
	}
	if ordered[2].ID != normalTask.ID {
		t.Errorf("normal task should be last, got %v", ordered[2].ID)
	}
}

// TestExperimentScheduler_NoExperiments_NormalBehavior verifies that when
// no experiments are active, normal scheduling behavior is preserved.
func TestExperimentScheduler_NoExperiments_NormalBehavior(t *testing.T) {
	database, _ := setupTestDB(t)
	scheduler := NewExperimentScheduler(database, 6)

	// Should not block normal tasks
	block, err := scheduler.ShouldBlockNormalTasks()
	if err != nil {
		t.Fatalf("ShouldBlockNormalTasks failed: %v", err)
	}
	if block {
		t.Error("should not block when no experiments active")
	}

	// Should allow scheduling of any task
	taskID := uuid.New()
	canSchedule, reason, err := scheduler.CanScheduleTask(taskID)
	if err != nil {
		t.Fatalf("CanScheduleTask failed: %v", err)
	}
	if !canSchedule {
		t.Error("task should be schedulable with no experiments")
	}
	if reason != "no active experiments" {
		t.Errorf("unexpected reason: %s", reason)
	}

	// Should return 0 agents per variant
	agentsPerVariant, totalVariants, err := scheduler.AgentsPerVariant()
	if err != nil {
		t.Fatalf("AgentsPerVariant failed: %v", err)
	}
	if totalVariants != 0 {
		t.Errorf("should have 0 variants, got %d", totalVariants)
	}
	if agentsPerVariant != 0 {
		t.Errorf("should allocate 0 agents per variant, got %d", agentsPerVariant)
	}

	// Should return empty under-allocated list
	underAllocated, err := scheduler.GetUnderAllocatedVariants()
	if err != nil {
		t.Fatalf("GetUnderAllocatedVariants failed: %v", err)
	}
	if len(underAllocated) != 0 {
		t.Errorf("should return empty list, got %d items", len(underAllocated))
	}
}

// TestExperimentScheduler_ExperimentCompletion_ResumesNormalBehavior verifies
// that normal tasks can be scheduled again after experiments complete.
func TestExperimentScheduler_ExperimentCompletion_ResumesNormalBehavior(t *testing.T) {
	database, projectID := setupTestDB(t)
	scheduler := NewExperimentScheduler(database, 6)

	// Create and start an experiment
	exp := experiment.Experiment{
		ID:        uuid.New(),
		ProjectID: projectID,
		Title:     "Test Experiment",
		Status:    experiment.StatusRunning,
	}
	if err := database.Create(&exp).Error; err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	// Should block normal tasks
	block, err := scheduler.ShouldBlockNormalTasks()
	if err != nil {
		t.Fatalf("ShouldBlockNormalTasks failed: %v", err)
	}
	if !block {
		t.Error("should block while experiment is running")
	}

	// Complete the experiment
	if err := database.Model(&experiment.Experiment{}).
		Where("id = ?", exp.ID).
		Update("status", experiment.StatusCompleted).Error; err != nil {
		t.Fatalf("update experiment: %v", err)
	}

	// Should now allow normal tasks
	block, err = scheduler.ShouldBlockNormalTasks()
	if err != nil {
		t.Fatalf("ShouldBlockNormalTasks failed: %v", err)
	}
	if block {
		t.Error("should allow normal tasks after experiment completes")
	}

	// Normal task should be schedulable
	normalTaskID := uuid.New()
	canSchedule, reason, err := scheduler.CanScheduleTask(normalTaskID)
	if err != nil {
		t.Fatalf("CanScheduleTask failed: %v", err)
	}
	if !canSchedule {
		t.Error("normal task should be schedulable after experiment completes")
	}
	if reason != "no active experiments" {
		t.Errorf("unexpected reason: %s", reason)
	}
}
