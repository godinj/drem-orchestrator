package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/godinj/drem-orchestrator/pkg/orchclient"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

func handleTaskReport(ctx context.Context, client *orchclient.Client, cfg cliConfig, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: dremctl report <task-id-prefix>")
	}
	taskID, err := resolveTaskUUID(ctx, client, cfg.project, args[0])
	if err != nil {
		return err
	}
	report, err := client.TaskReport(ctx, cfg.project, taskID)
	if err != nil {
		return err
	}
	return renderTaskReport(stdout, cfg.json, report)
}

func renderTaskReport(w io.Writer, jsonMode bool, report orchdto.TaskReportDTO) error {
	if jsonMode {
		return writeJSON(w, report)
	}
	fmt.Fprintf(w, "# Drem task report: %s\n\n", report.Task.Title)
	fmt.Fprintf(w, "- Task: `%s`\n", report.Task.ID)
	fmt.Fprintf(w, "- Status: `%s`\n", report.Task.Status)
	fmt.Fprintf(w, "- Wall time: %s\n", reportDuration(report.WallDurationMS))
	fmt.Fprintf(w, "- SGLang tokens: %d input / %d output\n", report.Totals.TokensIn, report.Totals.TokensOut)
	if report.MeasurementCoverage.ExternalCodexMeasured {
		fmt.Fprintf(w, "- Codex goal usage: %d tokens / %s across %d goal(s)\n",
			report.Totals.CodexTokensUsed, reportDuration(report.Totals.CodexElapsedMS), report.Totals.CodexGoalCount)
	} else {
		fmt.Fprintln(w, "- Codex goal usage: unmeasured")
	}
	fmt.Fprintf(w, "- Measurement coverage: %.2f%% (%d/%d inference runs)\n\n",
		report.MeasurementCoverage.Percent,
		report.MeasurementCoverage.MeasuredInferenceRuns,
		report.MeasurementCoverage.EligibleInferenceRuns)

	fmt.Fprintln(w, "| Phase | Time | Visits | Inference runs | Input tokens | Output tokens |")
	fmt.Fprintln(w, "| --- | ---: | ---: | ---: | ---: | ---: |")
	for _, phase := range report.Phases {
		fmt.Fprintf(w, "| %s | %s | %d | %d | %d | %d |\n",
			phase.Name, reportDuration(phase.DurationMS), phase.Visits,
			phase.InferenceRuns, phase.TokensIn, phase.TokensOut)
	}

	fmt.Fprintln(w, "\n## Delivery evidence")
	fmt.Fprintf(w, "\n- Child tasks: %d\n", len(report.Children))
	fmt.Fprintf(w, "- Worker attempts: %d (%d completed, %d failed, %d aborted)\n",
		report.Totals.WorkerAttempts, report.Totals.CompletedAttempts,
		report.Totals.FailedAttempts, report.Totals.AbortedAttempts)
	fmt.Fprintf(w, "- Artifact versions: %d\n", report.Totals.ArtifactVersions)
	fmt.Fprintf(w, "- Native verification runs: %d\n", report.Totals.VerificationRuns)
	fmt.Fprintf(w, "- Computer Use runs: %d\n", report.Totals.ComputerUseRuns)
	fmt.Fprintf(w, "- Host rework: %d sessions / %d submissions\n",
		report.Totals.HostReworkSessions, report.Totals.HostReworkSubmissions)
	if len(report.CodexGoals) > 0 {
		fmt.Fprintln(w, "\n## Codex goal usage")
		for _, usage := range report.CodexGoals {
			fmt.Fprintf(w, "\n- `%s`: %d tokens / %s (`%s`, thread `%s`)\n",
				usage.GoalObjective, usage.TokensUsed, reportDuration(usage.ElapsedMS), usage.GoalStatus, usage.ThreadID)
		}
	}
	if len(report.Warnings) > 0 {
		fmt.Fprintln(w, "\n## Measurement warnings")
		for _, warning := range report.Warnings {
			fmt.Fprintf(w, "\n- %s", warning)
		}
		fmt.Fprintln(w)
	}
	return nil
}

func reportDuration(milliseconds int64) string {
	if milliseconds < 1000 {
		return fmt.Sprintf("%dms", milliseconds)
	}
	return (time.Duration(milliseconds) * time.Millisecond).Round(time.Second).String()
}
