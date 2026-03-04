"""Merge orchestration — coordinates multi-step merge processes.

Handles merging agent branches into features, features into main,
conflict detection, and post-merge sync across all active worktrees.

Merge workflow (3-tier):
1. Agent branches merge into their parent feature branch
2. Feature branch merges into main (after human approval)
3. All other feature branches rebase onto new main
"""

from __future__ import annotations

import logging
from dataclasses import dataclass
from pathlib import Path
from typing import Any
from uuid import UUID

from orchestrator.enums import TaskStatus
from orchestrator.git_utils import (
    CommitInfo,
    GitError,
    run_git,
    run_shell,
)
from orchestrator.messaging import MessageBus
from orchestrator.models import Task
from orchestrator.worktree import (
    FEATURE_PREFIX,
    MergeResult,
    SyncResult,
    WorktreeManager,
)

logger = logging.getLogger(__name__)


async def _is_worktree_clean(worktree: Path) -> bool:
    """Check if a worktree is clean, ignoring the .claude/ directory.

    Agent worktrees are nested under .claude/worktrees/ inside feature
    worktrees, which causes .claude/ to appear as untracked content.
    This is expected and should not prevent merges or rebases.
    """
    output = await run_git(
        ["status", "--porcelain"],
        cwd=worktree,
    )
    if not output:
        return True
    # Filter out .claude/ entries (untracked agent worktree directories)
    for line in output.splitlines():
        # Porcelain format: XY <path> or XY <path> -> <path>
        # The path starts at index 3
        path_part = line[3:].strip()
        if not path_part.startswith(".claude/") and path_part != ".claude/":
            return False
    return True


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


@dataclass
class MergeStatus:
    """Overview of merge state across the project."""

    features_ready: list[str]  # branches in MERGING state
    features_conflicted: list[str]  # branches with known conflicts
    features_behind: list[tuple[str, int]]  # (branch, commits_behind_main)
    main_branch: str
    main_head: str


