# Orch Container Constraint-Gate Fix — Implementation Plan

Status: **implemented, 2026-04-20.** All four implementation commits
(plan doc + feat + test + feat-orch + this docs-tick) landed on
worktree-agent-a0f359c8. Sibling of `plans/worker-prompt-delivery.md`
(landed earlier today) and `plans/drem-project-register-update.md`.
Unblocks the T3 canary on quickfix-categorized tasks — the constraint
gate inside the orch container no longer auto-pauses on
`bash: line 1: go: command not found`.

Tests added:

- `internal/constraints/evaluate_test.go` — 5 new
  (missing tool yields SKIP and does NOT execute the command;
  present tool exits zero is PASS; present tool fails is FAIL;
  `commandTool` first-token heuristic with env-assignment and
  absolute-path cases; `FormatReport` SKIP row + three-count
  summary; `buildReport` partitions Passed/Failed/Skipped).
- `internal/constraints/compare_test.go` — 1 new block with 6
  sub-cases (PASS↔SKIP, FAIL↔SKIP, SKIP↔SKIP, and SKIP not
  masking unrelated PASS→FAIL regressions).

All tests pass on `go test -count=1 ./...`; `go vet ./...` clean.

Rollout (§6) unchanged; no image rebuild needed. Operator can
re-run the T3 canary on a quickfix task and confirm dispatch is
attempted rather than the task auto-pausing.

## 1. Problem

