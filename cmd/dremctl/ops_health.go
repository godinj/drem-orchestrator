package main

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	const usage = "usage: dremctl recover stale-assignment <task-id-prefix> (--dry-run|--apply)"
	if len(args) == 0 || args[0] != "stale-assignment" {
		return errors.New(usage)
	}
	dryRun, apply, rest, err := parseRecoverMode(args[1:])
	if err != nil {
		return err
	}
	if len(rest) != 1 || (dryRun == apply) {
		return errors.New(usage)
	}
	taskID, err := resolveTaskUUID(ctx, client, cfg.project, rest[0])
	if err != nil {
		return err
	}
	result, err := client.RecoverStaleAssignment(ctx, cfg.project, taskID, orchdto.StaleAssignmentRecoveryRequest{
		DryRun: dryRun,
		Apply:  apply,
		Actor:  "dremctl",
	})
	if err != nil {
		return err
	}
	return renderStaleAssignmentRecovery(stdout, cfg.json, result)
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
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TYPE\tSEVERITY\tTASK\tWORKER\tSTATUS\tAGE\tMESSAGE")
	for _, issue := range issues {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			issue.Type,
			issue.Severity,
			shortIDOrDash(issue.TaskID),
			shortIDOrDash(issue.WorkerID),
			dash(issue.Status),
			formatIssueAge(issue.AgeSeconds),
			singleLine(issue.Message),
		)
	}
	return tw.Flush()
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
