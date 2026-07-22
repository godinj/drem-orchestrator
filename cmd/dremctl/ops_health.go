package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/godinj/drem-orchestrator/pkg/orchclient"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

func handleHealth(ctx context.Context, client *orchclient.Client, cfg cliConfig, args []string, stdout io.Writer) error {
	if len(args) != 1 || args[0] != "issues" {
		return errors.New("usage: dremctl health issues")
	}
	issues, err := client.HealthIssues(ctx, cfg.project)
	if err != nil {
		return err
	}
	return renderHealthIssues(stdout, cfg.json, issues)
}

func handleRecover(ctx context.Context, client *orchclient.Client, cfg cliConfig, args []string, stdout io.Writer) error {
	const usage = "usage: dremctl recover <stale-assignment|exited-container|duplicate-active-attempts|contaminated-branch|stuck-parent-phase|accepted-child-work-adoption> <task-id-prefix> (--dry-run|--apply)"
	if len(args) == 0 {
		return errors.New(usage)
	}
	action := args[0]
	dryRun, apply, rest, err := parseRecoverMode(args[1:])
	if err != nil {
		return err
	}
	if len(rest) != 1 || (dryRun == apply) {
		return errors.New(usage)
	}
	if apply && strings.TrimSpace(cfg.actor) == "" {
		return errors.New("--actor or DREM_ACTOR is required for recovery apply")
	}
	taskID, err := resolveTaskUUID(ctx, client, cfg.project, rest[0])
	if err != nil {
		return err
	}
	if action != "stale-assignment" {
		result, err := client.RecoverTask(ctx, cfg.project, taskID, recoverAPIAction(action), orchdto.TaskRecoveryRequest{
			DryRun: dryRun,
			Apply:  apply,
			Actor:  cfg.actor,
		})
		if err != nil {
			return err
		}
		return renderTaskRecovery(stdout, cfg.json, result)
	}
	result, err := client.RecoverStaleAssignment(ctx, cfg.project, taskID, orchdto.StaleAssignmentRecoveryRequest{
		DryRun: dryRun,
		Apply:  apply,
		Actor:  cfg.actor,
	})
	if err != nil {
		return err
	}
	return renderStaleAssignmentRecovery(stdout, cfg.json, result)
}

func recoverAPIAction(action string) string {
	switch action {
	case "exited-container":
		return "exited-container-with-fresh-heartbeat"
	case "contaminated-branch":
		return "contaminated-branch-fail-gate"
	default:
		return action
	}
}

func parseRecoverMode(args []string) (dryRun bool, apply bool, rest []string, err error) {
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			dryRun = true
		case "--apply":
			apply = true
		default:
			if strings.HasPrefix(arg, "-") {
				return false, false, nil, fmt.Errorf("unknown flag %s", arg)
			}
			rest = append(rest, arg)
		}
	}
	return dryRun, apply, rest, nil
}

func renderHealthIssues(w io.Writer, jsonMode bool, issues []orchdto.HealthIssueDTO) error {
	if jsonMode {
		return writeJSON(w, issues)
	}
	if len(issues) == 0 {
		_, err := fmt.Fprintln(w, "no health issues detected")
		return err
	}
	sort.SliceStable(issues, func(i, j int) bool {
		return healthIssueRank(issues[i].Type) < healthIssueRank(issues[j].Type)
	})
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TYPE\tSEVERITY\tTASK\tWORKER\tROLE\tBRANCH\tATTEMPTS\tSTATUS\tAGE\tMESSAGE\tRECOMMENDED_ACTION")
	for _, issue := range issues {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			issue.Type,
			issue.Severity,
			shortIDOrDash(issue.TaskID),
			shortIDOrDash(issue.WorkerID),
			dash(issue.Role),
			dash(issue.Branch),
			formatAttemptIDs(issue.AttemptIDs),
			dash(issue.Status),
			formatIssueAge(issue.AgeSeconds),
			singleLine(formatHealthMessage(issue)),
			dash(issue.RecommendedAction),
		)
	}
	return tw.Flush()
}

func formatAttemptIDs(ids []string) string {
	if len(ids) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, shortIDOrDash(id))
	}
	return strings.Join(parts, ",")
}

func formatHealthMessage(issue orchdto.HealthIssueDTO) string {
	msg := issue.Message
	if issue.GateFailure != nil {
		msg = firstNonEmptyCLI(msg, issue.GateFailure.Message)
		if issue.GateFailure.Reason != "" {
			msg += " gate_reason=" + issue.GateFailure.Reason
		}
	}
	if len(issue.BlockedDependencies) > 0 {
		parts := make([]string, 0, len(issue.BlockedDependencies))
		for _, dep := range issue.BlockedDependencies {
			parts = append(parts, formatBlockedDependency(dep))
		}
		msg = strings.TrimSpace(msg + " blockers=" + strings.Join(parts, "; "))
	}
	return msg
}

func formatBlockedDependency(dep orchdto.BlockedDependencyDTO) string {
	if dep.DependencyID == "" && dep.TaskID == "" {
		return dep.Message
	}
	parts := []string{}
	if dep.TaskID != "" {
		parts = append(parts, "task="+shortIDOrDash(dep.TaskID))
	}
	if dep.DependencyID != "" {
		parts = append(parts, "dep="+shortIDOrDash(dep.DependencyID))
	}
	if dep.Status != "" {
		parts = append(parts, "status="+dep.Status)
	}
	return strings.Join(parts, ",")
}

func firstNonEmptyCLI(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func healthIssueRank(issueType string) int {
	switch issueType {
	case "missing_failure_evidence":
		return 2
	default:
		return 0
	}
}

func renderStaleAssignmentRecovery(w io.Writer, jsonMode bool, result orchdto.StaleAssignmentRecoveryDTO) error {
	if jsonMode {
		return writeJSON(w, result)
	}
	mode := "dry-run"
	if result.Applied {
		mode = "applied"
	}
	_, err := fmt.Fprintf(w, "%s: task %s %s safe=%t worker=%s status=%s: %s\n",
		mode,
		shortID(result.TaskID),
		result.Classification,
		result.Safe,
		dash(result.AssignedWorker),
		dash(result.WorkerStatus),
		result.Message,
	)
	return err
}

func renderTaskRecovery(w io.Writer, jsonMode bool, result orchdto.TaskRecoveryDTO) error {
	if jsonMode {
		return writeJSON(w, result)
	}
	mode := "dry-run"
	if result.Applied {
		mode = "applied"
	}
	if !result.Safe {
		mode = "refused"
	}
	_, err := fmt.Fprintf(w, "%s: task %s action=%s safe=%t policy=%s evidence=%s result=%s: %s\n",
		mode,
		shortID(result.TaskID),
		result.Action,
		result.Safe,
		dash(result.Policy),
		dash(result.Evidence),
		dash(result.Result),
		result.Message,
	)
	return err
}

func formatIssueAge(seconds int64) string {
	if seconds <= 0 {
		return "-"
	}
	mins := seconds / 60
	if mins < 60 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dh%dm", mins/60, mins%60)
}

func shortIDOrDash(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "-"
	}
	return shortID(id)
}
