# Kill-List Audit — `internal/tmux` + `internal/worktree`

**Scope:** Phase 7 of `docs/prd-containerization.md` retires both packages. This doc enumerates every caller, the symbols used, an effort estimate for replacement, and migration risk flags for state-machine paths.

**Effort key:**

- **trivial** — import-only, test-helper that stubs `&worktree.Manager{BareRepoPath:...}`, or a single standalone helper call that maps 1:1 to a git RPC or to the new `internal/gitref/` package.
- **moderate** — a handful of call sites in one file, or use of `Manager` field plumbing that replaces with a thin client interface without logic changes.
- **architectural** — held as a struct field on the orchestrator / runner / merger; the caller's behaviour is defined by the filesystem/worktree model. Must be redesigned against spawner-RPC + bare-repo-only model.

**Risk flag 🔥** — caller lives on a hot state-machine path (task prep, scheduling, merge, completion, failure recovery, reconcile). Regressions here break the scheduler. Replacement must land with paired tests.

---

## 1. `internal/tmux` callers

Symbol surface used externally: `NewManager`, `Manager` (field), `IsAgentSessionAlive`, `FocusAgentSession`, `CreateShellSession`, `SessionName`.

| # | File:line | Package | Symbols used | Effort | 🔥 | Notes |
|---|---|---|---|---|---|---|
| 1 | `internal/agent/runner.go:26,94,137,243` | `agent` | `tmux.Manager` field; `NewRunner` param; `TmuxManager()` accessor | **architectural** | 🔥 | `Runner` is the agent-spawn hub. Every worker / planner / reviewer / supervisor goes through here. Replace with spawner-RPC client; `TmuxManager()` accessor gets removed outright. |
| 2 | `internal/tui/app.go:14,201` | `tui` | `tmuxpkg` import; `FocusAgentSession` | **moderate** | — | TUI is a presentation layer. `FocusAgentSession` becomes either a no-op (container logs via `docker logs`) or a `drem logs <container-id>` spawn. |
| 3 | `internal/tui/keyhandlers.go:178,180,209,211` | `tui` | `IsAgentSessionAlive`, `FocusAgentSession` | **moderate** | — | Same as above. Keybindings that "attach to agent pane" become "tail container logs". |
| 4 | `internal/tui/actions.go:371-376` | `tui` | `SessionName` field read; `CreateShellSession`, `FocusAgentSession` | **moderate** | — | "Open shell in worktree" action. Dies with worktrees — shells now need `docker exec -it <container>` targeting a worker, or a new "scratch container" feature. Flag to operator: this is a UX feature being dropped. |
| 5 | `internal/orchestrator/merge_reliability_test.go:16,65,219,313` | `orchestrator/tests` | `tmux.NewManager("test-...")` | **trivial** | — | Test fixture only. Replace with a no-op fake spawner client or drop. |
| 6 | `internal/orchestrator/agent_result_test.go:17,46` | `orchestrator/tests` | `tmux.NewManager("test-agent-result")` | **trivial** | — | Test fixture. |
| 7 | `internal/orchestrator/orchestrator_completion_test.go:12,37` | `orchestrator/tests` | `tmux.NewManager("test-completion")` | **trivial** | — | Test fixture. |

**Subtotal:** 7 caller files. 1 architectural (runner.go), 3 moderate (TUI), 3 trivial (test fixtures).

The only tmux call site that touches the state machine directly is `agent/runner.go`. Every other non-test caller is presentation-layer (TUI) — those migrations are cosmetic.

---

## 2. `internal/worktree` callers

Symbol surface used externally:

- **Package funcs (standalone):** `RunGit`, `GetChangedFiles`, `IsClean`, `CommitUnstagedChanges`, `UntrackEphemeralFiles`, `BranchHasNewCommits`, `GenerateRepoMapAsync`, `NewManager`.
- **`Manager` methods:** `FeatureWorktreePath`, `MainWorktreePath`, `CreateFeature`, `RemoveFeature`, `CreateAgentWorktree`, `RemoveAgentWorktree`, `RebaseBranchOnto`.
- **Types:** `Manager` (field + literal), `MergeResult`, `RebaseResult`, `WorktreeInfo`.

### 2a. Non-test callers (production code paths)

