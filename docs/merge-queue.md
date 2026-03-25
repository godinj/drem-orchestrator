# Merge Queue

The merge queue serializes feature-branch merges and adds automatic rebase and
conflict classification, eliminating the cascading merge failures that occur when
multiple agents complete work concurrently.

## Architecture

```
orchestrator (MERGING tasks)
        │
        ▼
   MergeQueue          ← sync.Mutex serializes calls
        │
        ├─ rebase feature branch onto current main HEAD
        │
        └─ delegate to merge.Orchestrator (the original merger)
```

`MergeQueue` implements the same `mergerClient` interface as the underlying
`merge.Orchestrator`, so the swap is transparent to the orchestrator:

```go
merger := merge.NewMergeQueue(merge.NewOrchestrator(wt, database), wt)
```

`MergeQueue` accepts two collaborators:

| Collaborator | Interface | Concrete type |
|---|---|---|
| `merger` | `merge.Merger` | `*merge.Orchestrator` |
| `rebaser` | `merge.Rebaser` | `*worktree.Manager` |

## Rebase-before-merge strategy

Before every `MergeFeatureIntoMain` call, the queue:

1. Looks up the feature worktree path via `FindWorktreeByBranch`.
2. Resolves the main worktree path via `MainWorktreePath`.
3. Rebases the feature branch onto current main HEAD.
4. If the rebase succeeds, delegates to the underlying merger.
5. If the rebase conflicts, returns a failed `MergeResult` with the conflict
   list — the underlying merger is never called.

If the feature worktree cannot be found (e.g., already cleaned up), the queue
falls back to a direct merge without rebase.

## Serialization

A `sync.Mutex` ensures only one `MergeFeatureIntoMain` call executes at a time.
This prevents two merges from racing to update the same main branch, which was
the root cause of cascading conflicts when multiple agents completed work in the
same tick.

`MergeAgentIntoFeature` (agent → feature branch merges) is **not** serialized —
these target different branches and cannot conflict with each other.

## Conflict classification

When a merge or rebase fails with conflicts, the orchestrator classifies each
conflicting file:

| Class | Criteria | Examples |
|---|---|---|
| **Trivial** | `.md`, `.txt` extensions; `LICENSE`, `.gitignore` filenames | `README.md`, `CHANGELOG.md` |
| **Non-trivial** | Everything else | `.go`, `.toml`, `.json` source/config files |

Classification is performed by `merge.ClassifyConflicts()` and formatted by
`merge.FormatClassifiedConflicts()` for inclusion in failure messages and events.

## Auto-resolution

When **all** conflicts in a merge are trivial, `AutoResolveConflicts` can
resolve them automatically by accepting the incoming (`--theirs`) version:

1. For each trivial file: `git checkout --theirs <file>` then `git add <file>`.
2. Finalize with `git commit --no-edit`.

If **any** conflict is non-trivial, auto-resolution is skipped entirely — the
worktree is left untouched for human or agent intervention.

## Structured failure reporting

Merge failures include classified conflict details in both the task failure
reason and the `merge_conflict` event:

- **Task failure reason**: human-readable summary with trivial/non-trivial
  counts and per-file classification.
- **Event details**: structured map with `conflicts` (raw file list) and
  `classified_details` (formatted classification string).

The orchestrator logs a classification summary for each failure:
```
merge conflict classification task_id=<id> trivial=N non_trivial=M
```
