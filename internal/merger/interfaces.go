// Package merger implements the container-local merge workflow used by the
// drem-merger binary: clone the integration branch into a fresh workspace,
// merge a feature branch, run the project test command, push the integration
// branch back to the bare repository, and delete the feature branch.
//
// Unlike internal/merge/, which assumes host-side worktrees, every step in
// this package runs against a single container-local clone. The bare
// repository is assumed to live on a host bind mount. No host-side worktrees
// are ever created.
package merger

import "context"

// GitrefRegistry is the narrow surface the merger needs for branch-ref
// bookkeeping. It is expressed in terms of (bareRepo, branch) so that the
// merger does not need to understand BranchRef IDs. cmd/drem-merger/main.go
// adapts a *gitref.Registry to this interface by resolving the ID via
// FindByBranch before calling MarkMerged / MarkDeleted by ID.
//
// Implementations must be safe for concurrent use across a single merge.
type GitrefRegistry interface {
	// MarkMerged transitions the ref for (bareRepo, branch) to "merged".
	// Callers should invoke this only after integration has been pushed.
	MarkMerged(ctx context.Context, bareRepo, branch string) error

	// MarkDeleted transitions the ref for (bareRepo, branch) to "deleted".
	// Callers should invoke this only after the remote branch has actually
	// been removed from the bare repository.
	MarkDeleted(ctx context.Context, bareRepo, branch string) error
}

// MergeReporter is the outbound reporting surface. Production code posts
// merge_result records to the orchestrator's /internal/logs endpoint; tests
// use a fake to assert the Reporter is always invoked, even on failure.
type MergeReporter interface {
	// Report emits a single merge_result record. The method must be called
	// exactly once per Merger.Merge invocation, regardless of success.
	Report(ctx context.Context, project, taskID string, result MergeResult) error
}

// NoopReporter is a MergeReporter that discards all records. It exists for
// tests and for the fallback path when no orchestrator URL is configured.
type NoopReporter struct{}

// Report satisfies MergeReporter by doing nothing.
func (NoopReporter) Report(context.Context, string, string, MergeResult) error {
	return nil
}

// NoopRegistry is a GitrefRegistry that records nothing. It is used by tests
// that do not care about branch-ref bookkeeping and as a safe default when
// the real registry is unavailable.
type NoopRegistry struct{}

// MarkMerged satisfies GitrefRegistry by doing nothing.
func (NoopRegistry) MarkMerged(context.Context, string, string) error { return nil }

// MarkDeleted satisfies GitrefRegistry by doing nothing.
func (NoopRegistry) MarkDeleted(context.Context, string, string) error { return nil }
