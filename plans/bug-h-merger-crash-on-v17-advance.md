# Bug H — Merger exits code=1 with `missing required flags: [--test-cmd]`

**Status**: OPEN. Investigation complete; fix not yet greenlit. Plan
filed to capture root cause and fix options so operator can pick an
implementation path.

**Origin**: 2026-04-22. Surfaced during live dogfood of the new
v15/v16 retry CLI (Bug F follow-up). v17 parent
`6b6eb427-a250-4339-bef7-5abb845817e4` advanced from `testing_ready`
to `merging` via `POST /projects/drem-orchestrator/tasks/{id}/pass`
(11ms, actor=user). Orch spawned a `drem-merger` container
(`serene_spence`); container exited with code 1 three seconds later.
Orch translated the exit via `failureReasonForExit` to
`"merge aborted: misc exit from merger (code=1)"` and transitioned
v17 `merging → failed`.

Reconciler subsequently rolled v17 back to `testing_ready` (see
`reconcile: all subtasks done, recovering parent` at 06:31:08Z), and
the parent is currently in a hot-loop emitting
`testing_ready fixer failed, needs human review` every 5 seconds (see
Bug I for the hot-loop impact on the event bus).

## Root cause

Exit-1 is `drem-merger`'s fall-through "misc" code in
`cmd/drem-merger/main.go::exitCodeFor`. The actual reason is in the
container's stderr, not in the orchestrator log:

```
$ docker logs serene_spence
drem-merger: missing required flags: [--test-cmd]
```

`cmd/drem-merger/main.go::parseFlags` (line 86) treats an empty
`--test-cmd` value the same as a completely-missing flag:

```go
if strings.TrimSpace(f.testCmd) == "" {
    missing = append(missing, "--test-cmd")
}
```

The orch-side caller `internal/orchestrator/merge_dispatch.go::160`
passes `o.testGate.TestCommand` verbatim as the argv value. In the
live drem-orchestrator project the project-level `drem.toml` does
**not** declare a `test_command`:

```
# /var/lib/drem/drem.toml (mounted into orch container)
[project]
  language = "go"
```

Orch's `TestGateConfig.TestCommand` is therefore the zero-value empty
string. `buildMergerArgv` still emits `--test-cmd ""` as two argv
entries; the merger's flag parser walks the argv, finds the `=""`
value, runs it through `strings.TrimSpace`, concludes the flag was
not supplied, and exits 1.

This is a **contract mismatch between the orch and the merger**:
- Orch treats TestCommand as "optional, skip tests when empty".
  See `internal/orchestrator/test_execution.go::238` where a
  downstream code path guards on `if cmd == "" { skip }`.
- Merger treats TestCommand as "required, refuse to start if empty".
  See `cmd/drem-merger/main.go::86` and the doc header `"TestCmd:
  empty TestCmd means 'no tests', which is the rollback-safe default
  for unknown project types"` from `internal/merger/merger.go:74` —
  **the contract inside the merger library says "empty is fine", but
  the CLI wrapper enforces non-empty**.

Two separate contracts, one test-suite covered (the library), one
drift-free-broken (the CLI flag validator). `merge_dispatch.go` sits
between them and trusts the library contract.

## Impact

- v17 cannot advance. Every time operator (or reconciler) pushes it
  to `merging`, the merger crashes and it rolls back to a `failed`
  or re-entered `testing_ready` state.
- v15 and v16 parents are `failed` (per Bug F origin). Retrying them
  via the new retry CLI would queue a merger spawn too → same crash.
- **All 3 live dogfood tasks are effectively wedged behind this one
  contract mismatch.** The retry/pass gate verbs work; the merger
  spawn they trigger does not.

## Fix options

Ranked by implementation cost and operator-risk.

### Option A (preferred) — Infer TestCommand at orch startup + fail-fast guard

Two changes, both in `internal/orchestrator/`:

