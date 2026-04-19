# Agent: Merger Package and Container

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is Phase 2 work for the containerization initiative: build the merger package (clone integration branch, merge a feature branch, run tests, push result, delete feature branch) as a standalone binary that runs inside the merger container. The existing merge logic in `internal/merge/` and `internal/orchestrator/merge_execution.go` assumes host-side worktrees; the new merger performs every step in a container-local clone and talks to the bare repo over a bind mount.

## Context

Read these specs before starting:

- `docs/prd-containerization.md` (sections: "Merger"; "Lifecycle and recovery" → merger pool; user stories 9, 22, 23, 24, 46)
- `internal/merge/` (current merge logic — study `merge.go` for the merge algorithm and conflict handling; preserve the external behavior)
- `internal/orchestrator/merge_execution.go` (current dispatch pattern — understand the contract the merger fulfills so prompt 12 can swap call sites cleanly)
- `internal/worktree/git.go` (reference for git-command wrapping; the merger re-implements the narrow slice it needs without importing worktree)
- `internal/testutil/testutil_git.go` (`SetupBareRepo`, `CommitFile`)
- `internal/gitref/` (produced by prompt 03 — the registry the merger updates on success)
- `pkg/orchclient/` or `pkg/orchdto/` (produced by prompt 08 — how the merger reports results back)

## Dependencies

- Prompt 03 (`internal/gitref/`) — needed to mark branches merged/deleted. Stub the two methods `MarkMerged` and `MarkDeleted` if unavailable.
- Prompt 08 (`pkg/orchclient/`) — needed to POST structured merge-result events to the orchestrator's internal ingestion endpoint. If unavailable, stub a `type MergeReporter interface { Report(ctx, MergeResult) error }` and wire a no-op implementation for tests.

## Deliverables

### New files

#### 1. `internal/merger/merger.go`

- `type Merger struct { WorkDir string; BareRepo string; TestCmd string; Reporter MergeReporter; Registry GitrefRegistry }`
- `type MergeRequest struct { FeatureBranch string; IntegrationBranch string; Project string; TaskID string }`
- `type MergeResult struct { Success bool; Conflicts []string; TestsPassed bool; TestOutput string; MergedSHA string; StartedAt time.Time; FinishedAt time.Time }`
- `func (m *Merger) Merge(ctx context.Context, req MergeRequest) (*MergeResult, error)` — the end-to-end operation

Flow:

1. Clean `WorkDir`: `os.RemoveAll(workDir)`, recreate.
2. `git clone --branch <integration> <BareRepo> <WorkDir>`
3. `git fetch origin <feature>:<feature>`
4. `git merge --no-ff <feature>` — capture output; on conflict, collect the conflicted file list and set `MergeResult.Conflicts`
5. If merge succeeded, run `TestCmd` inside `WorkDir`; capture stdout + stderr into `TestOutput`; set `TestsPassed`
6. If tests passed, `git push origin <integration>`; record `MergedSHA`
7. If tests passed and push succeeded, `git push origin --delete <feature>` and call `Registry.MarkMerged` then `Registry.MarkDeleted`
8. Always call `Reporter.Report(ctx, result)` before returning (let Kyle learn about the merge attempt even on failure)

Retain the conflict-detection and test-gating behavior of `internal/merge/` — the merger is authoritative for "is this code fit to enter integration."

#### 2. `internal/merger/git.go`

Narrow git command wrappers used by `merger.go`: `clone`, `fetch`, `merge`, `push`, `pushDelete`, `conflictedFiles` (parse `git status --porcelain` for `UU`/`AA`/`DD` entries). All accept a context and a work directory; none touch global state.

#### 3. `internal/merger/interfaces.go`

- `type GitrefRegistry interface { MarkMerged(ctx context.Context, bareRepo, branch string) error; MarkDeleted(ctx context.Context, bareRepo, branch string) error }` — accepts a thin shim so the merger does not import `internal/gitref` directly (the shim is injected from `cmd/drem-merger/main.go`)
- `type MergeReporter interface { Report(ctx context.Context, project, taskID string, result MergeResult) error }`

#### 4. `cmd/drem-merger/main.go`

