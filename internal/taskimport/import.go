package taskimport

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// Import parses a Markdown task file and creates all tasks in the database.
// Tasks whose titles already exist in the project are skipped.
// Returns the number of tasks created.
func Import(r io.Reader, db *gorm.DB, projectID uuid.UUID) (int, error) {
	parsed, err := Parse(r)
	if err != nil {
		return 0, fmt.Errorf("parse: %w", err)
	}

	// Build a title->ID map for dependency resolution (includes existing tasks).
	titleToID := make(map[string]uuid.UUID)
	var existing []model.Task
	if err := db.Where("project_id = ?", projectID).Find(&existing).Error; err != nil {
		return 0, fmt.Errorf("load existing tasks: %w", err)
	}
	for _, t := range existing {
		titleToID[t.Title] = t.ID
	}

	created := 0

	for _, pt := range parsed {
		// Skip if parent already exists.
		if _, exists := titleToID[pt.Title]; exists {
			slog.Info("skipping existing task", "title", pt.Title)
			continue
		}

		parent := &model.Task{
			ID:          uuid.New(),
			ProjectID:   projectID,
			Title:       pt.Title,
			Description: pt.Description,
			Status:      model.StatusBacklog,
			Priority:    pt.Priority,
			Labels:      toJSONArray(pt.Labels),
		}

		if err := db.Create(parent).Error; err != nil {
			return created, fmt.Errorf("create task %q: %w", pt.Title, err)
		}
		titleToID[pt.Title] = parent.ID
		created++
		slog.Info("created task", "title", pt.Title, "id", parent.ID)

		// Create subtasks.
		for _, sp := range pt.Subtasks {
			if _, exists := titleToID[sp.Title]; exists {
				slog.Info("skipping existing subtask", "title", sp.Title)
				continue
			}

			sub := &model.Task{
				ID:           uuid.New(),
				ProjectID:    projectID,
				ParentTaskID: &parent.ID,
				Title:        sp.Title,
				Description:  sp.Description,
				Status:       model.StatusBacklog,
				Priority:     sp.Priority,
				Labels:       toJSONArray(sp.Labels),
			}

			if err := db.Create(sub).Error; err != nil {
				return created, fmt.Errorf("create subtask %q: %w", sp.Title, err)
			}
			titleToID[sp.Title] = sub.ID
			created++
			slog.Info("created subtask", "title", sp.Title, "id", sub.ID, "parent", parent.ID)
		}
	}

	// Second pass: resolve dependencies now that all IDs are known.
	allParsed := make([]ParsedTask, 0, len(parsed)*2)
	for _, pt := range parsed {
		allParsed = append(allParsed, pt)
		allParsed = append(allParsed, pt.Subtasks...)
	}

	for _, pt := range allParsed {
		if len(pt.DependsOn) == 0 {
			continue
		}
		taskID, ok := titleToID[pt.Title]
		if !ok {
			continue // was skipped
		}

		var depIDs model.JSONArray
		for _, depTitle := range pt.DependsOn {
			depID, ok := titleToID[depTitle]
			if !ok {
				return created, fmt.Errorf("task %q depends on unknown task %q", pt.Title, depTitle)
			}
			depIDs = append(depIDs, depID.String())
		}

		if err := db.Model(&model.Task{}).Where("id = ?", taskID).
			Update("dependency_ids", depIDs).Error; err != nil {
			return created, fmt.Errorf("set dependencies for %q: %w", pt.Title, err)
		}
	}

	return created, nil
}

func toJSONArray(ss []string) model.JSONArray {
	if len(ss) == 0 {
		return nil
	}
	return model.JSONArray(ss)
}
