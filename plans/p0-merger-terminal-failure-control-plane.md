# P0: Merger Terminal-Failure Control-Plane Patch

Status: implementation authorized by operator on 2026-04-24
Scope: merger/reconciler control-plane safety for task `6b6eb427` and future deterministic merge failures

## Problem

The current merger path can cycle a parent task through `testing_ready -> merging -> failed -> in_progress -> testing_ready` after deterministic merger failure. Recent evidence also showed a merger crash recorded against the zero UUID while adjacent spawn events identified the real parent task. That makes merger exhaustion non-terminal, hides task-correlated failure evidence, and allows reconciler completion logic to requeue a parent whose merge has already failed deterministically.

## Goals

- Make deterministic merger conflicts and exhausted merger retry budgets terminal for the parent task.
- Record merger failure evidence against the real parent task before and after merger launch.
- Prevent zero-UUID merger crash or failure events from passing silently.
- Add preflight checks that block impossible merger launches before spawning a merger container.
- Guard reconciler recovery so `all subtasks done` cannot resurrect a parent with terminal merger failure or exhausted merger attempts.

## Non-Goals

- Do not auto-resolve the five-file CanaryV17 conflict.
- Do not add broad merge-conflict heuristics beyond existing `git rerere` behavior.
- Do not repair stale task artifacts in place.
- Do not expand unrelated lifecycle or task-filing surfaces in this patch.

## Implementation Sequence

1. Add or reuse a task-correlated merger-attempt record that is written before each merger spawn. It must include parent task ID, attempt number, container ID/name when known, timestamps, exit code when known, normalized failure reason, and log/stderr reference when available.
2. Add merger preflight before spawning a merger container. Check that source branch/ref and target ref are present, the merger can resolve the intended parent task ID, and required evidence/log locations are writable. A preflight failure records task-correlated blocked/failed evidence and launches no container.
3. Normalize deterministic conflict and exhausted retry outcomes into a terminal parent state or terminal failure reason. The exact state name may use the existing failed state if no `merge_blocked` state exists, but the reason must distinguish `conflict` and `merge_failed_after_attempts` from transient worker failure.
4. Guard reconciler parent recovery. A parent with terminal merger failure, deterministic conflict, or exhausted merger attempts must not be moved by `reconcile-failed-parent-all-subtasks-done` back to `in_progress` or `testing_ready`.
5. Make zero-UUID merger evidence impossible in the regression path. If the real task ID cannot be resolved, record a preflight/dispatch failure against the attempted parent and do not emit a zero-UUID crash/failure event as the only evidence.

## Regression Tests

- Induced merge conflict produces one terminal parent outcome and no later reconciler-driven `in_progress` or `testing_ready` transition.
- Configured retry budget of `N` produces exactly `N` merger spawns, then one terminal parent transition.
- Reconciler skips `reconcile-failed-parent-all-subtasks-done` for a parent with exhausted merger attempts.
- Merger exit `128` records the real task ID, attempt number, container ID/name, exit code, failure reason, and log reference.
- Preflight failure records task-correlated blocked/failed evidence and launches no merger container.
- A zero UUID task ID in merger failure evidence fails the regression test.

## Likely Touch Points

- Merger dispatch and spawn orchestration around `internal/orchestrator/merge_dispatch.go` and related worker-spawn helpers.
- Reconciler parent-completion logic that currently emits or acts on `reconcile-failed-parent-all-subtasks-done`.
- Merger command/worker evidence plumbing under `cmd/drem-merger/` if exit-code normalization or log reference capture is currently missing there.
- Existing model/event types only if the current schema lacks a durable place for merger attempt metadata or terminal merge-failure reason.

## Verification

- `gofmt` on changed Go files.
- Targeted Go tests for merger dispatch, reconciler recovery, and merger evidence plumbing.
- Full relevant package tests if targeted tests pass.
- `scripts/check_constitution.sh` before merge readiness.

## Acceptance Criteria

- Task `6b6eb427` or an equivalent induced-conflict fixture cannot re-enter the merger/reconciler loop after deterministic conflict or retry exhaustion.
- Merger failure evidence is task-correlated and includes enough attempt/log context for Mike/Seth review without relying on container archaeology.
- Preflight failures are visible as task-correlated blocked/failed evidence and do not launch merger containers.
- Existing successful merge behavior remains unchanged.

## Seth follow-up: merge-control git identity source scope at 2026-04-28T00:10:38Z

Seth reported that the smallest next source package is deterministic command-local Git identity plus structured agent-merge failure evidence in orchestrator merge-control. This is not a lifecycle mutation and does not reopen CanaryV17, which remains done on supported surfaces.

Implementation scope to route through the source-capable coder lane:

- Confirm exact source filenames by searching for `merge into feature branch failed` because Seth's container has no repo checkout.
- Ensure merge/commit operations that can create commits in container filesystems use deterministic per-command Git identity, preferably `git -c user.name=drem-orchestrator -c user.email=drem-orchestrator@localhost ...` or command-local environment. Do not use global Git config.
- Preserve `MergeResult.GitStderr` and conflict details through `handleAgentMergeFailure()` so `Committer identity unknown` becomes a diagnosable failure while preserving the agent branch.
- Align agent-branch merge-control evidence with supported `merger_attempt_started` / `merger_attempt_finished` style fields: real task ID, attempt, phase, worker/container, branch, command, exit code, stderr or log reference. No zero-UUID-only crash/commit/merge evidence should be accepted as sufficient.

Expected tests remain scoped to `internal/orchestrator` and should use `internal/testutil` Git helpers: command-local identity in repo/container FS with no configured Git identity, agent merge failure normalization with stderr preserved, and supported event-surface correlation to the real task/attempt.

## Kyle source-lane routing accepted at 2026-04-28T00:28:31Z

Kyle accepts Mike's `2026-04-28T00:12:37Z` report replying to operator thread `a916cb6a`: no supported surface currently shows a source-lane task or coder identity for the merge-control Git identity/evidence package, and Mike found no Mike-side `dremctl` task-create or arbitrary spawn route. Mike correctly avoided host-exec, Docker/log break-glass, lifecycle mutation, retry/pass/fail, SGLang action, global Git config, source search, and legacy temp-worker/tmux paths.

Supported-surface recheck at `2026-04-28T00:28:31Z` shows orchestrator health OK, no running workers in the world summary, and recent tasks `3a5cba14`, `d4cb0f44`, `b8147d33`, `cfbf6327`, `91839e84`, and `a629ffe2` all terminal `done`; no visible recent source task is carrying this merge-control package.

Decision: Alex owns filing or designating the source-capable orchestrator/cold-worker lane for deterministic command-local Git identity plus structured agent-merge failure evidence, with Seth's recorded scope as the acceptance bar. Kyle emitted a high-priority routed outbox request to Alex under corrid `0571d4eb` and reported the blocker/status back to the operator thread. No Kyle lifecycle mutation, gate action, retry, credential change, SGLang restart, destructive Docker/git action, unsupported log route, host-exec, or legacy route is opened by this acceptance.