| # | File:line | Package | Symbols used | Effort | 🔥 | Notes |
|---|---|---|---|---|---|---|
| 1 | `internal/orchestrator/orchestrator.go:29,109,168` | `orchestrator` | `worktree.Manager` as struct field `Orchestrator.worktree` | **architectural** | 🔥 | Root plumbing. Every handler reaches `o.worktree.*`. Must become `o.gitref` (branch registry) + `o.spawner` (container RPC). |
| 2 | `internal/orchestrator/task_processing.go:11,152,197,203,213,329,332` | `orchestrator` | `o.worktree.RemoveAgentWorktree`, `CreateFeature`, `FeatureWorktreePath` ×2, `GenerateRepoMapAsync`, `GetChangedFiles` | **architectural** | 🔥 | Central scheduling path — feature creation, agent allocation, change detection. Most of this logic disappears: feature-worktree creation is replaced by branch registration; agent-worktree creation moves into the spawner. `GetChangedFiles` becomes `git diff` via spawner-RPC or bare-repo against branches. |
| 3 | `internal/orchestrator/task_prep.go:16` | `orchestrator` | `o.worktree.FeatureWorktreePath` for prep input paths | **architectural** | 🔥 | Prep writes `plan.json` / constraints into the feature worktree. Replace with an in-memory buffer or a per-project scratch volume mounted into the prep container. |
| 4 | `internal/orchestrator/session_spawning.go:17` | `orchestrator` | Feature path lookups for agent cwd | **architectural** | 🔥 | Agent cwd is set from worktree path. In the new model, cwd is `/workspace` inside the spawned container; the branch clone happens at container startup. |
| 5 | `internal/orchestrator/agent_failure.go:12,68,76,206,350,354` | `orchestrator` | `worktree.BranchHasNewCommits`, `o.worktree.RemoveAgentWorktree` ×2, `worktree.MergeResult`, `worktree.RunGit(...diff...)` | **architectural** | 🔥 | Failure recovery path. `RemoveAgentWorktree` → spawner `DestroyWorker`. `BranchHasNewCommits` + the diff shell-out become calls against the bare repo directly (no worktree required). `MergeResult` type re-homes to `internal/merger/`. |
| 6 | `internal/orchestrator/agent_results.go:16` | `orchestrator` | `o.worktree` field access | **architectural** | 🔥 | Reads post-agent state. Same pattern — becomes branch-state queries + spawner inspect. |
| 7 | `internal/orchestrator/reconcile.go:12` | `orchestrator` | `o.worktree` field access | **architectural** | 🔥 | Reconcile walks orchestrator state against reality. Today "reality" = worktrees + tmux panes; becomes "containers + branches". Rewrite against `ListWorkers` RPC + bare-repo branch list. |
| 8 | `internal/orchestrator/reconcile_parents.go:12,28,39,93,200` | `orchestrator` | `o.worktree.MainWorktreePath`, `worktree.RunGit` ×2, `o.worktree.RemoveFeature` | **architectural** | 🔥 | Parent-branch reconciliation. Main worktree concept dies; git operations run against the bare repo directly (`git --git-dir=<bare> log/diff ...`). `RemoveFeature` → spawner `DestroyWorker` + branch delete on bare. |
| 9 | `internal/orchestrator/handlers.go:16,173,183` | `orchestrator` | `o.worktree.FeatureWorktreePath`, `worktree.UntrackEphemeralFiles` | **moderate** | 🔥 | HTTP/CLI handler touches feature worktree for "untrack ephemeral files" hack. Behaviour is file-cleanup — moves into merger container pre-merge step. |
| 10 | `internal/orchestrator/cli.go:8` | `orchestrator` | Import-only for `cli` wiring | **trivial** | — | Passes through `*worktree.Manager` into the orchestrator constructor. Deleted when the field is deleted. |
| 11 | `internal/orchestrator/test_execution.go:19` | `orchestrator` | Feature worktree path for `go test` cwd | **architectural** | 🔥 | Test-execution cwd. Replaced by test command executing inside worker/merger container's `/workspace`. |
| 12 | `internal/orchestrator/test_writing.go:12` | `orchestrator` | Feature path lookups | **architectural** | 🔥 | Test-writing state. Same migration as `task_prep.go`. |
| 13 | `internal/orchestrator/quickfix_processing.go:10` | `orchestrator` | Feature path lookups | **moderate** | 🔥 | Quickfix path allocates worktrees. Replaced by ephemeral fixer container. |
| 14 | `internal/orchestrator/context_monitor.go:11` | `orchestrator` | Field access for context-fixer worktree allocation | **moderate** | 🔥 | Supervisor loop. Container-resident — fixer spawns become spawner-RPC calls. |
| 15 | `internal/orchestrator/direct_tool_dispatch.go:28` | `orchestrator` | Field access for direct-tool agent cwd | **moderate** | — | Direct-tool agents (G4) need a cwd. Post-migration they run in worker containers. |
| 16 | `internal/agent/runner.go:27,96,137,284,290` | `agent` | `worktree.Manager` field; `NewRunner` param; `CreateAgentWorktree`; `GenerateRepoMapAsync` | **architectural** | 🔥 | Pairs with tmux caller #1. The entire `SpawnAgent` path becomes: spawner-RPC `SpawnWorker(project, agent_type, worker_id, branch, labels)`. `GenerateRepoMapAsync` moves into the worker image startup (or lives as a per-project side-car). |
| 17 | `internal/merge/merge.go:18` | `merge` | `worktree.Manager` + `MergeResult` | **architectural** | 🔥 | Merger package is being re-homed into a warm container pool (`drem-merger`). Old in-process merge logic dies; merger RPC takes over. |
| 18 | `internal/merge/queue.go:7,13,14,18,22,43,58,80,97` | `merge` | Interface types `MergeResult`, `RebaseResult`; `MergeFeatureIntoMain`, `MergeAgentIntoFeature`, `RebaseBranchOnto` method signatures | **architectural** | 🔥 | Whole queue abstraction transplants onto the merger RPC. The `worktree.Manager`-satisfies-interface trick goes away. |