1. **Populate TestCommand from `inferTestCommand` when drem.toml
   leaves it blank.** `inferTestCommand` already exists
   (`internal/orchestrator/test_execution.go::283`); extend
   orch bootstrap so the runtime value is `go test ./...` for a Go
   project even when the operator did not declare one. Honors the
   rollback-safe "empty means skip" contract for non-Go projects by
   leaving TestCommand empty only when inference returns empty.

2. **Fail-fast in `buildMergerArgv`.** If `testCmd == ""`, return an
   error and refuse to spawn the merger; orch can then transition
   the task to `failed` with an operator-facing reason
   (`"merger spawn skipped: project has no test command"`) instead
   of leaking a code-1 misc-exit with no context. This closes the
   contract-mismatch permanently — orch either sends a non-empty
   argv value or refuses to spawn.

**LOC estimate**: ~40. One commit `feat(orch): infer project test
command at startup and fail-fast merger spawn on empty TestCommand`.

**Regression proof**: add test to
`merge_dispatch_test.go::TestBuildMergerArgv_EmptyTestCmdRejected`
and `test_execution_test.go::TestInferTestCommand_AppliedAtStartup`.

### Option B — Operator config fix + docs update

Add `test_command = "go test ./..."` to
`/home/godinj/.drem/projects/drem-orchestrator/data/drem.toml` and
re-register the project via `drem project register --update`.

**LOC estimate**: 1 line in drem.toml, plus doc update in
`install.md` to mention the field.

**Downside**: does not fix the underlying contract mismatch. Next
project to register without `test_command` hits the same trap. Not
plan-worthy alone; pairs with A as the immediate unblock.

### Option C — Merger-side: accept empty TestCmd and skip test phase

Change `cmd/drem-merger/main.go::parseFlags` so `--test-cmd ""` is
valid and mapped to `TestCmd: ""` on the library side, which already
treats empty as "no tests". Honors the library's documented
contract.

**LOC estimate**: ~15. One commit `fix(drem-merger): accept empty
--test-cmd as no-tests (library contract)`.

**Downside**: removes a fail-fast guardrail. If an operator wanted
tests to run and misconfigured the project, the merger would silently
merge without running them. Combines poorly with orch-side B because
the "no test_command" configuration becomes invisible.

### Option D — Verbose merger exit logging (observability only)

Add stderr capture to `executeMerge` so future merger code-1 crashes
surface the inner reason in the orch log without requiring
`docker logs <container>`. Orthogonal — doesn't fix the bug but
shortens the next investigation.

**LOC estimate**: ~30 in `merge_dispatch.go` + test.

## Recommendation

**Ship A as the primary fix + D as the observability follow-up, both
in one commit sequence.** A makes the contract explicit and forces an
orch-side refusal when TestCommand is missing; D ensures the next
crash (different cause) is self-diagnosing.

Ship operator-side B (drem.toml test_command) as the immediate
unblock-tonight if operator wants v17/v15/v16 to complete before A
lands. Without A, though, the underlying mismatch remains.

**Skip C** — removing the merger-side guard regresses safety.

## Constitution notes

- `internal/orchestrator/test_execution.go` is currently 303 lines;
  adding inferTestCommand-at-startup wiring estimated ~10 lines
  (still under 800 cap).
- `internal/orchestrator/merge_dispatch.go` is 326 lines; +20 for
  fail-fast guard keeps it well under cap.
- Full-repo `go test ./...` must stay GREEN. Merger-side tests in
  `internal/merger/merger_test.go` already cover `TestCmd=""` as
  "skip tests" — no change needed there.

## Out of scope for this plan

- Root-causing why the reconciler rolls v17 back to `testing_ready`
  after `failed` status. That belongs in a separate hot-loop fix
  (see Bug I) — same symptom class as the
  `testing_ready fixer failed` loop.
- Merger retry logic. `failureReasonForExit` distinguishes
  `miscExit` (non-retryable) from `pushFailed` (retryable); misc-
  exit is correctly non-retryable today.
- drem-merger image rebuild. The running image is already the
  correct one; the crash is in argv parsing, not in image content.

## Operator decision point

Pick A+D, B alone, or A+B+D. Do NOT ship C.