class MergeOrchestrator:
    """Coordinates the multi-step merge process across worktrees.

    Orchestrates merging agent branches into features, features into main,
    conflict detection, build verification, and post-merge sync.
    """

    def __init__(
        self,
        worktree_manager: WorktreeManager,
        message_bus: MessageBus | None = None,
        db_session_factory: Any = None,
    ) -> None:
        self._wt = worktree_manager
        self._bus = message_bus
        self._db_session_factory = db_session_factory

    async def plan_agent_merge(
        self, agent_branch: str, feature_worktree: Path
    ) -> MergePlan:
        """Analyze what would happen if we merged an agent branch into the feature.

        1. Get the merge base (where agent diverged from feature)
        2. Get commits on agent branch since divergence
        3. Get files changed by agent since divergence
        4. Get files changed on feature since divergence
        5. Identify overlapping files as potential conflicts
        6. Return MergePlan (no side effects)
        """
        target_branch = await run_git(
            ["rev-parse", "--abbrev-ref", "HEAD"],
            cwd=feature_worktree,
        )

        # Find the merge base between agent and feature
        merge_base = await run_git(
            ["merge-base", agent_branch, target_branch],
            cwd=feature_worktree,
        )

        # Get commits on the agent branch since divergence
        # We need a temporary worktree-independent way to inspect the agent branch.
        # Use git log with explicit branch ref from the feature worktree.
        sep = "---COMMIT-SEP---"
        fmt = f"%H{sep}%h{sep}%an{sep}%ai{sep}%s"
        agent_log_output = await run_git(
            [
                "log",
                f"--format={fmt}",
                f"{merge_base}..{agent_branch}",
            ],
            cwd=feature_worktree,
        )

        commits_to_merge: list[CommitInfo] = []
        if agent_log_output:
            for line in agent_log_output.splitlines():
                parts = line.split(sep, maxsplit=4)
                if len(parts) != 5:
                    continue
                commits_to_merge.append(
                    CommitInfo(
                        sha=parts[0],
                        short_sha=parts[1],
                        author=parts[2],
                        date=parts[3],
                        message=parts[4],
                    )
                )

        # Files changed by the agent since divergence
        agent_files_output = await run_git(
            ["diff", "--name-only", f"{merge_base}..{agent_branch}"],
            cwd=feature_worktree,
        )
        agent_files = (
            agent_files_output.splitlines() if agent_files_output else []
        )

        # Files changed on the feature since divergence
        feature_files_output = await run_git(
            ["diff", "--name-only", f"{merge_base}..{target_branch}"],
            cwd=feature_worktree,
        )
        feature_files = (
            feature_files_output.splitlines() if feature_files_output else []
        )

        # Potential conflicts: files changed in both branches since divergence
        agent_set = set(agent_files)
        feature_set = set(feature_files)
        potential_conflicts = sorted(agent_set & feature_set)

        return MergePlan(
            source_branch=agent_branch,
            target_worktree=feature_worktree,
            target_branch=target_branch,
            commits_to_merge=commits_to_merge,
            files_changed=agent_files,
            potential_conflicts=potential_conflicts,
        )

    async def merge_agent_into_feature(
        self, agent_branch: str, feature_worktree: Path
    ) -> MergeResult:
        """Merge a single agent branch into the feature branch.

        1. Verify feature worktree is clean
        2. Execute git merge via WorktreeManager
        3. If conflict: merge is aborted by WorktreeManager, conflicts reported
        4. If success: return MergeResult with merge commit SHA
        """
        # Verify the feature worktree is clean before merging
        if not await _is_worktree_clean(feature_worktree):
            target_branch = await run_git(
                ["rev-parse", "--abbrev-ref", "HEAD"],
                cwd=feature_worktree,
            )
            return MergeResult(
                success=False,
                source_branch=agent_branch,
                target_branch=target_branch,
                merge_commit=None,
                conflicts=["<worktree has uncommitted changes>"],
            )

        # Delegate to WorktreeManager.merge_branch which handles
        # conflict detection and merge --abort
        result = await self._wt.merge_branch(agent_branch, feature_worktree)

        if result.success:
            logger.info(
                "Merged %s into feature at %s (commit %s)",
                agent_branch,
                feature_worktree,
                result.merge_commit,
            )
        else:
            logger.warning(
                "Merge conflict: %s into feature at %s — conflicts: %s",
                agent_branch,
                feature_worktree,
                result.conflicts,
            )

        return result

    async def merge_all_agents_into_feature(
        self, task: Task, feature_worktree: Path
    ) -> FeatureMergeReport:
        """Merge all completed agent branches into the feature.

        1. Get all subtasks that are DONE with agent branches
        2. Sort by completion time (earliest first)
        3. For each agent branch:
           a. Plan the merge (detect potential conflicts)
           b. Execute the merge
           c. If conflict: stop, report which agent conflicted
        4. After all merges: run build verification
        5. Return FeatureMergeReport
        """
        feature_branch = await run_git(
            ["rev-parse", "--abbrev-ref", "HEAD"],
            cwd=feature_worktree,
        )

        # Collect completed subtasks with agent branches, sorted by completion
        done_subtasks = [
            st
            for st in task.subtasks
            if st.status == TaskStatus.DONE
            and st.agent is not None
            and st.agent.worktree_branch
        ]
        done_subtasks.sort(
            key=lambda st: st.completed_at or st.agent.completed_at or st.id
        )

        agent_merges: list[MergeResult] = []
        all_files_changed: set[str] = set()
        total_commits = 0
        all_succeeded = True

        for subtask in done_subtasks:
            agent_branch = subtask.agent.worktree_branch

            # Plan first to gather info
            try:
                plan = await self.plan_agent_merge(
                    agent_branch, feature_worktree
                )
                total_commits += len(plan.commits_to_merge)
                all_files_changed.update(plan.files_changed)
            except GitError:
                # Branch may have been deleted or is unreachable
                logger.warning(
                    "Could not plan merge for %s — branch may not exist",
                    agent_branch,
                )
                agent_merges.append(
                    MergeResult(
                        success=False,
                        source_branch=agent_branch,
                        target_branch=feature_branch,
                        merge_commit=None,
                        conflicts=["<branch not found>"],
                    )
                )
                all_succeeded = False
                break

            # Execute the merge
            result = await self.merge_agent_into_feature(
                agent_branch, feature_worktree
            )
            agent_merges.append(result)

            if not result.success:
                all_succeeded = False
                break  # Stop on first conflict

        # Build verification (only if all merges succeeded)
        build_verified = False
        build_output: str | None = None
        if all_succeeded and agent_merges:
            build_verified, build_output = await self.verify_build(
                feature_worktree
            )

        return FeatureMergeReport(
            feature_branch=feature_branch,
            agent_merges=agent_merges,
            all_succeeded=all_succeeded,
            build_verified=build_verified,
            build_output=build_output,
            files_changed=sorted(all_files_changed),
            commit_count=total_commits,
        )

    async def merge_feature_into_main(self, task: Task) -> MergeResult:
        """Merge a feature branch into the main/master branch.

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
        default_branch = await self._wt.get_default_branch()
        main_worktree = self._wt.bare_repo / default_branch
        feature_branch = task.feature_branch

        # 1. Verify main is clean
        if not await _is_worktree_clean(main_worktree):
            return MergeResult(
                success=False,
                source_branch=feature_branch,
                target_branch=default_branch,
                merge_commit=None,
                conflicts=["<main worktree has uncommitted changes>"],
            )

        # 2. Pull latest main (if remote configured)
        try:
            await run_git(["pull", "--ff-only"], cwd=main_worktree)
        except GitError:
            # No remote configured or pull failed — continue with local state
            pass

        # 3 + 4. Execute git merge
        pre_merge_head = await run_git(
            ["rev-parse", "HEAD"], cwd=main_worktree
        )
        result = await self._wt.merge_branch(feature_branch, main_worktree)

        if not result.success:
            return result

        # 5a. Build verification on main
        build_ok, build_output = await self.verify_build(main_worktree)
        if not build_ok:
            # Revert the merge
            logger.warning(
                "Build failed after merging %s into %s, reverting",
                feature_branch,
                default_branch,
            )
            try:
                await run_git(
                    ["reset", "--hard", pre_merge_head],
                    cwd=main_worktree,
                )
            except GitError:
                pass

            return MergeResult(
                success=False,
                source_branch=feature_branch,
                target_branch=default_branch,
                merge_commit=None,
                conflicts=[f"<build failed: {build_output}>"],
            )

        # 5c. Sync all other features (rebase onto new main)
        await self.sync_features_after_merge(feature_branch)

        # 6. Clean up: remove feature worktree
        try:
            # Strip feature/ prefix for remove_feature
            feature_name = feature_branch.removeprefix(FEATURE_PREFIX)
            await self._wt.remove_feature(feature_name)
        except GitError as e:
            logger.warning("Failed to clean up feature worktree: %s", e)

        return result

    async def sync_features_after_merge(
        self, merged_feature: str
    ) -> list[SyncResult]:
        """After merging a feature into main, rebase all other active features.

        For each active feature worktree:
        1. Check if worktree is clean (agents must not be mid-work)
        2. If agents are working: skip, notify via message bus
        3. If clean: rebase onto main
        4. If rebase conflicts:
           a. Abort rebase
           b. Notify via message bus
        5. Return results
        """
        default_branch = await self._wt.get_default_branch()
        worktrees = await self._wt.list_worktrees()
        results: list[SyncResult] = []

        for wt in worktrees:
            if wt.is_bare:
                continue
            if not wt.branch.startswith(FEATURE_PREFIX):
                continue
            if wt.branch == merged_feature:
                continue  # Skip the feature we just merged

            # Check if worktree is clean
            if not await _is_worktree_clean(wt.path):
                logger.info(
                    "Skipping sync of %s — worktree is dirty", wt.branch
                )
                if self._bus:
                    await self._bus.publish(
                        "merge.sync_skipped",
                        {
                            "feature": wt.branch,
                            "reason": "worktree_dirty",
                            "merged_feature": merged_feature,
                        },
                    )
                results.append(
                    SyncResult(
                        feature=wt.branch,
                        success=False,
                        conflicts=["<worktree has uncommitted changes>"],
                    )
                )
                continue

            # Check if agents are actively working (agent_count > 0 means
            # agents exist, but we only skip if the worktree itself is dirty)
            # The cleanness check above covers this.

            # Attempt rebase onto default branch
            try:
                await run_git(
                    ["rebase", default_branch],
                    cwd=wt.path,
                )
                results.append(SyncResult(feature=wt.branch, success=True))
                logger.info(
                    "Synced %s onto %s", wt.branch, default_branch
                )
            except GitError:
                # Capture conflicts
                conflicts: list[str] = []
                try:
                    status_output = await run_git(
                        ["diff", "--name-only", "--diff-filter=U"],
                        cwd=wt.path,
                    )
                    if status_output:
                        conflicts = status_output.splitlines()
                except GitError:
                    pass

                # Abort the rebase
                try:
                    await run_git(["rebase", "--abort"], cwd=wt.path)
                except GitError:
                    pass

                results.append(
                    SyncResult(
                        feature=wt.branch,
                        success=False,
                        conflicts=conflicts,
                    )
                )

                if self._bus:
                    await self._bus.publish(
                        "merge.sync_conflict",
                        {
                            "feature": wt.branch,
                            "conflicts": conflicts,
                            "merged_feature": merged_feature,
                        },
                    )

                logger.warning(
                    "Rebase conflict syncing %s: %s",
                    wt.branch,
                    conflicts,
                )

        return results

    async def verify_build(self, worktree_path: Path) -> tuple[bool, str]:
        """Run build verification in a worktree.

        Looks for common build commands in order:
        1. pyproject.toml with [tool.pytest] -> uv run pytest
        2. Makefile with test target -> make test
        3. package.json with test script -> npm test

        Returns (success, output).
        """
        # Check for pyproject.toml with pytest config
        pyproject = worktree_path / "pyproject.toml"
        if pyproject.exists():
            content = pyproject.read_text()
            if "pytest" in content:
                try:
                    output = await run_shell(
                        "uv run pytest --tb=short -q 2>&1",
                        cwd=worktree_path,
                    )
                    return True, output
                except GitError as e:
                    return False, e.stderr

        # Check for Makefile
        makefile = worktree_path / "Makefile"
        if makefile.exists():
            content = makefile.read_text()
            if "test:" in content:
                try:
                    output = await run_shell(
                        "make test 2>&1",
                        cwd=worktree_path,
                    )
                    return True, output
                except GitError as e:
                    return False, e.stderr

        # Check for package.json
        packagejson = worktree_path / "package.json"
        if packagejson.exists():
            try:
                output = await run_shell(
                    "npm test 2>&1",
                    cwd=worktree_path,
                )
                return True, output
            except GitError as e:
                return False, e.stderr

        # No build tool found — pass by default
        return True, "no build tool detected"

    async def get_merge_status(self, project_id: UUID) -> MergeStatus:
        """Overview of merge state across the project.

        Reports:
        - Features ready to merge (in MERGING status)
        - Features with conflicts
        - Sync status of all feature branches (ahead/behind main)
        """
        default_branch = await self._wt.get_default_branch()
        main_worktree = self._wt.bare_repo / default_branch
        main_head = await run_git(["rev-parse", "HEAD"], cwd=main_worktree)

        worktrees = await self._wt.list_worktrees()

        features_behind: list[tuple[str, int]] = []
        features_conflicted: list[str] = []

        for wt in worktrees:
            if wt.is_bare or not wt.branch.startswith(FEATURE_PREFIX):
                continue

            status = await self._wt.get_branch_status(wt.path)
            if status.behind > 0:
                features_behind.append((wt.branch, status.behind))

        # features_ready and features_conflicted would normally be
        # populated from the database. For now, return empty lists
        # since we don't have DB access in this stub.
        return MergeStatus(
            features_ready=[],
            features_conflicted=features_conflicted,
            features_behind=features_behind,
            main_branch=default_branch,
            main_head=main_head,
        )
