"""Tests for merge.py — merge orchestration.

Uses real temporary bare git repos with real worktrees (not mocks).
Each test creates an isolated repo, exercises the merge orchestrator,
and verifies the results against actual git state.
"""

from __future__ import annotations

from datetime import datetime
from pathlib import Path

import pytest

from types import SimpleNamespace

from orchestrator.enums import AgentStatus, TaskStatus  # noqa: F401
from orchestrator.git_utils import run_git
from orchestrator.merge import (
    FeatureMergeReport,
    MergeOrchestrator,
    MergePlan,
)
from orchestrator.messaging import MessageBus
from orchestrator.worktree import WorktreeManager


def _mock_task(**kwargs):
    """Create a mock task with defaults for merge tests."""
    defaults = dict(
        id="test-task",
        title="test",
        status=TaskStatus.DONE,
        worktree_branch=None,
        subtasks=[],
        assigned_agent=None,
        updated_at=None,
    )
    defaults.update(kwargs)
    return SimpleNamespace(**defaults)


def _mock_agent(**kwargs):
    """Create a mock agent with defaults for merge tests."""
    defaults = dict(
        id="test-agent",
        worktree_branch=None,
        status=AgentStatus.IDLE,
    )
    defaults.update(kwargs)
    return SimpleNamespace(**defaults)


async def create_test_repo(tmp_path: Path) -> tuple[Path, WorktreeManager]:
    """Create a bare repo with main worktree and sample commits.

    Returns (bare_repo_path, worktree_manager).
    """
    bare = tmp_path / "test-project.git"
    bare.mkdir()

    # Init bare repo
    await run_git(["init", "--bare", str(bare)])
    await run_git(["symbolic-ref", "HEAD", "refs/heads/main"], cwd=bare)

    # Create the main worktree with an initial commit
    main_dir = bare / "main"
    await run_git(
        ["worktree", "add", "--orphan", "-b", "main", str(main_dir)],
        cwd=bare,
    )
    await run_git(["config", "user.email", "test@test.com"], cwd=main_dir)
    await run_git(["config", "user.name", "Test"], cwd=main_dir)

    (main_dir / "README.md").write_text("# Test Project\n")
    await run_git(["add", "README.md"], cwd=main_dir)
    await run_git(["commit", "-m", "initial commit"], cwd=main_dir)

    manager = WorktreeManager(bare_repo=bare)
    return bare, manager


async def _make_agent_commits(
    manager: WorktreeManager,
    feature_name: str,
    files: dict[str, str],
    commit_msg: str = "agent work",
) -> str:
    """Create an agent worktree, add files, commit, return branch name.

    Args:
        manager: The WorktreeManager.
        feature_name: Feature to create the agent inside.
        files: Mapping of filename -> content to create and commit.
        commit_msg: Commit message.

    Returns:
        The agent branch name.
    """
    agent = await manager.create_agent_worktree(feature_name)

    # Set git config for agent worktree
    await run_git(
        ["config", "user.email", "agent@test.com"], cwd=agent.path
    )
    await run_git(["config", "user.name", "Agent"], cwd=agent.path)

    for filename, content in files.items():
        filepath = agent.path / filename
        filepath.parent.mkdir(parents=True, exist_ok=True)
        filepath.write_text(content)
        await run_git(["add", filename], cwd=agent.path)

    await run_git(["commit", "-m", commit_msg], cwd=agent.path)
    return agent.branch


@pytest.fixture
async def test_env(tmp_path: Path) -> tuple[Path, WorktreeManager, MergeOrchestrator]:
    """Fixture providing a bare repo, manager, and merge orchestrator."""
    bare, manager = await create_test_repo(tmp_path)
    bus = MessageBus(redis_url="redis://localhost:6379")
    orchestrator = MergeOrchestrator(
        worktree_manager=manager,
        message_bus=bus,
        db_session_factory=None,
    )
    return bare, manager, orchestrator


