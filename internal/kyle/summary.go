package kyle

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// RenderSummary returns a compact text digest of the supplied snapshots.
// The format is stable and suitable for "drem kyle summary" or for
// pasting into a chat channel. See internal/kyle/testdata/summary_basic.txt
// for the reference output used by summary_test.go.
//
// Format per project:
//
//	<name>:
//	  workers:    <N running> (breakdown), <N failed>
//	  tasks:      <N in-flight> (breakdown)
//	  recent:     <N commits>, <N merge-success>, <N crashes>
//	  health:     OK | STALE | DEGRADED
func RenderSummary(now time.Time, snapshots []ProjectSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "== Drem World State @ %s ==\n\n", now.UTC().Format(time.RFC3339))
	if len(snapshots) == 0 {
		b.WriteString("(no registered projects)\n")
		return b.String()
	}
	for _, snap := range snapshots {
		renderOne(&b, now, snap)
		b.WriteString("\n")
	}
	return b.String()
}

// renderOne writes the per-project block to b. Extracted so tests can
// render a single project without the surrounding header.
func renderOne(b *strings.Builder, now time.Time, snap ProjectSnapshot) {
	fmt.Fprintf(b, "%s:\n", snap.Project)
	fmt.Fprintf(b, "  workers:    %s\n", workerLine(snap.Workers))
	fmt.Fprintf(b, "  tasks:      %s\n", taskLine(snap.Tasks))
	if line := taskAttentionLine(now, snap.Tasks); line != "" {
		fmt.Fprintf(b, "  attention:  %s\n", line)
	}
	fmt.Fprintf(b, "  recent:     %s\n", eventLine(snap.RecentEvents))
	fmt.Fprintf(b, "  health:     %s\n", healthLine(snap))
}

// workerLine renders a compact summary of worker status counts. Running
// workers are grouped by agent type; failed workers are counted in a
// separate tail so operators can see failures without scanning a long
// per-type breakdown.
func workerLine(workers []orchdto.WorkerDTO) string {
	if len(workers) == 0 {
		return "0 running"
	}
	running := 0
	failed := 0
	byType := map[string]int{}
	for _, w := range workers {
		switch w.Status {
		case "failed", "exited_error":
			failed++
		case "working", "running", "starting":
			running++
			byType[w.AgentType]++
		}
	}
	parts := make([]string, 0, len(byType))
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		parts = append(parts, fmt.Sprintf("%d %s", byType[t], t))
	}
	breakdown := ""
	if len(parts) > 0 {
		breakdown = " (" + strings.Join(parts, ", ") + ")"
	}
	return fmt.Sprintf("%d running%s, %d failed", running, breakdown, failed)
}

// taskLine renders a count of in-flight tasks and a status breakdown.
// "In-flight" means anything that is not a terminal state (merged,
// cancelled, failed). The status order is alphabetical so the output is
// deterministic across runs.
func taskLine(tasks []orchdto.TaskDTO) string {
	if len(tasks) == 0 {
		return "0 in-flight"
	}
	terminal := map[string]bool{
		"done":      true,
		"merged":    true,
		"cancelled": true,
		"failed":    true,
		"abandoned": true,
	}
	inflight := 0
	byStatus := map[string]int{}
	for _, t := range tasks {
		if terminal[t.Status] {
			continue
		}
		inflight++
		byStatus[t.Status]++
	}
	statuses := make([]string, 0, len(byStatus))
	for s := range byStatus {
		statuses = append(statuses, s)
	}
	sort.Strings(statuses)
	parts := make([]string, 0, len(statuses))
	for _, s := range statuses {
		parts = append(parts, fmt.Sprintf("%d %s", byStatus[s], s))
	}
	breakdown := ""
	if len(parts) > 0 {
		breakdown = " (" + strings.Join(parts, ", ") + ")"
	}
	return fmt.Sprintf("%d in-flight%s", inflight, breakdown)
}

const staleActiveTaskAfter = 4 * time.Hour

func taskAttentionLine(now time.Time, tasks []orchdto.TaskDTO) string {
	items := make([]string, 0, len(tasks))
	for _, t := range tasks {
		if strings.TrimSpace(t.LatestFailureSummary) == "" {
			continue
		}
		kind := taskAttentionKind(t)
		if kind == "" {
			continue
		}
		label := firstNonEmpty(t.Title, t.ID, "task")
		items = append(items, fmt.Sprintf("%s %s: %s", kind, label, oneLine(t.LatestFailureSummary)))
	}
	for _, t := range staleActiveTasks(now, tasks) {
		label := firstNonEmpty(t.Title, t.ID, "task")
		items = append(items, fmt.Sprintf("stuck %s: %s for %s", label, t.Status, compactAge(now.Sub(t.UpdatedAt))))
	}
	if len(items) == 0 {
		return ""
	}
	if len(items) > 3 {
		items = append(items[:3], fmt.Sprintf("+%d more", len(items)-3))
	}
	return strings.Join(items, "; ")
}

func staleActiveTasks(now time.Time, tasks []orchdto.TaskDTO) []orchdto.TaskDTO {
	stale := make([]orchdto.TaskDTO, 0, len(tasks))
	for _, t := range tasks {
		if t.UpdatedAt.IsZero() || now.Before(t.UpdatedAt) {
			continue
		}
		if now.Sub(t.UpdatedAt) < staleActiveTaskAfter {
			continue
		}
		switch t.Status {
		case "planning", "test_writing", "in_progress", "testing_ready", "merging", "classifying":
			stale = append(stale, t)
		}
	}
	return stale
}

func compactAge(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	if d < 48*time.Hour {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

func taskAttentionKind(t orchdto.TaskDTO) string {
	switch t.CurrentHealth {
	case "stuck":
		return "stuck"
	case "needs_attention":
		return "needs-attention"
	case "failed", "unhealthy", "degraded":
		return "failure"
	}
	if t.LatestFailureCurrent != nil && !*t.LatestFailureCurrent {
		return ""
	}
	switch t.Status {
	case "failed", "rejected":
		return "failure"
	case "paused", "needs_clarification", "plan_review", "test_review", "testing_ready":
		return "needs-attention"
	}
	return ""
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= 120 {
		return s
	}
	return s[:117] + "..."
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// eventLine counts notable event types from the recent-events slice. The
// categories are fixed so the digest is comparable across projects even
// when one project has exotic event types Kyle does not recognise.
func eventLine(events []orchdto.EventDTO) string {
	commits := 0
	mergeSuccess := 0
	crashes := 0
	for _, ev := range events {
		switch ev.Type {
		case "commit", "worker_commit":
			commits++
		case "merge_success", "merge_completed":
			mergeSuccess++
		case "agent_crash", "worker_exit_error", "merge_failed":
			crashes++
		}
	}
	return fmt.Sprintf("%d commits, %d merge-success, %d crashes", commits, mergeSuccess, crashes)
}

// healthLine collapses the snapshot's liveness signals into a single
// one-word verdict. STALE beats DEGRADED beats OK: if both conditions
// hold, staleness wins because a stale snapshot cannot reliably be judged
// degraded.
func healthLine(snap ProjectSnapshot) string {
	if snap.Stale {
		return "STALE"
	}
	if snap.LastError != "" {
		return "DEGRADED"
	}
	return "OK"
}
