# Reconcile file extraction

Bring `internal/orchestrator/reconcile.go` back under the 800-line
constitution ceiling by extracting the stuck-agent sweep into a sibling
file. Pure mechanical move — no semantic change.

## Problem

T3 canary pause from constitution check:

```
FAIL: File length ceiling — internal/orchestrator/reconcile.go has 817
lines, exceeds limit of 800
```

The reconciler-container-awareness work (commits `924b751..efc7c5c`) added
~55 lines to `reconcileStuckAgents` — a new `containerRunningSet` lookup
plus a `buildContainerRunningSet` helper that calls `o.Spawner.ListWorkers`.
That pushed the file from 770 → 817 and tripped the ceiling.

Raising the limit is not an option; the ceiling is in
`.drem/constraints.toml` specifically to keep coupling visible.

## Chosen cut line

Move to a new sibling file `internal/orchestrator/reconcile_stuck.go`:

- `reconcileStuckAgents` — the stuck-agent sweep, including the new
  container-awareness early-exit.
- `buildContainerRunningSet` — helper populating the container-running
  set from `o.Spawner.ListWorkers`.

Why these two together: they are the only code path in `reconcile.go`
that consults the spawner directly; every other sweep operates purely on
the DB + runner + worktree. Extracting them together removes the
`internal/spawner` import from `reconcile.go` and keeps the stuck-agent
concern cohesive in one file.

Why `reconcile_stuck.go` not `reconcile_containers.go`: the existing
`reconcile_containers.go` already carries a distinct concern — startup
respawn via `reconcileOnStartup` + event-driven container reconciliation.
Folding stuck-agent detection into that file would conflate two reconcile
flows (one-shot startup vs. per-tick sweep). Keeping the stuck-agent
sweep in its own file makes the boundary legible and matches the
per-sweep naming pattern already used by `reconcile_parents.go`.

## Before / after

| File                       | Before | After |
| -------------------------- | ------ | ----- |
| `reconcile.go`             | 817    | 635   |
| `reconcile_stuck.go`       | —      | 193   |

`reconcile.go` now sits with ~165 lines of headroom below the ceiling.

## Behavioural confirmation

No function bodies changed. No signatures changed. No call sites changed
(`o.reconcileStuckAgents()` in `Reconcile` still resolves to the same
method on the same receiver, just defined in a sibling file). The
`internal/spawner` import moved to `reconcile_stuck.go`; no other imports
changed.

Validation:

- `go vet ./...` clean.
- `go test -count=1 ./internal/orchestrator/...` — green in ~47 s.
- `go test -count=1 ./...` — green in ~53 s.
- Pre-existing tests in `reconcile_test.go` (6 `reconcileStuckAgents`
  tests) and `reconcile_containers_test.go` (4 container-awareness tests
  added by `924b751`) exercise the moved code unmodified.

## Out of scope

- Splitting `reconcile_containers.go` (still 155 lines, well inside the
  ceiling; a split today would be premature).
- Any semantic change to stuck-agent detection — the container-awareness
  logic moves verbatim.
- Renaming or restructuring the `Reconcile` dispatch list.
- Adding new tests — mechanical moves don't warrant new coverage.