class TestPlanAgentMerge:
    async def test_plan_agent_merge(self, test_env) -> None:
        """Create agent commits, plan merge, verify commit list and file changes."""
        bare, manager, orchestrator = test_env
        feature = await manager.create_feature("plan-test")

        # Create an agent with some commits
        agent_branch = await _make_agent_commits(
            manager,
            "plan-test",
            {"src/module.py": "print('hello')\n", "src/util.py": "# utils\n"},
            commit_msg="add modules",
        )

        # Plan the merge
        plan = await orchestrator.plan_agent_merge(agent_branch, feature.path)

        assert isinstance(plan, MergePlan)
        assert plan.source_branch == agent_branch
        assert plan.target_branch == "feature/plan-test"
        assert plan.target_worktree == feature.path

        # Should have 1 commit
        assert len(plan.commits_to_merge) == 1
        assert plan.commits_to_merge[0].message == "add modules"

        # Should list the changed files
        assert "src/module.py" in plan.files_changed
        assert "src/util.py" in plan.files_changed

        # No conflicts since feature has no diverging changes
        assert plan.potential_conflicts == []

    async def test_plan_detects_potential_conflicts(self, test_env) -> None:
        """Plan should identify files changed in both branches."""
        bare, manager, orchestrator = test_env
        feature = await manager.create_feature("plan-conflict")

        # Set git config on feature worktree
        await run_git(
            ["config", "user.email", "test@test.com"], cwd=feature.path
        )
        await run_git(
            ["config", "user.name", "Test"], cwd=feature.path
        )

        # Create agent that modifies README.md
        agent_branch = await _make_agent_commits(
            manager,
            "plan-conflict",
            {"README.md": "agent version\n"},
            commit_msg="agent edits readme",
        )

        # Also modify README.md on the feature branch
        (feature.path / "README.md").write_text("feature version\n")
        await run_git(["add", "README.md"], cwd=feature.path)
        await run_git(
            ["commit", "-m", "feature edits readme"], cwd=feature.path
        )

        plan = await orchestrator.plan_agent_merge(agent_branch, feature.path)

        assert "README.md" in plan.potential_conflicts


class TestMergeAgentClean:
    async def test_merge_agent_clean(self, test_env) -> None:
        """Agent branch merges cleanly, verify merge commit."""
        bare, manager, orchestrator = test_env
        feature = await manager.create_feature("merge-clean")

        agent_branch = await _make_agent_commits(
            manager,
            "merge-clean",
            {"new_file.py": "# new file\n"},
            commit_msg="add new file",
        )

        result = await orchestrator.merge_agent_into_feature(
            agent_branch, feature.path
        )

        assert result.success is True
        assert result.source_branch == agent_branch
        assert result.target_branch == "feature/merge-clean"
        assert result.merge_commit is not None
        assert len(result.merge_commit) == 40
        assert result.conflicts == []

        # Verify the file is now in the feature worktree
        assert (feature.path / "new_file.py").exists()
        assert (feature.path / "new_file.py").read_text() == "# new file\n"


class TestMergeAgentConflict:
    async def test_merge_agent_conflict(self, test_env) -> None:
        """Create conflicting changes, verify conflict detected and merge aborted."""
        bare, manager, orchestrator = test_env
        feature = await manager.create_feature("merge-conflict")

        # Set git config on feature worktree
        await run_git(
            ["config", "user.email", "test@test.com"], cwd=feature.path
        )
        await run_git(
            ["config", "user.name", "Test"], cwd=feature.path
        )

        # Create agent that modifies README.md
        agent_branch = await _make_agent_commits(
            manager,
            "merge-conflict",
            {"README.md": "agent version of readme\n"},
            commit_msg="agent changes readme",
        )

        # Modify README.md on feature branch (diverge)
        (feature.path / "README.md").write_text("feature version of readme\n")
        await run_git(["add", "README.md"], cwd=feature.path)
        await run_git(
            ["commit", "-m", "feature changes readme"], cwd=feature.path
        )

        result = await orchestrator.merge_agent_into_feature(
            agent_branch, feature.path
        )

        assert result.success is False
        assert result.merge_commit is None
        assert "README.md" in result.conflicts

        # Verify feature worktree is clean after abort (ignoring .claude/
        # which is the agent worktree directory and expected to be untracked)
        status = await run_git(["status", "--porcelain"], cwd=feature.path)
        non_claude_lines = [
            line for line in status.splitlines()
            if not line.strip().startswith("?? .claude/")
        ]
        assert non_claude_lines == []

    async def test_merge_rejects_dirty_worktree(self, test_env) -> None:
        """Merging into a dirty worktree should fail immediately."""
        bare, manager, orchestrator = test_env
        feature = await manager.create_feature("merge-dirty")

        agent_branch = await _make_agent_commits(
            manager,
            "merge-dirty",
            {"new.py": "code\n"},
            commit_msg="agent work",
        )

        # Make feature worktree dirty
        (feature.path / "uncommitted.txt").write_text("dirty\n")

        result = await orchestrator.merge_agent_into_feature(
            agent_branch, feature.path
        )

        assert result.success is False
        assert "<worktree has uncommitted changes>" in result.conflicts