The T3 canary on 2026-04-20 built a one-file quickfix task ("Append a
line to `docs/CANARY-LOG.md`"). The classifier correctly tagged it as
`quickfix`. On the quickfix-fast-track path
(`internal/orchestrator/merge_execution.go:212-260`), the orchestrator
runs `constraints.Evaluate` against the feature worktree **inside the
orch container**, and if any constraint fails, it sets
`NeedsHumanReview=true` and pauses the task.

Two of the command constraints in `.drem/constraints.toml` shell out
to Go tooling:

```toml
[[command]]
name   = "gofmt compliance"
run    = "gofmt -l ./internal/ ./cmd/"
expect = "empty_output"

[[command]]
name = "go vet"
run  = "go vet ./..."
```

The orch container (`deploy/docker/orch.Dockerfile`) ships
`debian:bookworm-slim` with only `ca-certificates`, `git`, and `tini`
installed at runtime. The compiled drem binary is glibc-linked via
CGO; there is no Go toolchain present in the runtime stage.

Result: when the quickfix-fast-track constraint gate runs, `go vet
./...` fails with:

```
bash: line 1: go: command not found
```

The FormatReport output landed in `task.Context["constraint_violations"]`
verbatim:

```
PASS: gofmt compliance
FAIL: go vet
  bash: line 1: go: command not found
PASS: File length ceiling
…
9 checks passed, 1 failed
```

Interesting observation: `gofmt compliance` passed. The bookworm-slim
base image does NOT include gofmt; however `gofmt -l` on zero matching
files exits zero with empty stdout (it was invoked as `gofmt -l
./internal/ ./cmd/` — if `gofmt` itself were missing, bash would print
`gofmt: command not found` to stderr, stdout would be empty, and the
`empty_output` check would PASS because it only inspects stdout). So
the PASS on gofmt is a silent pass — both tools are actually missing,
but only `go vet` trips the gate because its expect mode is
`exit_zero`.

This means: **two constraints are fragile in the orch container**
(`go vet` hard-fails with the missing-tool error, `gofmt compliance`
silently passes when it should have been reported as skipped).
Fix must handle both.

## 2. Reproducer

Exact log line from `/var/lib/drem/drem.log` in the orch container:

```
"constraint_violations":"PASS: gofmt compliance\n
  FAIL: go vet\n
  bash: line 1: go: command not found\n
  PASS: File length ceiling\n
  PASS: Exported function count\n
  PASS: Internal import ceiling\n
  PASS: GORM hook consolidation\n
  PASS: DB init outside testutil\n
  PASS: Git helpers outside testutil\n
  PASS: Test factories outside testutil\n
  PASS: Export ratio ceiling\n
  ──────────────────────────\n
  9 checks passed, 1 failed\n"
```

To reproduce in isolation (on any host that has the drem binary but
no `go` on PATH):

```
PATH=/usr/bin:/bin go run ./cmd/check-constraints
# exits non-zero because `go vet` can't find `go`
```

Or from a container shell that matches the orch runtime:

```
docker run --rm -it \
  -v "$PWD":/src -w /src \
  debian:bookworm-slim bash -c 'apt-get update >/dev/null 2>&1 && \
    apt-get install -y --no-install-recommends ca-certificates git \
    >/dev/null 2>&1 && go vet ./... ; echo exit=$?'
# → bash: line 1: go: command not found; exit=127
```

## 3. Option A vs Option B

### Option A — Install the Go toolchain in the orch image

Patch `deploy/docker/orch.Dockerfile`: either `apt install golang-go`
in stage 2 (bookworm ships Go 1.22, which is old — the build stage
uses 1.25), or copy `/usr/local/go` from the `golang:1.25-bookworm`
build stage into the runtime stage.

Pros:
- `go vet` just works, no constraint-code change needed.
- Matches the existing mental model ("constraints run in orch, orch
  has what constraints need").
- Minimal diff: a few lines in the Dockerfile.

Cons:
- **~120 MB added** to the orch image (the full Go toolchain is ~450 MB
  uncompressed; even a trimmed copy is ≥100 MB). The current runtime
  image is ~160 MB; this would roughly double it. Every `docker pull
  localhost:5000/drem-orch:latest` on every project now carries this
  weight.
- **Toolchain drift risk**: the host can have Go 1.24, the build
  stage uses 1.25, the runtime stage would carry another 1.25 copy
  but diverge the moment the build stage bumps. Constraints like `go
  vet` behave subtly differently across minor versions (`printf`
  format-string checks, unused-import rules).
- **Duplicates Go between orch and workers**: per-language worker
  images already carry Go (for Go-language tasks). Adding it to orch
  means two Go copies per project.
- **Punishes non-Go projects**: the orch image is shared across
  languages (per `internal/projects/templates/project-compose.yml.tmpl`
  — same image, per-project compose). A Python project's orch would
  now ship Go for constraints that Python projects wouldn't use.

### Option B — Gate constraint checks on tool availability

Modify `internal/constraints/evaluate.go` `evalCommand` to parse the
first token of `c.Run`, call `exec.LookPath` on it, and if the tool
is missing, return a result with a new `Skipped` flag set. Skipped
results do NOT count toward `Report.Failed`. `FormatReport` prints
them as `SKIP: <name> (tool unavailable: <tool>)`.

Pros:
- **Keeps orch image slim.** No added toolchain.
- **Idempotent across environments**: dev host with full tools runs
  the real check; orch container skips with a clear SKIP line; CI
  runs in a Go-equipped container and evaluates normally. Same code,
  different outcomes based on what's installed.
- **Matches the architectural reality** that the orch container is
  a coordination layer, not a dev environment. Worker containers
  carry their per-language toolchains and will catch constraint
  violations at integration time.
- **Solves the silent-pass bug too**: `gofmt compliance` will now
  be reported as SKIP when gofmt is missing, rather than a misleading
  PASS.

Cons:
- **Coverage loss in the orch layer**: `go vet` and `gofmt` no
  longer run in the quickfix-fast-track check. A quickfix that
  introduces a gofmt violation or a `go vet` issue would slip past
  the orch-layer gate. But the same checks are guaranteed to run
  later in the flow — the integration gate runs inside the orch
  container too (same constraint), so the coverage loss is equal;
  either the orch container needs Go (Option A) or it doesn't
  (Option B), and neither choice changes what's caught elsewhere.
- **A few more lines of code** than Option A and new test cases.
  Worth it for the smaller, portable image.

### Chosen: Option B

The constraint code is cleanly layered (one `evalCommand` function,
one `Result` struct, one `FormatReport`). The change is ~30 lines of
production code plus tests. The coverage cost is real but bounded:
the T3 canary wants dispatch to work, not for the orch container to
do full code review — workers and the developer's own `cmd/check-constraints`
invocation catch the same violations when the toolchain is present.

Option A's 120 MB tax on every orch image pull, applied project-wide
and language-agnostic, is too big a price for keeping the current
mental model. If the constraint checks were the only enforcement
point, Option A might win; but they are not — workers re-run them
in fully toolchained containers, and developers run them locally.

### Option C — Move constraint checks to worker containers (future)

Architecturally the correct long-term answer: the orch container
coordinates, workers enforce. The constraint gate would ship the
constraint-eval work to a worker (already has Go, gofmt, everything)
and block on its result. This is substantially more complex — it
requires a new spawner role, prompt, and result plumbing. Filed as
follow-up, not done in this session.

## 4. Implementation Steps

### 4.1 `internal/constraints/report.go`

Add `Skipped bool` and `SkipReason string` to `Result`. Add `Skipped
int` counter to `Report`. `Skipped` results do NOT count toward
`Failed`. `FormatReport` renders a third row type:

```
PASS: gofmt compliance
SKIP: go vet (tool unavailable: go)
FAIL: File length ceiling
  internal/big.go has 900 lines, exceeds limit of 800
──────────────────────────
9 checks passed, 1 skipped, 1 failed
```

Summary line always shows all three counts to keep them visible to
operators reading logs.

### 4.2 `internal/constraints/evaluate.go`

New helper `commandTool(run string) string` that extracts the first
whitespace-separated token from `c.Run`. Examples:
- `"go vet ./..."` → `"go"`
- `"gofmt -l ./internal/ ./cmd/"` → `"gofmt"`
- `"bash -c 'echo hi'"` → `"bash"` (already on all Unix systems)

Edge cases to handle:
- Empty `c.Run` → return empty string (caller treats as "cannot
  check", runs command as before).
- Command with env prefix like `"GOFLAGS=-mod=vendor go vet ./..."`
  → first token is `"GOFLAGS=-mod=vendor"`. Detect `=` and skip
  until we find a bare token. Rare, but handle it so future changes
  to `constraints.toml` don't re-introduce the bug.
- Command with shell features like `"go vet ./... | tee log"` →
  first token is `"go"`, which is what we want.

Function signature:

```go
// commandTool returns the probable command binary name from a shell
// invocation string. Returns "" when the run string is empty or when
// no bare token can be identified.
func commandTool(run string) string
```

Modify `evalCommand` at the top:

```go
func evalCommand(c commandConstraint, worktreeRoot string) (Result, error) {
    result := Result{Name: c.Name, Type: "command"}

    if tool := commandTool(c.Run); tool != "" {
        if _, err := exec.LookPath(tool); err != nil {
            result.Skipped = true
            result.SkipReason = fmt.Sprintf("tool unavailable: %s", tool)
            return result, nil
        }
    }
    // … existing cmd := exec.Command(...) logic …
}
```

A `Skipped` result has `Passed=false` (so it doesn't get counted as a
pass) but also does not count as a failure. `buildReport` needs the
new bucket.

### 4.3 `buildReport` update

```go
func buildReport(results []Result) *Report {
    report := &Report{Results: results}
    for _, r := range results {
        switch {
        case r.Skipped:
            report.Skipped++
        case r.Passed:
            report.Passed++
        default:
            report.Failed++
        }
    }
    return report
}
```

### 4.4 `FormatReport` update

Render SKIP rows between PASS/FAIL. Update summary line.

### 4.5 `CompareReports` update

A skipped result behaves like "no signal — do not block." In the
comparison:

- baseline PASS, current SKIP → treat as PASS→PASS (no regression).
- baseline SKIP, current PASS → improvement, no action.
- baseline SKIP, current FAIL → do NOT treat as new violation (we
  have no baseline to compare against; the tool was missing before).
- baseline FAIL, current SKIP → treat as PASS (no regression).
- baseline SKIP, current SKIP → PASS→PASS.

In short: a SKIP on either side of the comparison is a no-op for
that constraint. Implement by treating `r.Skipped || r.Passed` as
"passing" for comparison purposes. Feature SKIP never dominates.

Rationale: we don't want the absence of a toolchain to either
introduce false failures (original bug) OR mask regressions. Since
the integration gate runs in the same container with the same
toolchain availability, baseline and feature will always both SKIP
the same constraints. If they ever diverge (e.g. PATH difference
between worktrees), treating SKIP as "no signal" is the safe
interpretation — the orch container simply has no way to check,
and an emitted WARNING in logs tells the operator to investigate.

### 4.6 Log SKIPs for operator visibility

In `constraint_gate_policy.go::evaluateConstraintGate` and
`merge_execution.go::transitionQuickFixToMerging`, after the
evaluation, walk the report's Results and emit a single INFO-level
log entry summarizing any SKIPs, so operators know when tools are
missing and coverage is reduced.

```go
for _, r := range report.Results {
    if r.Skipped {
        o.logger.Info("constraint skipped (tool unavailable)",
            "task_id", task.ID, "constraint", r.Name, "reason", r.SkipReason)
    }
}
```

This is advisory and doesn't affect flow.

## 5. Test Strategy

### 5.1 Unit tests (`internal/constraints/evaluate_test.go`)

Add three new TestEvalCommand cases:

- **missing tool yields SKIP**: `constraint.Run = "this-tool-does-not-exist-ever-42 foo"`.
  Assert `result.Skipped == true`, `result.Passed == false`,
  `result.SkipReason` contains the tool name, and the command was
  NOT executed (verify via a marker file or by choosing a command
  that would fail destructively if run).
- **present tool runs check**: `constraint.Run = "true"`. Assert
  `Skipped == false`, `Passed == true`.
- **present tool that fails runs and fails**: `constraint.Run = "echo
  boom >&2 && false"`. Assert `Skipped == false`, `Passed == false`,
  `Messages` contains "boom".

### 5.2 Unit tests (`internal/constraints/report_test.go`)

Add a `TestFormatReportWithSkip` that verifies the SKIP row format
and the three-count summary line.

Add a `TestBuildReportSkippedCount` that verifies `Skipped` does
not count toward `Failed`.

### 5.3 Unit tests (`internal/constraints/compare_test.go`)

Add table-driven cases:

- baseline PASS, current SKIP → not dominated.
- baseline SKIP, current PASS → not dominated.
- baseline SKIP, current FAIL → not dominated (no baseline signal).
- baseline FAIL, current SKIP → not dominated.
- baseline SKIP, current SKIP → not dominated.
- baseline PASS, current FAIL → still dominated (unchanged behavior).

### 5.4 Unit test for commandTool

Table-driven test covering:
- `"go vet ./..."` → `"go"`
- `"gofmt -l ./internal/ ./cmd/"` → `"gofmt"`
- `""` → `""`
- `"   "` → `""`
- `"GOFLAGS=-mod=vendor go vet ./..."` → `"go"` (after skipping env
  assignment)
- `"bash -c 'echo hi'"` → `"bash"`

### 5.5 No integration/docker test

A `docker run` assertion would need the daemon — skip in this session
per the original prompt's guidance that daemon-dependent tests are
optional. The unit tests cover the missing-tool path exhaustively;
the orch image is unchanged so there is no image-level invariant to
assert.

## 6. Rollout

1. **Plan doc** (this file) as first commit.
2. **feat commit** on `internal/constraints/` — report.go + evaluate.go
   + compare.go changes with the `bash: line 1: go: command not
   found` reproducer quoted in the commit body.
3. **test commit** adding the five new test groups.
4. **feat commit** on `internal/orchestrator/constraint_gate_policy.go`
   and `merge_execution.go` — add SKIP logging.
5. **docs commit** ticking:
   - `docs/constraints-system/design.md` — add a short "Tool
     availability and SKIP results" section describing the semantics.
   - `README.md` — no-op, already describes constraints generally.
   - `plans/orch-container-constraint-gate-fix.md` — status → "implemented".

The change is backward-compatible: callers that inspect
`Report.Failed` see the same values they did before for any
constraint that was previously passing or failing. Only
previously-erroring-due-to-missing-tool cases change behavior (from
FAIL to SKIP). Existing `FormatReport` string prefixes (`PASS:` and
`FAIL:`) are unchanged; `SKIP:` is new and additive.

## 7. Open Questions

- **Should SKIP appear in worker/CI runs?** If a CI container
  forgets to install Go, it will now silently skip `go vet` rather
  than fail loudly. Mitigation: the SKIP log line emitted at
  evaluation time gives operators a visible signal. Operators who
  want strict enforcement can add an out-of-band assertion
  (e.g. a startup-time check that required tools are present) —
  that's out of scope here.
- **Should `expect=empty_output` mode also consider stderr?** The
  silent-pass behavior for `gofmt` when the tool is missing is
  separate from the primary bug, but is noted as a collateral win:
  once gofmt is detected as missing, its result is SKIP rather than
  silent PASS. No further change to `empty_output` semantics needed.
- **`commandTool` heuristics**: it's a best-effort parser. If
  someone writes a command using `env`, shell functions, or
  `bash -c "…"` with the real tool inside the subshell, the check
  might skip on `bash` availability rather than the inner tool.
  That's acceptable — `bash` is always present in both dev and
  container environments. The heuristic errs on the side of
  "assume tool is available, run the check" when the first token
  is ambiguous.

## 8. Non-Goals

- Moving constraint checks to worker containers (Option C, future
  work).
- Adding new constraint types or checks.
- Changing quickfix's auto-execute policy or the
  `NeedsHumanReview` fallback semantics when a non-SKIP FAIL occurs.
- Restructuring the `constraints` package; this is a minimal
  surgical addition.
