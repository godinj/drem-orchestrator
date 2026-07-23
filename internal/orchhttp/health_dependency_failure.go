package orchhttp

import (
	"time"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

func dependencyFailureIssue(parent model.Task, children []model.Task, now time.Time) orchdto.HealthIssueDTO {
	if parent.Status != model.StatusInProgress || len(children) == 0 {
		return orchdto.HealthIssueDTO{}
	}
	failed := make([]string, 0)
	nonterminal := make([]string, 0)
	for _, child := range children {
		switch child.Status {
		case model.StatusFailed, model.StatusRejected:
			failed = append(failed, child.ID.String())
		case model.StatusDone, model.StatusCancelled:
		default:
			nonterminal = append(nonterminal, child.ID.String())
		}
	}
	if len(failed) == 0 || len(nonterminal) == 0 {
		return orchdto.HealthIssueDTO{}
	}
	return orchdto.HealthIssueDTO{
		Type: dependencyFailureStall, Severity: "critical", TaskID: parent.ID.String(),
		Status: string(parent.Status), DetectedAt: now,
		BlockedDependencies: []orchdto.BlockedDependencyDTO{{
			TaskID: nonterminal[0], DependencyID: failed[0],
			Message: "required child failed while dependent work remains nonterminal",
		}},
		Message: "active parent has a failed required child and dependency-blocked descendants; reconciliation must terminalize the plan",
	}
}

func attemptIDs(attempts []model.WorkerAttempt) []string {
	ids := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		ids = append(ids, attempt.ID.String())
	}
	return ids
}

func attemptWorkerID(attempt model.WorkerAttempt) string {
	if attempt.WorkerID != "" {
		return attempt.WorkerID
	}
	if attempt.AgentID != nil {
		return attempt.AgentID.String()
	}
	return ""
}