class TestMergeAllAgents:
    async def test_merge_all_agents(self, test_env) -> None:
        """Three agents, all merge cleanly, verify FeatureMergeReport."""
        bare, manager, orchestrator = test_env
        feature = await manager.create_feature("all-agents")

        now = datetime.now()

        # Create three agents with different files
        branch_a = await _make_agent_commits(
            manager, "all-agents", {"a.py": "# a\n"}, "agent a work"
        )
        branch_b = await _make_agent_commits(
            manager, "all-agents", {"b.py": "# b\n"}, "agent b work"
        )
        branch_c = await _make_agent_commits(
            manager, "all-agents", {"c.py": "# c\n"}, "agent c work"
        )

        # Build a Task with subtasks for each agent
        task = _mock_task(
            title="all-agents task",
            status=TaskStatus.MERGING,
            worktree_branch="feature/all-agents",
            subtasks=[
                _mock_task(
                    title="subtask a",
                    status=TaskStatus.DONE,
                    assigned_agent=_mock_agent(worktree_branch=branch_a),
                    updated_at=now,
                ),
                _mock_task(
                    title="subtask b",
                    status=TaskStatus.DONE,
                    assigned_agent=_mock_agent(worktree_branch=branch_b),
                    updated_at=now,
                ),
                _mock_task(
                    title="subtask c",
                    status=TaskStatus.DONE,
                    assigned_agent=_mock_agent(worktree_branch=branch_c),
                    updated_at=now,
                ),
            ],
        )

        report = await orchestrator.merge_all_agents_into_feature(
            task, feature.path
        )

        assert isinstance(report, FeatureMergeReport)
        assert report.feature_branch == "feature/all-agents"
        assert report.all_succeeded is True
        assert len(report.agent_merges) == 3
        assert all(m.success for m in report.agent_merges)
        assert report.commit_count == 3  # 1 commit per agent

        # All files should be present in feature
        assert (feature.path / "a.py").exists()
        assert (feature.path / "b.py").exists()
        assert (feature.path / "c.py").exists()

        # files_changed should list all changed files
        assert "a.py" in report.files_changed
        assert "b.py" in report.files_changed
        assert "c.py" in report.files_changed

    async def test_merge_all_agents_partial_conflict(self, test_env) -> None:
        """Two clean, one conflict, verify stops at conflict."""
        bare, manager, orchestrator = test_env
        feature = await manager.create_feature("partial-conflict")

        # Set git config on feature
        await run_git(
            ["config", "user.email", "test@test.com"], cwd=feature.path
        )
        await run_git(
            ["config", "user.name", "Test"], cwd=feature.path
        )

        now = datetime.now()

        # Agent A: clean file
        branch_a = await _make_agent_commits(
            manager, "partial-conflict", {"a.py": "# a\n"}, "agent a"
        )

        # Agent B: will conflict with feature's README.md change
        branch_b = await _make_agent_commits(
            manager,
            "partial-conflict",
            {"README.md": "agent B version\n"},
            "agent b conflicts",
        )

        # Agent C: clean file (should never be attempted)
        branch_c = await _make_agent_commits(
            manager, "partial-conflict", {"c.py": "# c\n"}, "agent c"
        )

        # Modify README.md on feature to create conflict with agent B
        (feature.path / "README.md").write_text("feature version\n")
        await run_git(["add", "README.md"], cwd=feature.path)
        await run_git(
            ["commit", "-m", "feature changes readme"], cwd=feature.path
        )

        task = _mock_task(
            title="partial conflict task",
            status=TaskStatus.MERGING,
            worktree_branch="feature/partial-conflict",
            subtasks=[
                _mock_task(
                    title="a",
                    status=TaskStatus.DONE,
                    assigned_agent=_mock_agent(worktree_branch=branch_a),
                    updated_at=now,
                ),
                _mock_task(
                    title="b",
                    status=TaskStatus.DONE,
                    assigned_agent=_mock_agent(worktree_branch=branch_b),
                    updated_at=now,
                ),
                _mock_task(
                    title="c",
                    status=TaskStatus.DONE,
                    assigned_agent=_mock_agent(worktree_branch=branch_c),
                    updated_at=now,
                ),
            ],
        )

        report = await orchestrator.merge_all_agents_into_feature(
            task, feature.path
        )

        assert report.all_succeeded is False
        # Agent A should have merged, agent B should have conflicted
        assert len(report.agent_merges) == 2
        assert report.agent_merges[0].success is True  # agent a
        assert report.agent_merges[1].success is False  # agent b
        assert "README.md" in report.agent_merges[1].conflicts

        # Agent A's file should be present
        assert (feature.path / "a.py").exists()
        # Agent C should never have been attempted
        assert not (feature.path / "c.py").exists()


