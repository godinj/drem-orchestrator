# Architecture Constitution

Concrete, falsifiable rules to prevent specific categories of codebase decay.
These are not principles or aspirations — each rule can be verified with a grep,
a line count, or a single targeted question.

**Origin:** Every rule here addresses a problem documented in
`~/Documents/drem-canvas-docs/reference/drem-orchestrator-code-quality-report.md`.
New rules are added only when a new category of decay is observed. Rules are
removed when hook enforcement makes them redundant (see Graduation Path below).

**Enforcement status:**
- `[enforced]` — a hook or CI check blocks violations
- `[not yet enforced]` — manual compliance only; infrastructure pending

---

## Structural Limits

### File length ceiling: 800 lines `[not yet enforced]`

No `.go` source file (non-test) may exceed 800 lines. If adding code would
breach this limit, extract into a separate file in the same package first.

`orchestrator.go` (4,567 lines) is grandfathered but must shrink, not grow.
Any change to a grandfathered file that increases its line count is a violation.

**Compliance test:** `wc -l` on the changed file; must be <= 800 (or <= previous
count for grandfathered files).

### Function count ceiling: 20 exported functions per file `[not yet enforced]`

No single file may define more than 20 exported functions or methods. If
exceeded, split into a focused file within the same package.

`orchestrator.go` (84 functions) is grandfathered under the same shrink-only
rule.

**Compliance test:** `grep -c '^func ' file.go`; must be <= 20 (or <= previous
count for grandfathered files).

### Package import ceiling: 6 internal imports `[not yet enforced]`

No package may import more than 6 other `internal/` packages. If exceeded,
the package is accumulating too many responsibilities — extract a sub-concern
into its own package or push logic down to dependencies.

`orchestrator` (8 internal imports) is grandfathered but must not add more.

**Compliance test:** Count `internal/` imports in a package's source files;
must be <= 6 (or <= previous count for grandfathered packages).

---

## Formatting

### gofmt compliance: 100% `[not yet enforced]`

All `.go` files must pass `gofmt -l` with no output. Do not commit
unformatted code.

**Compliance test:** `gofmt -l ./internal/ ./cmd/` returns no results.

**Current violations:** 11 files have formatting drift.

---

## Duplication

### Search before creating `[not yet enforced]`

Before writing any helper function in a test file, check `internal/testutil/`
for an existing implementation. If one exists, import it rather than creating
a local copy.

**Compliance test:** No two test files contain the same helper function body
(e.g., `setupBareRepo`, `addWorktree`, `commitFile`, `newTestDB`).

### Three-copy threshold `[not yet enforced]`

If the same pattern (function body, struct layout, boilerplate block) exists in
3+ locations, extract it before adding a 4th. The extraction must happen in the
same commit as the new usage.

**Compliance test:** `grep` for the pattern across the codebase; count must stay
below 3.

### testutil is the single source for test infrastructure `[not yet enforced]`

All test database creation must use `testutil.NewTestDB` or
`testutil.NewSharedTestDB`. All git repo setup must use `testutil.SetupBareRepo`,
`testutil.AddWorktree`, `testutil.CommitFile`. Do not define local versions of
these functions in test files.

**Compliance test:**
```bash
# DB init outside testutil
grep -rn 'gorm.Open(sqlite' internal/ --include='*_test.go' | grep -v testutil/
# Git helpers outside testutil
grep -rn 'func setupBareRepo\|func initBareRepo\|func addWorktree\|func commitFile' \
  internal/ --include='*_test.go' | grep -v testutil/
```
Both must return no results.

**Current violations:** `merge/merge_test.go` (3 duplicate git helpers),
`orchestrator/orchestrator_test.go` (2 duplicate git helpers),
`orchestrator/lifecycle_test.go` (1 duplicate DB init),
`agent/runner_mock_test.go` (1 duplicate DB init).

---

## Interfaces & Coupling

### Interfaces at consumption sites `[not yet enforced]`

When a package depends on a collaborator that it needs to mock or swap in tests,
define an interface in the consuming package, not the providing package. Do not
depend on concrete types from other packages when an interface would suffice.

**Compliance test:** Count interfaces in the codebase; any new external
dependency (runner, manager, supervisor) should have a corresponding interface
at the consumption site.

**Current state:** Only 2 interfaces exist (`SessionManager`,
`mergeWorktreeClient`). Orchestrator depends on 8 concrete internal types.

---

## Constants & Magic Numbers

### No bare numeric literals in business logic `[not yet enforced]`

Thresholds, timeouts, retry counts, and percentage values must be defined as
named constants with a comment explaining the choice. Do not use bare numeric
literals for these values.

**Compliance test:** `grep` for bare numbers used as timeouts, retry limits, or
thresholds in non-test `.go` files; new occurrences must use named constants.

**Current violations:** `85` (context fixer threshold), `50` (memory limit),
various `time.Duration` literals, retry count `3` in multiple places.

---

## Models

### No duplicate GORM hooks `[not yet enforced]`

GORM lifecycle hooks (BeforeCreate, BeforeUpdate, etc.) that share identical
logic across model types must be consolidated. Use either a shared embedded
struct with a single hook or a GORM callback registered once at DB init time.

**Compliance test:** `grep -c 'func.*BeforeCreate' internal/model/models.go`;
should be 1 (shared) not 6 (per-type).

**Current violations:** 6 identical `BeforeCreate` UUID generation hooks on
Project, Task, Agent, TaskEvent, Memory, TaskComment.

---

## Test Infrastructure

### Test factory functions in testutil `[not yet enforced]`

Common test entity creation (projects, tasks, agents) must use shared factory
functions from `internal/testutil/`. Do not define `createTestProject`,
`createTestTask`, or equivalent functions in individual test files.

**Compliance test:**
```bash
grep -rn 'func createTest\|func newTest' internal/ --include='*_test.go' | grep -v testutil/
```
Must return no results.

### Minimize real I/O in unit tests `[not yet enforced]`

Tests that only assert on database state should not create real git worktrees.
Reserve git worktree setup for integration tests that exercise actual git
operations.

**Compliance test:** Manual review — if a test's assertions only check DB
records, it should not call `SetupBareRepo` or `AddWorktree`.

---

## Graduation Path

When a constitution rule can be reliably detected by a script:

1. Add the check to `scripts/check_constitution.sh`
2. Mark the rule in this document as `[enforced]`
3. The rule stays in the document for context but the hook is now authoritative
