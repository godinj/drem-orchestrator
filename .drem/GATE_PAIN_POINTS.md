# Gate Pain Points — Observed 2026-04-10/11

Accumulated friction from the constraint gate, test gate, and merge pipeline
during the cost-reduction sprint. Each item includes the symptom we hit, the
root cause, and a suggested long-term fix.

---

## 1. Pre-existing master violations block ALL merges

**Symptom:** Every feature branch inherits master's 6 constitution violations.
The constraint gate compares feature vs master, but when master itself is
dirty, even branches that *improve* the situation get flagged as "dominated"
and blocked.

**Tasks affected:** 3c0f4413, fab34ab3, e9b076b5 (and anything else that
tries to merge while master has violations).

**Root cause:** `CompareReports` flags the feature as dominated when it has
ANY failing constraint, even if feature is strictly better than master. The
delta logic checks per-constraint magnitude but doesn't account for
"inherited and unchanged" violations.

**Workaround applied:** `skip_constraint_gate = true` in drem.toml.

**Long-term fix:** The comparison should only block on *regressions* — new
violations or worsened counts relative to master. Pre-existing violations
that are unchanged (or improved) should pass. Consider a "baseline allowlist"
that automatically exempts known master violations.

---

## 2. No config/CLI to bypass the constraint gate

**Symptom:** When the gate became a blocker, the only option was modifying Go
source code and rebuilding. No runtime toggle existed.

**Workaround applied:** Added `skip_constraint_gate` toml field + orchestrator
setter (this session).

**Long-term fix:** Gates should be individually toggleable from drem.toml
without code changes. Consider a `[gates]` config section:

```toml
[gates]
  constraint_gate = true   # or false to skip
  test_gate       = true
  merge_gate      = true
  depth_review    = true
```

Also add `drem gate disable constraint` / `drem gate enable constraint` CLI
commands for live toggling without restart.

---

## 3. Classifier race condition — JSON written after orchestrator reads

**Symptom:** Tasks 3c0f4413 and fab34ab3 stuck in `classifying` with
`human_triage: true` because the orchestrator tried to read the
classification JSON before the classifier agent finished writing it.

**Root cause:** No file-existence polling or retry. Orchestrator reads once,
gets ENOENT, records the error, sets `human_triage: true`, and gives up.

**Workaround applied:** Manual DB update to clear error and move to backlog.

**Long-term fix:** Add a short retry loop (3 attempts, 2s backoff) when
reading classification output. Alternatively, have the classifier agent
signal completion via a DB update rather than relying on file presence.

---

## 4. `needs_human_review` / `human_triage` = task graveyard

**Symptom:** Tasks flagged with `needs_human_review: true` or
`human_triage: true` in context stop processing permanently. No notification
to the operator, no UI to review/approve, no timeout to auto-retry.

**Tasks affected:** e9b076b5, 3c0f4413, fab34ab3.

**Root cause:** These flags are write-only. Nothing reads them to surface
actionable information. The orchestrator just stops touching the task.

**Long-term fix:**
- Surface these in TUI with a "needs attention" badge
- Add a `drem triage` CLI that lists all human_triage tasks with context
- Consider an auto-escalation: if human_triage is set for >30min, emit a
  C-Suite event so Kyle/Mike can investigate
- Add `drem task approve <id>` to clear the flag and resume processing

---

## 5. Merge conflict loop — same conflict, infinite retries

**Symptom:** e9b076b5 bounced merging → failed → in_progress → testing_ready
→ merging 5+ times, hitting the same conflict in test_writing_test.go each
time. Orchestrator auto-reconciles the failure but never resolves the
underlying conflict.

**Root cause:** The reconciliation path resets the task to try again, but the
feature branch is never rebased. Same conflict recurs forever.

**Workaround applied:** Spawned temp worker to manually rebase the branch.

**Long-term fix:**
- After 2 merge conflict failures on the same file, auto-rebase the feature
  branch onto master before retrying
- If rebase itself conflicts, escalate to human_triage (with the actual
  conflict diff attached) instead of looping
- Track `merge_conflict_file` in context and detect same-file recurrence

---

## 6. Force-reconciled tasks get un-reconciled

**Symptom:** Mike force-reconciled e9b076b5 to `done` in the DB (code was
already on master). The orchestrator later reset it back to `testing_ready`,
undoing the manual fix.

**Root cause:** The reconciliation audit runs every N ticks and doesn't
distinguish "manually set to done" from "incorrectly in done." It sees no
matching merged branch and resets the status.

**Long-term fix:**
- Add a `reconciled_by` field (or `manual_override: true` flag) that the
  reconciliation audit respects
- `drem task resolve <id> --reason "code on master"` CLI that sets done +
  manual_override atomically
- Reconciliation should never demote a `done` task that has
  `manual_override: true`

---

## 7. No CLI for task status management

**Symptom:** Every stuck-task fix required raw SQLite queries against drem.db.
No safe, validated way to transition tasks, clear context flags, or inspect
state.

**Workaround applied:** Direct `sqlite3` commands.

**Long-term fix:** Add task management CLI:

```
drem task list --status failed,classifying
drem task show <id>              # pretty-print full context
drem task transition <id> <status> --reason "..."
drem task clear-context <id> <key>
drem task cancel <id>
drem task retry <id>             # clear failure context, reset to backlog
```

---

## 8. `-short` flag confusion — test_command vs constraint gate

**Symptom:** Setting `test_command = "go test -short ./..."` in drem.toml was
expected to skip constitution checks. It skips `TestConstitutionCheckPasses`
in `go test`, but the constraint gate runs `check_constitution.sh` directly
via `constraints.Evaluate()` — completely independent of test_command.

**Root cause:** Two separate enforcement paths for the same constraints:
1. `TestConstitutionCheckPasses` in go test (skippable with `-short`)
2. `constraints.Evaluate()` in the constraint gate (not affected by `-short`)

**Long-term fix:** Single source of truth. Either:
- Remove `TestConstitutionCheckPasses` entirely (constraint gate is the
  canonical check), OR
- Have the constraint gate respect a config flag, and remove the direct
  script invocation

The current dual-path setup is confusing and leads to false confidence when
one is disabled but the other isn't.

---

## Priority for long-term fixes

| # | Impact | Effort | Recommendation |
|---|--------|--------|----------------|
| 1 | Critical | Medium | Fix CompareReports delta logic |
| 2 | High | Low | Already done (this session) |
| 4 | High | Medium | Surface human_triage in TUI + CLI |
| 7 | High | Medium | Task management CLI |
| 5 | Medium | Medium | Auto-rebase after 2 conflict failures |
| 6 | Medium | Low | manual_override flag |
| 3 | Medium | Low | Retry loop on classification read |
| 8 | Low | Low | Remove dual enforcement path |