class TestMergeFeatureIntoMain:
    async def test_merge_feature_into_main(self, test_env) -> None:
        """Full lifecycle: create feature, add work, merge to main."""
        bare, manager, orchestrator = test_env
        main_dir = bare / "main"
        feature = await manager.create_feature("to-main")

        # Set git config on feature
        await run_git(
            ["config", "user.email", "test@test.com"], cwd=feature.path
        )
        await run_git(
            ["config", "user.name", "Test"], cwd=feature.path
        )

        # Add work on feature branch
        (feature.path / "feature_work.py").write_text("# feature work\n")
        await run_git(["add", "feature_work.py"], cwd=feature.path)
        await run_git(
            ["commit", "-m", "add feature work"], cwd=feature.path
        )

        task = _mock_task(
            title="to-main task",
            status=TaskStatus.MERGING,
            worktree_branch="feature/to-main",
        )

        result = await orchestrator.merge_feature_into_main(task)

        assert result.success is True
        assert result.source_branch == "feature/to-main"
        assert result.target_branch == "main"
        assert result.merge_commit is not None

        # Verify the file is in main
        assert (main_dir / "feature_work.py").exists()
        assert (main_dir / "feature_work.py").read_text() == "# feature work\n"


class TestSyncAfterMerge:
    async def test_sync_after_merge(self, test_env) -> None:
        """Merge feature A into main, verify feature B rebased."""
        bare, manager, orchestrator = test_env
        main_dir = bare / "main"

        # Create two features
        feature_a = await manager.create_feature("sync-a")
        feature_b = await manager.create_feature("sync-b")

        # Set git config
        await run_git(
            ["config", "user.email", "test@test.com"], cwd=feature_a.path
        )
        await run_git(
            ["config", "user.name", "Test"], cwd=feature_a.path
        )

        # Add work on feature A
        (feature_a.path / "from_a.py").write_text("# from A\n")
        await run_git(["add", "from_a.py"], cwd=feature_a.path)
        await run_git(
            ["commit", "-m", "work from feature A"], cwd=feature_a.path
        )

        # Merge feature A into main manually (so we can test sync separately)
        merge_result = await manager.merge_branch(
            "feature/sync-a", main_dir
        )
        assert merge_result.success

        # Now sync — feature B should be rebased
        results = await orchestrator.sync_features_after_merge(
            "feature/sync-a"
        )

        # Feature B should be synced (feature A is excluded since it was merged)
        b_results = [r for r in results if r.feature == "feature/sync-b"]
        assert len(b_results) == 1
        assert b_results[0].success is True

        # Verify feature B now has from_a.py (from main after merge)
        assert (feature_b.path / "from_a.py").exists()

    async def test_sync_skips_dirty(self, test_env) -> None:
        """Feature with uncommitted changes skipped during sync."""
        bare, manager, orchestrator = test_env
        main_dir = bare / "main"

        feature_a = await manager.create_feature("sync-dirty-a")
        feature_b = await manager.create_feature("sync-dirty-b")

        # Set git config
        await run_git(
            ["config", "user.email", "test@test.com"], cwd=feature_a.path
        )
        await run_git(
            ["config", "user.name", "Test"], cwd=feature_a.path
        )

        # Add work on feature A and merge into main
        (feature_a.path / "from_a.py").write_text("# from A\n")
        await run_git(["add", "from_a.py"], cwd=feature_a.path)
        await run_git(
            ["commit", "-m", "feature A work"], cwd=feature_a.path
        )
        merge_result = await manager.merge_branch(
            "feature/sync-dirty-a", main_dir
        )
        assert merge_result.success

        # Make feature B dirty
        (feature_b.path / "uncommitted.txt").write_text("dirty\n")

        # Sync
        results = await orchestrator.sync_features_after_merge(
            "feature/sync-dirty-a"
        )

        b_results = [r for r in results if r.feature == "feature/sync-dirty-b"]
        assert len(b_results) == 1
        assert b_results[0].success is False
        assert "<worktree has uncommitted changes>" in b_results[0].conflicts

        # Bus notification is best-effort (not connected in tests)
