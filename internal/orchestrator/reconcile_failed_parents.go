package orchestrator

import "github.com/godinj/drem-orchestrator/internal/model"

// reconcileFailedParents finds failed parent tasks whose subtasks have all
// completed successfully (status done) and recovers them. The parent is
// transitioned from failed → in_progress via the state machine, then
// checkFeatureCompletion is called to evaluate quality gates and advance
// to the appropriate next status.
//
// This covers the bug where a parent task fails (e.g., due to a subtask merge
// conflict) but all subtasks eventually succeed via retry. Without this check,
// such parents remain stuck in failed status indefinitely.
func (o *Orchestrator) reconcileFailedParents() (int, error) {
	return o.reconcileParentsByPolicy(parentReconcilePolicy{
		status:  model.StatusFailed,
		logVerb: "recovering",
		recover: recoverFailedParent,
	})
}
