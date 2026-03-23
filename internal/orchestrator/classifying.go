package orchestrator

import "github.com/godinj/drem-orchestrator/internal/model"

// ClassifierOutput represents the structured JSON output produced by a
// classifier agent after exploring the codebase. It contains either a
// successful classification or a clarification request.
type ClassifierOutput struct {
	// Successful classification fields
	Category       string   `json:"category"`
	ComplexityScore int      `json:"complexity_score"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	TargetFiles    []string `json:"target_files"`
	Rationale      string   `json:"rationale"`

	// Clarification request fields
	NeedsClarification bool     `json:"needs_clarification"`
	Questions          []string `json:"questions"`
}

// processClassifyingTasks finds tasks in CLASSIFYING with no assigned agent
// and spawns a classifier agent for each.
func (o *Orchestrator) processClassifyingTasks() {}

// onClassifierCompleted handles a classifier agent that finished successfully.
// It reads the classification.json from the agent's worktree and transitions
// the task based on the output.
func (o *Orchestrator) onClassifierCompleted(ag *model.Agent, task *model.Task) error {
	return nil
}

// onClassifierFailed handles a classifier agent that encountered an error.
// The task stays in CLASSIFYING and is parked for human triage.
func (o *Orchestrator) onClassifierFailed(ag *model.Agent, task *model.Task) error {
	return nil
}
