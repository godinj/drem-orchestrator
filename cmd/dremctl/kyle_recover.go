package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/pkg/orchclient"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

const (
	recoveryActor        = "kyle"
	recoveryAuditType    = "kyle_recovery_audit"
	retryPolicyRule      = "testing_ready.infra_tooling.one_retry_max"
	remediationPolicy    = "testing_ready.real_code_failure.remediation_required"
	delegatePolicy       = "testing_ready.uncertain.delegate_investigation"
	escalationPolicy     = "testing_ready.escalation_only.no_autonomous_action"
	breakGlassPolicy     = "testing_ready.supported_surface_unavailable.break_glass_self_confirmed"
	surfaceTestingFail   = "POST /projects/{name}/tasks/{id}/fail"
	recoveryRetryAction  = "retry testing_ready via fail endpoint"
	unsupportedRemediate = "file focused remediation/fixer work"
	unsupportedDelegate  = "delegate investigation to Mike or Seth"
	escalateAction       = "escalate to operator"
	breakGlassAction     = "record break-glass decision; unsupported DB/Docker/host execution disabled"
)

type recoveryDecision struct {
	TaskID         string `json:"task_id"`
	State          string `json:"state"`
	Evidence       string `json:"evidence"`
	Classification string `json:"classification"`
	PolicyRule     string `json:"policy_rule"`
	Action         string `json:"proposed_action"`
	Path           string `json:"path"`
	Supported      bool   `json:"supported"`
	BreakGlass     bool   `json:"break_glass"`
	SelfConfirmed  bool   `json:"self_confirmed"`
	Mission        string `json:"mission,omitempty"`
	Allowed        bool   `json:"allowed"`
	Reason         string `json:"reason,omitempty"`
}

func handleKyle(ctx context.Context, client *orchclient.Client, cfg cliConfig, args []string, stdout io.Writer) error {
	const usage = "usage: dremctl kyle recover [--mission-file PATH] (--dry-run|--apply)"
	if len(args) == 0 || args[0] != "recover" {
		return errors.New(usage)
	}
	fs := newFlagSet("kyle recover")
	dryRun := fs.Bool("dry-run", false, "print recovery decisions without mutating")
	apply := fs.Bool("apply", false, "execute the first allowlisted supported recovery action")
	missionFile := fs.String("mission-file", defaultKyleMissionFile(), "Kyle mission file; missing file is treated as no active mission")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || (*dryRun == *apply) {
		return errors.New(usage)
	}

	mission, err := readKyleMission(*missionFile)
	if err != nil {
		return err
	}
	decisions, err := buildRecoveryDecisions(ctx, client, cfg.project, mission)
	if err != nil {
		return err
	}
	if cfg.json {
		if *apply {
			result, err := applyFirstRecovery(ctx, client, cfg.project, decisions)
			if err != nil {
				return err
			}
			return writeJSON(stdout, result)
		}
		return writeJSON(stdout, decisions)
	}
	if err := renderRecoveryDecisions(stdout, decisions); err != nil {
		return err
	}
	if *apply {
		result, err := applyFirstRecovery(ctx, client, cfg.project, decisions)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "\napply: %s\n", result.Message)
	}
	return nil
}

func buildRecoveryDecisions(ctx context.Context, client *orchclient.Client, project string, mission string) ([]recoveryDecision, error) {
	tasks, err := client.ListTasks(ctx, project, orchclient.TaskFilter{Status: "testing_ready", Limit: taskPageLimit})
	if err != nil {
		return nil, err
	}
	events, err := client.Events(ctx, zeroTime(), taskPageLimit)
	if err != nil {
		return nil, err
	}
	// /events is currently the only supported event read surface. It has no
	// task-scoped filter, so retry budgets use this bounded recent window and
	// correlate by task_id inside each event payload.
	auditCounts := recoveryAuditCounts(events)
	decisions := make([]recoveryDecision, 0, len(tasks))
	for _, task := range tasks {
		decision := classifyRecoveryTask(task, mission)
		if decision.PolicyRule == retryPolicyRule && auditCounts[task.ID] >= 1 {
			decision.Allowed = false
			decision.Reason = "retry budget exhausted"
		}
		decisions = append(decisions, decision)
	}
	sort.Slice(decisions, func(i, j int) bool { return decisions[i].TaskID < decisions[j].TaskID })
	return decisions, nil
}

func classifyRecoveryTask(task orchdto.TaskDTO, mission string) recoveryDecision {
	evidence := recoveryEvidence(task)
	decision := recoveryDecision{TaskID: task.ID, State: task.Status, Evidence: evidence, Mission: mission, Allowed: true}
	lower := strings.ToLower(evidence + " " + task.LatestFailureType)
	switch {
	case containsAny(lower, "secret", "credential", "api_key", "token=", "force-push", "reset --hard", "git clean", "drem-sglang", "sglang", "docker compose up", "unclear product intent"):
		decision.Classification = "escalation_only"
		decision.PolicyRule = escalationPolicy
		decision.Action = escalateAction
		decision.Path = "operator"
		decision.Allowed = false
		decision.Reason = "escalation-only action class"
	case containsAny(lower, "timeout", "timed out", "connection reset", "network", "rate limit", "no space", "docker daemon", "infra", "infrastructure", "tooling", "flake", "flaky", "crash", "exit_reason", "build_error"):
		decision.Classification = "infra_tooling_gate_failure"
		decision.PolicyRule = retryPolicyRule
		decision.Action = recoveryRetryAction
		decision.Path = "supported"
		decision.Supported = true
	case containsAny(lower, "supported surface unavailable", "supported api unavailable", "unsupported surface required", "direct db", "task state repair", "assignment repair", "docker --no-deps", "host command"):
		decision.Classification = "supported_surface_unavailable"
		decision.PolicyRule = breakGlassPolicy
		decision.Action = breakGlassAction
		decision.Path = "break-glass"
		decision.BreakGlass = true
		decision.SelfConfirmed = strings.TrimSpace(mission) != ""
		decision.Allowed = false
		if decision.SelfConfirmed {
			decision.Reason = "self-confirmed by policy; unsupported DB/Docker/host execution is not implemented"
		} else {
			decision.Reason = "break-glass requires active mission; unsupported DB/Docker/host execution is not implemented"
		}
	case containsAny(lower, "test_failure", "assert", "expected", "failed", "compile", "undefined", "panic", "merge_failure"):
		decision.Classification = "real_code_or_test_failure"
		decision.PolicyRule = remediationPolicy
		decision.Action = unsupportedRemediate
		decision.Path = "supported endpoint missing"
		decision.Allowed = false
		decision.Reason = "remediation/fixer filing endpoint missing"
	default:
		decision.Classification = "uncertain_failure"
		decision.PolicyRule = delegatePolicy
		decision.Action = unsupportedDelegate
		decision.Path = "supported endpoint missing"
		decision.Allowed = false
		decision.Reason = "delegation endpoint missing"
	}
	return decision
}

