# Agent: Worker Watchdog Binary

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is Phase 1 foundation work for the containerization initiative: build a small standalone Go binary (`drem-watchdog`) that gets baked into every worker container image and provides crash-recovery guarantees by auto-committing and pushing in-progress work to the bare repository.

## Context

Read these specs before starting:

- `docs/prd-containerization.md` (sections: "Lifecycle and recovery"; "Recovery strategy"; user stories 17, 18, 19)
- `internal/worktree/git.go` (study `CommitUnstagedChanges`, `IsClean`, `CleanClaudeArtifacts` for patterns — the watchdog applies the same philosophy but inside the container against its local clone)
- `ARCHITECTURE.md` (function-count and file-length ceilings)
- `internal/testutil/testutil_git.go` (`SetupBareRepo`, `CommitFile` — use for tests)

## Deliverables

### New files

#### 1. `cmd/drem-watchdog/main.go`

Binary entry point. Parses flags, constructs a `watchdog.Loop`, runs it until signaled.

Flags:

- `--repo string` (required) — path to the container-local working copy
- `--branch string` (required) — branch to push to
- `--remote string` (default `"origin"`) — push target; the bare repo is added as this remote by the worker at startup
- `--interval duration` (default `60s`) — periodic commit cadence when the tree is dirty
- `--test-cmd string` (optional) — when set, the watchdog executes this command and, on exit code 0, immediately commits and pushes
- `--test-interval duration` (default `5m`) — test command cadence; ignored if `--test-cmd` is empty
- `--agent-id string` (required) — emitted on heartbeat markers so agentmon (prompt 11) and extraction (prompt 02) can attribute them

Install signal handlers for `SIGTERM` and `SIGINT` — on receipt, run one final commit+push attempt with a short timeout and exit cleanly.

Emit a heartbeat line to stdout every interval tick in the exact format the extraction package recognises: `DREM-HEARTBEAT agent_id=<id> timestamp=<RFC3339>`.

#### 2. `internal/watchdog/loop.go`

The core loop, decoupled from flag parsing so it can be unit-tested.

- `type Loop struct { Repo string; Branch string; Remote string; Interval time.Duration; TestCmd string; TestInterval time.Duration; AgentID string; Clock func() time.Time; Out io.Writer }` — `Clock` and `Out` are injected for tests (default to `time.Now` and `os.Stdout`)
- `func (l *Loop) Run(ctx context.Context) error` — the top-level loop; returns when `ctx` is cancelled
- `func (l *Loop) tickCommit(ctx context.Context) error` — check dirty, commit if so with message `"[watchdog] wip <RFC3339>"`, then push
- `func (l *Loop) tickTest(ctx context.Context) error` — run `TestCmd` via `exec.CommandContext`; on exit 0, force an immediate commit+push with message `"[watchdog] tests-passing <RFC3339>"`; on non-zero exit, do nothing
- `func (l *Loop) heartbeat()` — write the heartbeat line to `Out`

Helpers for running `git` commands should live in a sibling file `internal/watchdog/git.go` so `loop.go` stays under the file-length ceiling. The git helpers are deliberately re-implemented here (simpler surface, container-local only) rather than imported from `internal/worktree/` — the watchdog must not depend on any other internal package except `internal/extract` if it needs the heartbeat constant.

If possible, expose the heartbeat constant from `internal/extract` (prompt 02 owns this) and import it here. If prompt 02 has not yet produced that constant, inline it as `const HeartbeatPrefix = "DREM-HEARTBEAT"` locally and leave a TODO referencing the extract package.

### Tests

#### 3. `internal/watchdog/loop_test.go`

Use `testutil.SetupBareRepo(t)` to create a bare repo. In each test, clone it into a temporary directory (the clone simulates the container-local working copy) and point `Loop.Repo` at the clone.

- `tickCommit` on a clean tree is a no-op (no new commits on the branch)
- `tickCommit` on a dirty tree creates exactly one commit with the expected subject prefix and pushes to the bare repo; a follow-up `tickCommit` with no further changes is a no-op
- `tickTest` with a test command that exits 0 commits and pushes even if the previous tick already pushed (the test-pass signal is authoritative)
- `tickTest` with a test command that exits non-zero does not commit
- `heartbeat` writes the expected prefix, agent ID, and an RFC3339 timestamp

#### 4. `internal/watchdog/git_test.go`

Cover the git helpers (`isDirty`, `commitAll`, `pushBranch`) against a real clone of a testutil bare repo.

## Scope Limitation

- The watchdog does not talk to the orchestrator HTTP API, does not emit structured events beyond the heartbeat line, and does not read the agentmon protocol. Its observability is entirely via stdout, which the Docker log driver captures and agentmon subscribes to (prompt 11).
- The watchdog does not track its own state across runs — if the container is recreated, the watchdog simply starts fresh against the already-pushed branch history.
- The watchdog has no knowledge of Claude, opencode, or any agent's process — it does not kill, restart, or monitor the agent. It only tends the working tree.
- No retry loops on git-push failure beyond a single retry per tick. If the bare repo is unreachable, log and let the next tick try again.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- Package name: `watchdog`
- Binary name: `drem-watchdog`, output path `bin/drem-watchdog`
- File-length and function-count ceilings per `ARCHITECTURE.md`
- Tests: `testify/require`; `testutil.SetupBareRepo` for every test that needs a repo
- Build verification: `go build -o bin/drem-watchdog ./cmd/drem-watchdog && go test ./internal/watchdog/...`
- Constitution check: `bash scripts/check_constitution.sh`
