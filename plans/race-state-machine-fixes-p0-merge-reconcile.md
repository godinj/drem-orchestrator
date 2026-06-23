# P0 Fix Plan: Merge Attempt Scoping And Stale Worktree-Safe Reconciliation

Findings: `RACE-SM-004`, `RACE-SM-005`.

## Summary

Fix two P0 corruption paths:

- Scope `merge_result` side effects to the current merger attempt/container so stale output cannot overwrite task context.
- Remove host-worktree auto-commit from orphan reconciliation; reconciler must fail closed when feature worktree HEAD is stale or dirty.

## Current Evidence

### Merge result scoping

- `internal/orchhttp/handlers_internal.go`
  - `applyIngestSideEffects` handles `merge_result` by task ID and event type only.
  - It writes generic context keys such as `merge_commit` and `merge_conflicts`.
- `internal/orchestrator/merge_dispatch.go`
  - `dispatchMerge` reloads task context and consumes generic merge context keys.

### Stale worktree reconciliation

- `internal/orchestrator/reconcile.go`
  - Orphan recovery calls `gitexec.CommitUnstagedChanges` in the host feature worktree.
- `internal/gitexec/gitexec.go`
  - `CommitUnstagedChanges` runs `git add -u` and commits.
- `plans/reconciler-stale-worktree-fix.md`
  - Documents corruption sequence where worker push advances bare ref while host worktree remains stale; reconciler auto-commit creates a revert commit.

## Proposed Changes

## RACE-SM-004: Current merger attempt scoping

### Record current attempt metadata when merge dispatch starts

In `internal/orchestrator/merge_dispatch.go`, immediately after merger `RecordSpawn` succeeds:

```go
task.Context["current_merge_attempt_id"] = handle.AttemptID.String()
task.Context["current_merge_container_id"] = res.ContainerID
task.Context["current_merge_worker_id"] = workerID
delete(task.Context, "merge_commit")
delete(task.Context, "merge_conflicts")
delete(task.Context, "merge_failure_reason")
delete(task.Context, "merge_test_output")
delete(task.Context, "merge_result_attempt_id")
delete(task.Context, "merge_result_container_id")
if err := o.db.Model(task).Update("context", task.Context).Error; err != nil { ... }
```

For mergers, fail closed if `RecordSpawn` fails; current-attempt scoping depends on durable attempt identity.

### Guard ingest side effects

In `internal/orchhttp/handlers_internal.go`, add helper like:

```go
func currentMergerAttemptMatches(tx *gorm.DB, task model.Task, row model.TaskEvent) (bool, string, error) {
    containerID := stringField(row.Details, "container_id")
    workerID := stringField(row.Details, "worker_id")
    currentAttemptID, _ := task.Context["current_merge_attempt_id"].(string)
    currentContainerID, _ := task.Context["current_merge_container_id"].(string)
    currentWorkerID, _ := task.Context["current_merge_worker_id"].(string)
    if currentAttemptID == "" { return false, "", nil }
    if currentContainerID != "" && containerID != "" && currentContainerID != containerID { return false, "", nil }
    if currentWorkerID != "" && workerID != "" && currentWorkerID != workerID { return false, "", nil }
    var attempt model.WorkerAttempt
    err := tx.First(&attempt, "id = ? AND task_id = ? AND agent_type = ?", currentAttemptID, task.ID, "merger").Error
    if errors.Is(err, gorm.ErrRecordNotFound) { return false, "", nil }
    if err != nil { return false, "", err }
    if attempt.ContainerID != "" && containerID != "" && attempt.ContainerID != containerID { return false, "", nil }
    if attempt.WorkerID != "" && workerID != "" && attempt.WorkerID != workerID { return false, "", nil }
    return true, attempt.ID.String(), nil
}
```

In `applyIngestSideEffects`:

- If task is not `merging`, record event but skip context mutation.
- If current attempt does not match, record event but skip context mutation.
- If match succeeds, write merge context plus `merge_result_attempt_id` and `merge_result_container_id`.

### Verify context before dispatch consumes it

After reloading task in `dispatchMerge`:

```go
ctxAttempt, _ := task.Context["merge_result_attempt_id"].(string)
ctxContainer, _ := task.Context["merge_result_container_id"].(string)
if ctxAttempt != handle.AttemptID.String() || ctxContainer != res.ContainerID {
    o.logger.Warn("dispatchMerge: ignoring merge context from non-current attempt", ...)
    return result, nil
}
```

Then read `merge_commit` and `merge_conflicts`.