Binary entry point. Runs inside the merger container and consumes one merge request then exits (ephemeral invocation model from the PRD: "merger invocations are ephemeral; merger pool containers are warm and reset workspace between merges").

Flags:

- `--work-dir string` (default `/work`) — container-local clone directory
- `--bare-repo string` (default `/bare`) — read-write mount of the project's bare repo
- `--feature-branch string` (required)
- `--integration-branch string` (default `master`)
- `--project string` (required)
- `--task-id string` (required)
- `--test-cmd string` (required)
- `--orch-url string` (required) — for posting the result back via orchclient
- `--agentmon-token string` (required) — per-project shared token

Wires a `gitref.Registry` through the client library (or directly over `pkg/orchclient` if the orchestrator exposes a registry-mutation endpoint — the first cut reports via `/internal/logs` records of type `merge_result` and lets the orchestrator update gitref).

#### 5. `deploy/docker/merger.Dockerfile`

Multi-stage Go build. Runtime image needs `git` available for the merge operation. Use `debian:bookworm-slim` + `apt-get install -y --no-install-recommends git ca-certificates` as the runtime stage; the compiled `drem-merger` binary lives at `/usr/local/bin/drem-merger`.

Tag: `localhost:5000/drem-merger:latest`.

### Tests

#### 6. `internal/merger/merger_test.go`

Use `testutil.SetupBareRepo(t)` and `testutil.CommitFile(t, ...)` to construct a bare repo with an integration branch and a feature branch that diverges non-conflictingly. Wire an in-memory fake `Reporter` and fake `Registry`.

Cases:

- Happy path: non-conflicting feature branch merges, synthetic `TestCmd` (`"/bin/true"` or `sh -c 'exit 0'`) passes, `MergeResult.Success=true`, `MergeResult.MergedSHA` matches the integration HEAD after push, feature branch is deleted from the bare repo, `Registry.MarkMerged` and `Registry.MarkDeleted` each called once, `Reporter.Report` called once with success
- Merge conflict: create two branches that both modify the same line; `MergeResult.Success=false`, `Conflicts` non-empty, feature branch NOT deleted, registry not updated
- Test failure: non-conflicting merge but `TestCmd` exits non-zero (`sh -c 'exit 1'`); `MergeResult.TestsPassed=false`, feature branch NOT deleted, integration branch NOT pushed (merge is rolled back via `git reset --hard HEAD~1` — or never committed if tests run before the merge commit; your choice, document which)
- Idempotency: running the merger twice on the same feature branch where the first succeeded returns success without re-merging (detect by checking whether feature branch still exists on the remote)

#### 7. `internal/merger/git_test.go`

Unit tests for the git wrappers against a real temp clone.

## Migration

#### 8. `internal/merge/` and `internal/orchestrator/merge_execution.go`

Leave untouched in this prompt. The orchestrator still dispatches merges via `merge_execution.go`. Prompt 12 swaps the dispatch to spawn a merger container (via spawner RPC) instead of invoking in-process merge code. After prompt 12 lands, `internal/merge/` will have no callers left and can be removed — that removal belongs to a follow-up cleanup (not prompt 17, which targets tmux/worktree specifically).

## Scope Limitation

- The merger does not subscribe to tasks. It performs exactly one merge per invocation and exits. Scheduling is the orchestrator's job.
- The merger does not spawn other containers. It runs inside a container the spawner already created.
- The merger does not update SQLite directly. All state reporting goes through the orchestrator's `/internal/logs` endpoint.
- No retry logic on push failure beyond what `git push` itself does. If the bare repo is unreachable, report failure and let the orchestrator decide whether to retry.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- Package name: `merger`
- Binary name: `drem-merger`, output `bin/drem-merger`
- File-length, function-count, and import ceilings per `ARCHITECTURE.md`
- Use `exec.CommandContext` with a deadline on every git invocation; log stderr on non-zero exit
- Tests: `testify/require`, `testutil.SetupBareRepo`, `testutil.CommitFile`; no real Docker dependency in unit tests
- Build verification: `go build ./cmd/drem-merger/... ./internal/merger/... && go test ./internal/merger/...`
- Constitution check: `bash scripts/check_constitution.sh`
