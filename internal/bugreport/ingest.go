package bugreport

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// BugReportFile is the JSON schema agents write to .drem/bug-reports/<uuid>.json.
type BugReportFile struct {
	Title               string `json:"title"`
	Description         string `json:"description"`
	Category            string `json:"category"`
	Severity            string `json:"severity"`
	ReproductionContext string `json:"reproduction_context"`
	AgentID             string `json:"agent_id"`
	TaskID              string `json:"task_id"`
}

// Ingest scans dir for .json files, parses and validates them, inserts valid
// reports into the database (associating with projectID), and cleans up.
// Invalid files are moved to dir/failed/. Returns the number of reports ingested.
func (s *Service) Ingest(dir string, projectID uuid.UUID) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("read bug report directory %s: %w", dir, err)
	}

	ingested := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		filePath := filepath.Join(dir, entry.Name())
		if err := s.ingestFile(filePath, projectID); err != nil {
			log.Printf("bugreport: failed to ingest %s: %v", filePath, err)
			if moveErr := moveToFailed(filePath, dir); moveErr != nil {
				log.Printf("bugreport: failed to move %s to failed/: %v", filePath, moveErr)
			}
			continue
		}
		ingested++
	}
	return ingested, nil
}

// ingestFile parses, validates, and inserts a single bug report JSON file,
// then removes the file on success.
func (s *Service) ingestFile(filePath string, projectID uuid.UUID) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	var bf BugReportFile
	if err := json.Unmarshal(data, &bf); err != nil {
		return fmt.Errorf("parse JSON: %w", err)
	}

	// Validate required fields.
	if bf.Title == "" {
		return fmt.Errorf("title is required")
	}
	if bf.Description == "" {
		return fmt.Errorf("description is required")
	}

	// Parse and validate category.
	category, err := model.ParseBugReportCategory(bf.Category)
	if err != nil {
		return fmt.Errorf("invalid category: %w", err)
	}

	// Parse and validate severity.
	severity, err := model.ParseBugReportSeverity(bf.Severity)
	if err != nil {
		return fmt.Errorf("invalid severity: %w", err)
	}

	// Parse optional AgentID.
	var agentID *uuid.UUID
	if bf.AgentID != "" {
		id, err := uuid.Parse(bf.AgentID)
		if err != nil {
			return fmt.Errorf("invalid agent_id %q: %w", bf.AgentID, err)
		}
		agentID = &id
	}

	// Parse optional TaskID.
	var taskID *uuid.UUID
	if bf.TaskID != "" {
		id, err := uuid.Parse(bf.TaskID)
		if err != nil {
			return fmt.Errorf("invalid task_id %q: %w", bf.TaskID, err)
		}
		taskID = &id
	}

	report := &model.BugReport{
		Title:               bf.Title,
		Description:         bf.Description,
		Category:            category,
		Severity:            severity,
		ReproductionContext: bf.ReproductionContext,
		AgentID:             agentID,
		TaskID:              taskID,
		ProjectID:           projectID,
	}

	if err := s.Create(report); err != nil {
		return fmt.Errorf("create bug report: %w", err)
	}

	// Remove the file after successful ingestion.
	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("remove ingested file: %w", err)
	}

	return nil
}

// moveToFailed moves a file to the dir/failed/ subdirectory.
func moveToFailed(filePath, dir string) error {
	failedDir := filepath.Join(dir, "failed")
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		return fmt.Errorf("create failed directory: %w", err)
	}
	dest := filepath.Join(failedDir, filepath.Base(filePath))
	if err := os.Rename(filePath, dest); err != nil {
		return fmt.Errorf("move to failed: %w", err)
	}
	return nil
}
