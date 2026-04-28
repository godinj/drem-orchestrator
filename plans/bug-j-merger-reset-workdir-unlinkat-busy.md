# Bug J — merger resetWorkDir fails on bind-mounted `/work`

**Status:** filed 2026-04-22 post-Session-N-restart Option-A dogfood.
Awaiting operator greenlight. Blocks v17 `6b6eb427` merge.

## Symptom

Every `/pass` gate on task v17 (`6b6eb427`) drives the orch to spawn
a `drem-merger` container, which exits 1 with:

```
{"time":"...","level":"ERROR","msg":"merge failed",
 "error":"merger: reset work dir: remove /work: unlinkat //work:
          device or resource busy"}
drem-merger: merger: reset work dir: remove /work: unlinkat //work:
             device or resource busy
```

Orch then logs `merge aborted: misc exit from merger (code=1)` and
reconciler cycles the task back through `all subtasks done, testing
ready` → `testing_ready fixer failed, needs human review` loop on a
5s cadence. Identical failure signature on every merger container
sampled (`inspiring_booth`, `serene_spence`, `unruffled_jepsen`) —
present at least 39h before this filing; this is NOT a Session N
regression.

## Root cause

`internal/merger/merger.go:348-362`:

```go
func resetWorkDir(workDir string) error {
    if err := os.RemoveAll(workDir); err != nil {
        return fmt.Errorf("remove %s: %w", workDir, err)
    }
    ...
}
```

`workDir` defaults to `/work` (`cmd/drem-merger/main.go:62`). The
merger container bind-mounts the feature worktree onto `/work`
itself, not a subdirectory. `os.RemoveAll("/work")` eventually
`unlinkat`s the mount point — which the kernel refuses because the
path is an active mount (EBUSY).

Diagnostically: `os.RemoveAll` walks the tree and successfully
unlinks children, then tries to unlink `/work` itself and fails. So
the contents probably DO get cleared before the final error — but
the non-zero exit from `resetWorkDir` aborts the whole merge before
the subsequent `cloneBranch` even starts.

## Why it didn't fire before Bug H

Before Bug H (`3fdcb85`), the merger CLI rejected empty `--test-cmd`
and failed BEFORE reaching `resetWorkDir`. That's why operators saw
"merger crash on empty TestCmd" as the visible symptom. Bug H moved
the fail-close/fail-fast to the orch side, so merger invocations now
reach runtime and hit this second, pre-existing failure.

Bug H was necessary but not sufficient. Merger-library scoreboard
item 10 ("merger library empty-TestCmd hardening") was already on
the Session N+1 shortlist for related reasons — J belongs to the
same cluster.

## Secondary 401

Same run also logs:

```
"merge result reporter failed",
"error":"merge_result POST http://orch:8080/internal/logs
         returned 401: unauthorized"
```

Cosmetic (the merger still does the merge; it just can't report
result-logs back). But symptom of an auth gap on `/internal/logs`
— unclear whether the merger's bearer token path was ever wired for
this endpoint, or the endpoint's auth changed and merger wasn't
updated. Track as Bug J-b.

## Fix options

**A. Remove contents, not the mount point** (preferred, minimal):
Rewrite `resetWorkDir` to list children of `workDir` and
`os.RemoveAll` each, leaving the mount point itself intact. Matches
the container reality.

```go
func resetWorkDir(workDir string) error {
    entries, err := os.ReadDir(workDir)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            // Parent creation handled below.
            if p := parentDir(workDir); p != "" && p != "." {
                return os.MkdirAll(p, 0o755)
            }
            return nil
        }
        return fmt.Errorf("read %s: %w", workDir, err)
    }
    for _, e := range entries {
        if err := os.RemoveAll(filepath.Join(workDir, e.Name())); err != nil {
            return fmt.Errorf("remove %s: %w", e.Name(), err)
        }
    }
    return nil
}
```

Then `cloneBranch` needs to clone INTO workDir (not clone workDir
itself) — a shape change: `git clone <repo> .` from inside workDir,
or `git -C workDir init && git fetch && git checkout`. Two-line
change in `cloneBranch`, plus a host test that mirrors a mounted
workDir.

**B. Treat EBUSY on the mount point as non-fatal.** Fragile; errors
on tree-walk error propagation. Not recommended.

**C. Change the container's mount topology** so `/work` is a
directory inside the container and the bind lands on a parent. Out
of scope of the merger library fix; requires compose-template
change + coordination with the orch's worker-spawner.

## Test plan

1. Reproduction unit test in `internal/merger/merger_test.go` using
   a tmpdir that simulates a "can't unlink the root" condition
   (e.g., via a symlink and a chmod trick, or a genuine bind mount
   in a test harness if one exists).
2. Integration reproduction via the existing e2e harness by
   passing a pre-created workDir that must remain after resetWorkDir.
3. Manual: unblock v17 (`6b6eb427`) — `/pass` → expect merging →
   done, no roll-back.

## Recommendation

Option A. One-function change in the merger library, one additional
unit test, one shape tweak in `cloneBranch`. Belongs in
Session N+1 alongside scoreboard item 10. Operator greenlight gate.

## Scope this is NOT

- Does not touch orch's `buildMergerArgv` or `inferTestCommand`
  (Bug H shipped correctly).
- Does not touch the 401 on `/internal/logs` (Bug J-b, separate
  ticket).
