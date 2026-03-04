# Agent: Merge Orchestration

You are working on **Drem Orchestrator**, a Python application that orchestrates Claude Code agents
working in parallel across git worktrees. Your task is to build the merge orchestration module that
handles merging agent branches into features, features into main, conflict detection, and post-merge
sync across all active worktrees.

## Context

Read these before starting:
- `CLAUDE.md` (project conventions)
- `src/orchestrator/worktree.py` (WorktreeManager — merge_branch, sync_all, MergeResult)
- `src/orchestrator/git_utils.py` (run_git, get_commit_log, get_changed_files, is_clean)
- `src/orchestrator/models.py` (Task, Agent — worktree_branch, status fields)
- `src/orchestrator/enums.py` (TaskStatus — MERGING, DONE, FAILED)
- `src/orchestrator/state_machine.py` (transition_task)
- `src/orchestrator/messaging.py` (MessageBus — for notifying agents of rebase needs)

The merge workflow follows the existing drem-canvas pattern:
1. Agent branches merge into feature branch
2. Feature branch merges into main (after human approval)
3. All other feature branches rebase onto new main

## Dependencies

This agent depends on Agent 03 (Worktree Integration) and Agent 05 (Agent Runner).
If those files don't exist yet, create stubs with the interfaces and implement against them.

## Deliverables

### New files (`src/orchestrator/`)

#### 1. `merge.py`

Merge orchestration — coordinates the multi-step merge process.

```python
@dataclass
class MergePlan:
    """Describes a planned merge operation before execution."""
    source_branch: str
    target_worktree: Path
    target_branch: str
    commits_to_merge: list[CommitInfo]
    files_changed: list[str]
    potential_conflicts: list[str]  # files changed in both source and target

@dataclass
class FeatureMergeReport:
    """Result of merging all agent branches into a feature."""
    feature_branch: str
    agent_merges: list[MergeResult]
    all_succeeded: bool
    build_verified: bool
    build_output: str | None
    files_changed: list[str]
    commit_count: int

class MergeOrchestrator:
    def __init__(
        self,
        worktree_manager: WorktreeManager,
        message_bus: MessageBus | None,
        db_session_factory,
    ):
        pass

    async def plan_agent_merge(
        self, agent_branch: str, feature_worktree: Path
    ) -> MergePlan:
        """
        Analyze what would happen if we merged an agent branch into the feature.

        1. Get commits on agent branch since it diverged from feature
        2. Get files changed by agent
        3. Get files changed on feature since divergence
        4. Identify overlapping files as potential conflicts
        5. Return MergePlan (no side effects)
        """

    async def merge_agent_into_feature(
        self, agent_branch: str, feature_worktree: Path
    ) -> MergeResult:
        """
        Merge a single agent branch into the feature branch.

        1. Verify feature worktree is clean
        2. Execute git merge
        3. If conflict:
           a. Capture conflict file list
           b. Abort the merge (git merge --abort)
           c. Return MergeResult(success=False, conflicts=[...])
        4. If success: return MergeResult with merge commit SHA
        """

    async def merge_all_agents_into_feature(
        self, task: Task, feature_worktree: Path
    ) -> FeatureMergeReport:
        """
        Merge all completed agent branches into the feature.

        1. Get all subtasks that are DONE with agent branches
        2. Sort by completion time (earliest first)
        3. For each agent branch:
           a. Plan the merge (detect potential conflicts)
           b. Execute the merge
           c. If conflict: stop, report which agent conflicted
        4. After all merges: run build verification
        5. Clean up merged agent worktrees
        6. Return FeatureMergeReport
        """

    async def merge_feature_into_main(self, task: Task) -> MergeResult:
        """
        Merge a feature branch into the main/master branch.
        Called after human passes manual testing.

        1. Verify main worktree is clean
        2. Pull latest main (if remote configured)
        3. Plan the merge (show what's coming)
        4. Execute git merge
        5. If conflict: abort, return failure
        6. If success:
           a. Run build verification on main
           b. If build fails: revert merge, return failure
           c. Sync all other features (rebase onto new main)
        7. Clean up: remove feature worktree
        8. Return MergeResult
        """

    async def sync_features_after_merge(
        self, merged_feature: str
    ) -> list[SyncResult]:
        """
        After merging a feature into main, rebase all other active features.

        For each active feature worktree:
        1. Check if worktree is clean (agents must not be mid-work)
        2. If agents are working: skip, notify via message bus
        3. If clean: rebase onto main
        4. If rebase conflicts:
           a. Abort rebase
           b. Create a "resolve conflicts" subtask on the feature's parent task
           c. Notify via message bus
        5. Return results
        """

    async def verify_build(self, worktree_path: Path) -> tuple[bool, str]:
        """
        Run build verification in a worktree.
        Executes the project's build command (from CLAUDE.md or project config).
        Returns (success, output).
        """

    async def get_merge_status(self, project_id: UUID) -> MergeStatus:
        """
        Overview of merge state across the project:
        - Features ready to merge (MERGING status)
        - Features with conflicts
        - Sync status of all feature branches (ahead/behind main)
        """

@dataclass
class MergeStatus:
    features_ready: list[str]           # branches in MERGING state
    features_conflicted: list[str]      # branches with known conflicts
    features_behind: list[tuple[str, int]]  # (branch, commits_behind_main)
    main_branch: str
    main_head: str
```

### Tests

#### 2. `tests/test_merge.py`

Tests using a temporary bare git repo with real worktrees (not mocks):

- `test_plan_agent_merge` — create agent commits, plan merge, verify commit list and file changes
- `test_merge_agent_clean` — agent branch merges cleanly, verify merge commit
- `test_merge_agent_conflict` — create conflicting changes, verify conflict detected and merge aborted
- `test_merge_all_agents` — three agents, all merge cleanly, verify FeatureMergeReport
- `test_merge_all_agents_partial_conflict` — two clean, one conflict, verify stops at conflict
- `test_merge_feature_into_main` — full lifecycle: create feature, merge to main, verify main has changes
- `test_sync_after_merge` — merge feature A into main, verify feature B rebased
- `test_sync_skips_dirty` — feature with uncommitted changes skipped during sync

Setup helper:
```python
async def create_test_repo(tmp_path: Path) -> tuple[Path, WorktreeManager]:
    """
    Create a bare repo with main worktree and sample commits.
    Returns (bare_repo_path, worktree_manager).
    """
```

## Build Verification

```bash
uv sync
uv run pytest tests/test_merge.py -v
```
