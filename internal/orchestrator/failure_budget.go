package orchestrator

import (
	"fmt"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/model"
)

const (
	failureClassModelTruncation    = "model_truncation"
	failureClassToolLoop           = "tool_loop"
	failureClassInfraTimeout       = "infra_timeout"
	failureClassBranchPermission   = "branch_permission"
	failureClassBranchContam       = "branch_contamination"
	failureClassTestFailure        = "test_failure"
	failureClassToolFailure        = "tool_failure"
	failureClassInfraFailure       = "infra_failure"
	failureClassPlannerValidation  = "planner_validation"
	failureClassPlannerUnavailable = "planner_unavailable"
	failureClassInferenceBudget    = "inference_budget"

	retryBudgetsContextKey = "retry_budgets"
)

type retryBudgetState struct {
	Edge        string
	Class       string
	Attempts    int
	MaxRetries  int
	Exhausted   bool
	LastReason  string
	LastSummary string
	LastAt      time.Time
}

func consumeRetryBudget(task *model.Task, edge, class, summary string, now time.Time) retryBudgetState {
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	class = normalizeFailureClass(class, summary)
	state := loadRetryBudget(task, edge, class)
	state.Edge = edge
	state.Class = class
	state.MaxRetries = maxRetriesForFailureClass(class)
	state.Attempts++
	state.LastReason = class
	state.LastSummary = truncate(summary, maxErrorSnippetLen)
	state.LastAt = now
	state.Exhausted = state.Attempts > state.MaxRetries
	storeRetryBudget(task, state)
	recordLatestFailure(task, class, summary, now, state)
	return state
}

func loadRetryBudget(task *model.Task, edge, class string) retryBudgetState {
	key := retryBudgetKey(edge, class)
	budgets, _ := task.Context[retryBudgetsContextKey].(map[string]any)
	if budgets == nil {
		return retryBudgetState{}
	}
	raw, _ := budgets[key].(map[string]any)
	if raw == nil {
		return retryBudgetState{}
	}
	return retryBudgetState{
		Edge:        stringFromAny(raw["edge"]),
		Class:       stringFromAny(raw["class"]),
		Attempts:    intFromAny(raw["attempts"]),
		MaxRetries:  intFromAny(raw["max_retries"]),
		Exhausted:   boolFromAny(raw["exhausted"]),
		LastReason:  stringFromAny(raw["last_reason"]),
		LastSummary: stringFromAny(raw["last_summary"]),
		LastAt:      timeFromAny(raw["last_at"]),
	}
}

func storeRetryBudget(task *model.Task, state retryBudgetState) {
	budgets, _ := task.Context[retryBudgetsContextKey].(map[string]any)
	if budgets == nil {
		budgets = map[string]any{}
	}
	budgets[retryBudgetKey(state.Edge, state.Class)] = map[string]any{
		"edge":         state.Edge,
		"class":        state.Class,
		"attempts":     float64(state.Attempts),
		"max_retries":  float64(state.MaxRetries),
		"exhausted":    state.Exhausted,
		"last_reason":  state.LastReason,
		"last_summary": state.LastSummary,
		"last_at":      state.LastAt.Format(time.RFC3339Nano),
	}
	task.Context[retryBudgetsContextKey] = budgets
}

func retryBudgetKey(edge, class string) string {
	return strings.TrimSpace(edge) + "|" + strings.TrimSpace(class)
}

func maxRetriesForFailureClass(class string) int {
	switch class {
	case failureClassModelTruncation, failureClassToolLoop:
		return 1
	case failureClassBranchPermission, failureClassBranchContam, failureClassTestFailure, failureClassInferenceBudget:
		return 0
	case failureClassInfraTimeout, failureClassInfraFailure:
		return 3
	default:
		return 2
	}
}

func recordLatestFailure(task *model.Task, class, summary string, now time.Time, budget retryBudgetState) {
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["latest_failure_type"] = class
	task.Context["latest_failure_summary"] = truncate(summary, maxErrorSnippetLen)
	task.Context["latest_failure_at"] = now.Format(time.RFC3339Nano)
	task.Context["latest_failure_current"] = true
	task.Context["latest_failure_retry_edge"] = budget.Edge
	task.Context["latest_failure_retry_attempts"] = float64(budget.Attempts)
	task.Context["latest_failure_retry_max"] = float64(budget.MaxRetries)
	task.Context["latest_failure_retry_exhausted"] = budget.Exhausted
}

func (o *Orchestrator) failTaskWithFailureEvidence(task *model.Task, reason, class, summary string, now time.Time, budget retryBudgetState) error {
	recordLatestFailure(task, class, summary, now, budget)
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["failure_reason"] = reason
	task.Context["failure_class"] = class
	return o.failTask(task, reason)
}

func normalizeFailureClass(reason, evidence string) string {
	text := strings.ToLower(strings.TrimSpace(reason + " " + evidence))
	switch {
	case strings.Contains(text, "token_budget"), strings.Contains(text, "token budget"), strings.Contains(text, "no_progress"), strings.Contains(text, "no progress"):
		return failureClassInferenceBudget
	case strings.Contains(text, "model_truncation"), strings.Contains(text, "context_limit"), strings.Contains(text, "context limit"), strings.Contains(text, "truncat"):
		return failureClassModelTruncation
	case strings.Contains(text, "tool_loop"), strings.Contains(text, "tool loop"), strings.Contains(text, "too many tool"), strings.Contains(text, "tool use loop"):
		return failureClassToolLoop
	case strings.Contains(text, "infra_timeout"), strings.Contains(text, "timeout"), strings.Contains(text, "timed out"):
		return failureClassInfraTimeout
	case strings.Contains(text, "branch_permission"), strings.Contains(text, "permission denied"), strings.Contains(text, "not writable"):
		return failureClassBranchPermission
	case strings.Contains(text, "branch_contamination"), strings.Contains(text, "contamination"), strings.Contains(text, "worker trace"), strings.Contains(text, "prompt artifact"):
		return failureClassBranchContam
	case strings.Contains(text, "test_failure"), strings.Contains(text, "tests_failed"), strings.Contains(text, "test failed"), strings.Contains(text, "go test"), strings.Contains(text, "pytest"):
		return failureClassTestFailure
	case strings.Contains(text, "plan_validation_failed"):
		return failureClassPlannerValidation
	case strings.Contains(text, "planner_unhealthy"), strings.Contains(text, "upstream"), strings.Contains(text, "planner_http"):
		return failureClassPlannerUnavailable
	case strings.Contains(text, "oom"), strings.Contains(text, "killed"), strings.Contains(text, "terminated"):
		return failureClassInfraFailure
	case strings.Contains(text, "tool"), strings.Contains(text, "exit_nonzero"), strings.Contains(text, "command"):
		return failureClassToolFailure
	}
	return strings.TrimSpace(reason)
}

func retryEdgeForTask(task model.Task, role string) string {
	return fmt.Sprintf("%s:%s", task.Status, role)
}

func stringFromAny(v any) string {
	s, _ := v.(string)
	return s
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func boolFromAny(v any) bool {
	b, _ := v.(bool)
	return b
}

func timeFromAny(v any) time.Time {
	s, _ := v.(string)
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