**Non-test subtotal:** 18 files. 12 architectural + 5 moderate + 1 trivial. **Every architectural caller is on the hot state-machine path** — prep → scheduling → spawn → test → merge → failure → reconcile.

### 2b. Test callers

| # | File:line pattern | Count | Effort |
|---|---|---|---|
| `test_writing_test.go` | 29 sites using `&worktree.Manager{BareRepoPath:"/tmp/fake", DefaultBranch:"main"}` | 29 | **trivial** — stub replaces with `gitref.NewForTest(...)`. |
| `orchestrator/*_test.go` (classifying, bugreport, bugreport_classify, classifier_dispatch, constraint_gate_delta, constraint_gate_wiring, coverage_gap, dedup_scheduling, direct_plan_reviewer, direct_tool_dispatch, dispatch_stall, eventbus_integration, failure_recovery, integration_gate, lifecycle, merge_execution, merge_reliability, orchestrator, orchestrator_completion, planner_spawn_control, post_agent_constraint, quickfix, reconcile, scheduling, task_prep, test_gate, test_review) | one import each, plus a handful using `worktree.NewManager(...)` with a real bare repo | ~28 files | **trivial** each — most are `&worktree.Manager{BareRepoPath, DefaultBranch}` literals that become `gitref.Manager` literals. |
| `internal/merge/merge_test.go`, `internal/merge/queue_test.go` | Integration tests of merge queue | 2 | **moderate** — follow the merger RPC rewrite. |

**Test subtotal:** ~30 files, predominantly trivial rewrites tied to a `gitref` type rename + constructor swap. One integration suite (`internal/merge/*_test.go`) reshapes with the merger container tests.

---

## 3. Risk summary

**Every `internal/worktree` non-test caller is on the state-machine hot path.** There is no "leaf" worktree caller in production code: the orchestrator struct holds it as a field, and 12 of 18 files reach into it for feature/agent lifecycle operations.

**Migration risks to flag to Kyle before Phase 7:**

1. 🔥 **`agent/runner.go` is the single widest blast radius.** Both tmux and worktree coupling. This file alone defines how the orchestrator thinks about "spawning". Phase 2 of the PRD (spawner service + worker images) must land the replacement RPC surface before `runner.go` can be rewritten.
2. 🔥 **`reconcile.go` + `reconcile_parents.go` encode the "what is the world" logic.** Rebuilding these against container + branch state is the biggest untested behavioural rewrite. Recommend exhaustive integration tests in Phase 4 (Kyle / per-project compose) that specifically compare pre-/post-migration reconcile outputs against identical git state.
3. 🔥 **`merge/queue.go` and `merge/merge.go` own the merge gate.** The merger container in Phase 6 replaces these — but the queue abstraction (priority, concurrency, retry) must be preserved. Risk: re-implementing queue semantics inside the merger RPC loses the current scheduling guarantees.
4. 🔥 **`failure_recovery` + `agent_failure.go` use `RunGit` directly for diff extraction.** The `worktree.RunGit(args, cwd)` helper has no container equivalent yet. Replacement: either a bare-repo git-cli shell-out (no worktree needed for `git log`/`git diff`) or a new `gitref.RunGitInBare(args)` primitive.
5. ⚠️ **TUI regressions are invisible to the state machine but visible to the operator.** The "attach to agent pane" and "open shell in worktree" features (tui/actions.go, tui/keyhandlers.go) have no direct container analogue. Either build `drem logs <worker>` + `drem shell <worker>` commands (tailing `docker logs` / spawning `docker exec`) or acknowledge the feature regression.

**Recommended sequencing:**

1. Land `internal/gitref/` (branch registry, no fs ops) and replace every test-helper literal with `gitref.Manager` stubs first (trivial, decouples test churn from production migration).
2. Introduce spawner-RPC client in `agent/runner.go` behind a feature flag; keep worktree path alive until spawner is proven.
3. Rewrite reconcile against `ListWorkers` + bare-repo branch list. Parallel-run both reconcilers and diff their outputs for a week before cutting over.
4. Move merge logic into merger container image; flip `merge/queue.go` to RPC client.
5. Delete `internal/tmux/` and `internal/worktree/` in one commit once all production imports are gone.

---

*Generated by Seth (CTO) 2026-04-18 under Kyle pivot directive. Paired deliverables: `plans/host-state-inventory.md`, `infra/docker/*.Dockerfile`, root `docker-compose.yml`.*
