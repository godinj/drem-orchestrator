package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const TaskEventQuarantined = "task_event_quarantined"

// BeforeCreate keeps TaskEvent rows readable and correlated. Uncorrelated
// events are retained as diagnostics but quarantined out of task evidence paths.
func (e *TaskEvent) BeforeCreate(_ *gorm.DB) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	if strings.TrimSpace(e.Actor) == "" {
		e.Actor = "unknown"
	}
	if strings.TrimSpace(e.EventType) == "" {
		return fmt.Errorf("task event: event_type is required")
	}
	if err := ValidateTaskEventDetails(e.Details); err != nil {
		return err
	}
	if e.TaskID == uuid.Nil && e.EventType != TaskEventQuarantined {
		originalType := e.EventType
		originalDetails := e.Details
		e.EventType = TaskEventQuarantined
		e.Details = JSONField{
			"quarantined":         true,
			"diagnostic":          "zero_task_id",
			"original_event_type": originalType,
			"original_details":    originalDetails,
		}
	}
	return nil
}

func ValidateTaskEventDetails(details JSONField) error {
	if details == nil {
		return nil
	}
	if _, err := json.Marshal(details); err != nil {
		return fmt.Errorf("task event details must be valid JSON: %w", err)
	}
	return nil
}