func recoveryEvidence(task orchdto.TaskDTO) string {
	parts := []string{}
	if task.LatestFailureType != "" {
		parts = append(parts, "type="+task.LatestFailureType)
	}
	if task.LatestFailureSummary != "" {
		parts = append(parts, singleLine(task.LatestFailureSummary))
	}
	if len(parts) == 0 {
		return "no supported failure evidence"
	}
	return strings.Join(parts, "; ")
}

func recoveryAuditCounts(events []orchdto.EventDTO) map[string]int {
	counts := map[string]int{}
	for _, event := range events {
		if event.Type != recoveryAuditType {
			continue
		}
		var payload struct {
			TaskID  string `json:"task_id"`
			Details struct {
				PolicyRule string `json:"policy_rule"`
			} `json:"details"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			continue
		}
		if payload.TaskID != "" && payload.Details.PolicyRule == retryPolicyRule {
			counts[payload.TaskID]++
		}
	}
	return counts
}

func renderRecoveryDecisions(w io.Writer, decisions []recoveryDecision) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TASK ID\tSTATE\tMISSION\tEVIDENCE\tCLASSIFICATION\tPOLICY RULE\tPROPOSED ACTION\tSUPPORTED/BREAK-GLASS")
	for _, d := range decisions {
		path := d.Path
		if d.Supported {
			path = "supported"
		} else if d.BreakGlass {
			path = "break-glass"
		}
		if !d.Allowed && d.Reason != "" {
			path += ": " + d.Reason
		}
		mission := d.Mission
		if mission == "" {
			mission = "no active mission file"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", shortID(d.TaskID), d.State, singleLine(mission), d.Evidence, d.Classification, d.PolicyRule, d.Action, path)
	}
	return tw.Flush()
}

func defaultKyleMissionFile() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".drem-csuite", "kyle", "mission.md")
}

func readKyleMission(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read Kyle mission file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

type recoveryApplyResult struct {
	Applied  bool               `json:"applied"`
	TaskID   string             `json:"task_id,omitempty"`
	Action   string             `json:"action,omitempty"`
	Message  string             `json:"message"`
	Decision *recoveryDecision  `json:"decision,omitempty"`
	Gaps     []recoveryDecision `json:"gaps,omitempty"`
}

func applyFirstRecovery(ctx context.Context, client *orchclient.Client, project string, decisions []recoveryDecision) (recoveryApplyResult, error) {
	var gaps []recoveryDecision
	for _, decision := range decisions {
		if !decision.Supported || !decision.Allowed || decision.BreakGlass {
			if !decision.Supported || decision.BreakGlass {
				gaps = append(gaps, decision)
			}
			continue
		}
		if decision.Action != recoveryRetryAction {
			continue
		}
		id, err := uuid.Parse(decision.TaskID)
		if err != nil {
			return recoveryApplyResult{}, fmt.Errorf("parse task id %q: %w", decision.TaskID, err)
		}
		updated, err := client.Fail(ctx, project, id)
		if err != nil {
			return recoveryApplyResult{}, err
		}
		result := "task transitioned to " + updated.Status
		comment := recoveryComment(decision, result)
		if _, err := client.Comment(ctx, project, id, comment); err != nil {
			return recoveryApplyResult{}, err
		}
		_, err = client.AuditRecovery(ctx, project, id, orchdto.RecoveryAuditRequest{
			Actor:          recoveryActor,
			PolicyRule:     decision.PolicyRule,
			Evidence:       decision.Evidence,
			Surface:        surfaceTestingFail,
			Action:         decision.Action,
			Result:         result,
			NextFollowUp:   "orchestrator should reprocess in_progress task; escalate if blocker repeats",
			SupportedPath:  true,
			BreakGlassPath: false,
		})
		if err != nil {
			return recoveryApplyResult{}, err
		}
		return recoveryApplyResult{Applied: true, TaskID: decision.TaskID, Action: decision.Action, Message: result, Decision: &decision, Gaps: gaps}, nil
	}
	return recoveryApplyResult{Applied: false, Message: "no allowlisted supported recovery action available", Gaps: gaps}, nil
}

func recoveryComment(decision recoveryDecision, result string) string {
	return fmt.Sprintf("Kyle autonomous recovery applied. Policy: %s. Evidence: %s. Action: %s via supported surface %s. Result: %s. Next follow-up: escalate if blocker repeats.", decision.PolicyRule, decision.Evidence, decision.Action, surfaceTestingFail, result)
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func zeroTime() time.Time { return time.Time{} }
