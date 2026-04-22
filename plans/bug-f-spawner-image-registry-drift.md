# Bug F — Spawner image-registry drift (one source of truth)

**Status**: plan doc. The immediate v15/v16 unblock landed inline in
`internal/spawner/images.go` with a drift-guard test. This plan covers
the architectural follow-up: collapse the two parallel registries into
one.

**Origin**: 2026-04-22. Discovered during dogfood of the 5-phase orch-API
gate-mutation pivot. After `drem cli approve 3ddba802` (v15) and
`4e1318b3` (v16) transitioned cleanly from `test_review` to
`in_progress` via the new HTTP path, the orchestrator's follow-on
fixer dispatch crashed with
`rpc SpawnWorker: code=-32000 no image mapping for agent_type="fixer"`,
surfacing as a `worker_spawn_failed` event in `task_events` and
cascading the parent task to `status=failed` (reason:
`"subtasks failed: Implement CanaryV{15,16}Marker struct"`). v17's
subtasks were already `done` pre-approve so it sidestepped the bug
and auto-progressed to `testing_ready` cleanly.

The HTTP gate path itself was not at fault — this was a pre-existing
spawner registry gap, unmasked by the first live end-to-end exercise
of the approve verb.

## Problem statement

The repo today maintains **two parallel agent-type → image registries**:

1. `internal/spawner/images.go:defaultImages` — consulted at the
   spawner RPC boundary (`SpawnWorker` → `resolveImage`) whenever the
   caller does not supply an explicit `Image` override. This is the
   authoritative registry per the file's own header comment.
2. `internal/agent/image_resolver.go:DefaultImages` — consulted by
   `ImageResolver.Resolve`, used by callers that want to pre-resolve
   an image before constructing `SpawnWorkerParams`.

The doc comment on (2) explicitly says:

> It is kept in sync with internal/spawner/images.go by convention:
> the spawner is the source of truth for the image table; agent code
> only needs a copy when callers ask the resolver to produce a concrete
> image name before handing the SpawnWorkerParams to the spawner.

**The convention broke.** Pre-fix state:

| Agent type | `internal/spawner` | `internal/agent` |
|---|---|---|
| coder-go | ✓ | ✓ |
| coder-cpp | ✓ | ✓ |
| g4 | ✓ | ✓ |
| merger | ✓ | ✓ |
| reviewer | ✗ | ✓ |
| fixer | ✗ | ✓ |
| supervisor | ✗ | ✓ |
| classifier | ✗ | ✓ |
| csuite-mike | ✓ | ✗ |
| csuite-alex | ✓ | ✗ |
| csuite-ross | ✓ | ✗ |
| csuite-seth | ✓ | ✗ |

Each table was incomplete from the other's perspective. The immediate
unblock (`fe5aced`) aligns the spawner side with the agent side for the
four fixer-class types but does **not** fix the underlying duplication.

## Goals

1. **One registry**, not two. Every caller that needs an agent-type →
   image mapping hits the same lookup path.
2. **Compile-time drift impossible.** Adding a new agent type must not
   compile in one file and break the other.
3. **Override semantics preserved.** The agent-side `ImageResolver`
   already supports per-project `Overrides` (from `drem.toml`) and a
   language-sensitive `coder-<language>` synthesis. Both behaviours
   must survive the merge.

## Non-goals

- Introducing a yaml/json-driven config file for image mappings.
  Built-in map is fine; operators already override via `drem.toml`.
- Changing the image tags or registry URL. Mapping content stays as-is.
- Merging `csuite-*` entries under a common "claude-persona" image.
  Each csuite image is a distinct container; lumping them is out of
  scope.

## Design options

### Option A — spawner becomes the only registry, agent imports it

`internal/agent/image_resolver.go:DefaultImages` goes away. The agent
package imports `internal/spawner` (or a new `internal/images`
subpackage) and the resolver reaches the same map.

- Pros: preserves the spawner's "source of truth" claim; minimal code
  move.
- Cons: creates an agent→spawner import edge that didn't exist before.
  Check constitution cap on `internal/agent`.

### Option B — extract a shared `internal/images` package

Both `internal/spawner` and `internal/agent` import
`internal/images.DefaultImages`. Neither owns the table.

- Pros: clean dep graph, no existing package gains an import.
- Cons: one more internal package (small one).

### Option C — leave both tables, add a cross-check test

A test in either package imports the other's table and asserts the
spawner table is a superset of the agent table (or vice versa). No
code move; only the drift guard strengthens.

- Pros: zero code-structure churn.
- Cons: two tables forever; a new agent type still touches two files;
  the doc-comment "source of truth" claim is still aspirational.

**Recommendation**: Option B. One small new package, zero import-graph
weirdness, the exact shape the constitution wants (narrow package,
single responsibility). The move is mechanical.

## Sequencing

1. **Tracer-bullet commit** — introduce `internal/images/default.go`
   with the union of both current tables plus a `Resolve` function
   that handles the coder-language synthesis. Unit-tested in isolation.
2. **Spawner migration** — `internal/spawner/images.go` deletes its
   local map, calls `images.Resolve(agentType, labels)`. Existing
   spawner tests continue to pass.
3. **Agent migration** — `internal/agent/image_resolver.go` keeps the
   `ImageResolver` struct (needed for the Overrides semantics) but
   delegates the default lookup to `images.Resolve`. Existing tests
   pass.
4. **Drift-guard test retires** — the test that caught this bug
   (`images_test.go:TestResolveImage_NonCoderAgentTypesMapped`) is
   no longer meaningful once there's only one map. Retire it or
   repoint it at the shared package.

Each step is a standalone commit with tests. No dispatch-worth; one
sitting, inline or a small worktree.

## Constitution notes

- `internal/images/` is a new package. Import-cap trivially satisfied
  (zero internal imports).
- `internal/spawner/` net line-count change: ~-10 (delete map, add
  one-line import + call).
- `internal/agent/` net line-count change: ~-15 (delete map, keep
  struct, simplify `Resolve`).

## Regression proof

A single test in `internal/images/` exhaustively enumerates the agent
types the orchestrator spawns (from `internal/model/enums.go:AgentType`
constants) and asserts each either resolves to an image or is
intentionally unmapped (`planner`, which is warm-container only).
That test is the permanent drift guard — the list of agent types the
orchestrator actually uses lives in `internal/model`, and the test
sources from there rather than from a hand-maintained list.

## Priority

Low-urgency. The immediate bug is closed. This plan exists to prevent
the next drift. Ships opportunistically — good worktree-subagent
starter task when cycles open up, or inline on a quiet day.

— kyle, 2026-04-22