## RACE-SM-005: Remove reconciler auto-commit

### Add stale worktree helper

In `internal/gitexec/gitexec.go`:

```go
func WorktreeHeadDiffersFromBranchTip(ctx context.Context, dir, branch string) (bool, error) {
    head, err := RunGit(ctx, dir, "rev-parse", "HEAD")
    if err != nil { return false, fmt.Errorf("worktree freshness: resolve HEAD: %w", err) }
    tip, err := RunGit(ctx, dir, "rev-parse", branch)
    if err != nil { return false, fmt.Errorf("worktree freshness: resolve branch %s: %w", branch, err) }
    return strings.TrimSpace(head) != strings.TrimSpace(tip), nil
}
```

### Fail closed in orphan reconciliation

In `internal/orchestrator/reconcile.go`, replace the `CommitUnstagedChanges` block with:

```go
stale, err := gitexec.WorktreeHeadDiffersFromBranchTip(context.Background(), featureDir, featureBranch)
if err != nil {
    o.logger.Warn("reconcile: cannot verify feature worktree freshness", "feature", featureBranch, "error", err)
    continue
}
if stale {
    o.logger.Warn("reconcile: skipping orphan recovery on stale feature worktree", "feature", featureBranch, "feature_dir", featureDir)
    continue
}

clean, err := gitexec.IsClean(context.Background(), featureDir)
if err != nil {
    o.logger.Warn("reconcile: cannot inspect feature worktree cleanliness", "feature", featureBranch, "error", err)
    continue
}
if !clean {
    o.logger.Warn("reconcile: dirty feature worktree; refusing auto-commit during reconcile", "feature", featureBranch, "feature_dir", featureDir)
    continue
}
```

Longer-term: replace host-worktree reconciliation with temporary worktrees created from current bare refs.

## Tests

### Merge scoping tests

Add to `internal/orchhttp/server_test.go`:

- `TestIngestMergeResult_IgnoresStaleMergerAttempt`
  - Current task context points to new attempt/container.
  - Old merge result is ingested.
  - Event exists, but task context is unchanged.
- `TestIngestMergeResult_AcceptsCurrentMergerAttempt`
  - Current attempt/container matches.
  - Context receives `merge_commit`, conflict/failure metadata, and result attempt/container IDs.
- `TestIngestMergeResult_IgnoresSideEffectsForNonMergingTask`
  - Task status is terminal/non-merging.
  - Event is stored, context is unchanged.

Add to `internal/orchestrator/merge_dispatch_test.go`:

- `TestDispatchMerge_RecordsCurrentAttemptAndClearsStaleMergeContext`
- `TestDispatchMerge_IgnoresMergeContextFromDifferentAttempt`

### Reconciler stale worktree tests

Add to `internal/gitexec/gitexec_test.go`:

- `TestWorktreeHeadDiffersFromBranchTip_DetectsRefAdvancedBehindWorktree`
  - Advance branch ref from separate worktree/clone while host worktree remains stale.
  - Assert helper returns `true`.

Add to `internal/orchestrator/reconcile_test.go`:

- `TestReconcileOrphanedSubtasks_SkipsStaleFeatureWorktreeWithoutAutoCommit`
  - Bare ref is advanced beyond host worktree.
  - Run reconcile.
  - Assert no auto-commit subject is created and branch tip remains worker-pushed commit.
- `TestReconcileOrphanedSubtasks_MergesAgentBranchWhenFeatureWorktreeFreshAndClean`
  - Ensures legitimate clean recovery still works.

## Conflict Notes

- Merge dispatch currently logs `RecordSpawn` failures and continues. This plan makes merger identity failure fatal for that dispatch attempt because scoping depends on a durable attempt.
- Some existing ingest tests may expect `merge_result` with only `task_id` to update context. Update those tests to seed current attempt metadata or narrow them to event ingestion.
- Reconciler fix intentionally skips some recovery cases; it blocks corruption but may leave orphaned subtasks unresolved until a temp-worktree/bare-ref recovery flow lands.
- This plan overlaps with the spawn identity plan because both harden `RecordSpawn` from best-effort to required for container-scoped state.

## Open Questions

- Should merger/agentmon include `attempt_id` directly in `merge_result` payloads?
- Should stale `merge_result` events be updated with `side_effect_ignored` in details, or is logging enough?
- Should merger identity failure fail the task, leave it `merging`, or retry later?
- Should stale worktree detection compare only `HEAD` vs branch tip or also verify upstream/tracking refs?
