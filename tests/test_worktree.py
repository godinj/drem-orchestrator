"""Tests for worktree.py — async worktree manager.

Uses real temporary bare git repos for integration testing.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from orchestrator.git_utils import GitError, run_git
from orchestrator.worktree import WorktreeManager


async def _create_bare_repo(base: Path) -> Path:
    """Create a bare git repo with an initial commit on main.

    The layout mirrors the godinj-dotfiles wt convention:
        base/test-project.git/      <- bare repo
        base/test-project.git/main/ <- default branch worktree

    Returns the path to the bare repo.
    """
    bare = base / "test-project.git"
    bare.mkdir()

    # Init bare repo
    await run_git(["init", "--bare", str(bare)])
    # Set default branch to main
    await run_git(["symbolic-ref", "HEAD", "refs/heads/main"], cwd=bare)

    # Create the main worktree with an initial commit
    main_dir = bare / "main"
    await run_git(
        ["worktree", "add", "--orphan", "-b", "main", str(main_dir)], cwd=bare
    )
    await run_git(["config", "user.email", "test@test.com"], cwd=main_dir)
    await run_git(["config", "user.name", "Test"], cwd=main_dir)

    (main_dir / "README.md").write_text("# Test Project\n")
    await run_git(["add", "README.md"], cwd=main_dir)
    await run_git(["commit", "-m", "initial commit"], cwd=main_dir)

    return bare


@pytest.fixture
async def bare_repo(tmp_path: Path) -> Path:
    """Fixture providing a temporary bare repo."""
    return await _create_bare_repo(tmp_path)


@pytest.fixture
def manager(bare_repo: Path) -> WorktreeManager:
    """Fixture providing a WorktreeManager for the temp bare repo."""
    return WorktreeManager(bare_repo=bare_repo)


class TestGetDefaultBranch:
    async def test_returns_main(self, manager: WorktreeManager) -> None:
        """Default branch should be 'main'."""
        assert await manager.get_default_branch() == "main"


class TestCreateAndListFeature:
    async def test_create_feature(self, manager: WorktreeManager) -> None:
        """Creating a feature worktree should produce a valid WorktreeInfo."""
        info = await manager.create_feature("auth")

        assert info.branch == "feature/auth"
        assert info.path.exists()
        assert info.path.name == "auth"
        assert "feature" in str(info.path)
        assert len(info.head) == 40
        assert info.is_bare is False
        assert info.agent_count == 0

    async def test_create_feature_with_prefix(self, manager: WorktreeManager) -> None:
        """Passing 'feature/auth' should not double-prefix."""
        info = await manager.create_feature("feature/auth")
        assert info.branch == "feature/auth"

    async def test_create_feature_with_base_ref(
        self, manager: WorktreeManager, bare_repo: Path
    ) -> None:
        """Creating a feature from a specific base ref should work."""
        main_dir = bare_repo / "main"
        base = await run_git(["rev-parse", "HEAD"], cwd=main_dir)
        info = await manager.create_feature("from-base", base_ref=base)
        assert info.branch == "feature/from-base"
        assert info.head == base

    async def test_create_duplicate_fails(self, manager: WorktreeManager) -> None:
        """Creating a feature that already exists should raise GitError."""
        await manager.create_feature("dup")
        with pytest.raises(GitError):
            await manager.create_feature("dup")

    async def test_list_includes_feature(self, manager: WorktreeManager) -> None:
        """A created feature should appear in list_worktrees."""
        await manager.create_feature("listing")
        worktrees = await manager.list_worktrees()

        branches = [wt.branch for wt in worktrees]
        assert "feature/listing" in branches
        assert "main" in branches


class TestCreateAndListAgentWorktree:
    async def test_create_agent_worktree(self, manager: WorktreeManager) -> None:
        """Agent worktree should be nested inside the feature."""
        await manager.create_feature("agents-test")
        agent = await manager.create_agent_worktree("agents-test")

        assert agent.path.exists()
        assert agent.parent_feature == "feature/agents-test"
        assert agent.branch.startswith("worktree-agent-")
        assert ".claude/worktrees/agent-" in str(agent.path)
        assert len(agent.head) == 40

    async def test_list_agent_worktrees(self, manager: WorktreeManager) -> None:
        """Created agent worktrees should appear in list_agent_worktrees."""
        await manager.create_feature("multi-agents")
        await manager.create_agent_worktree("multi-agents")
        await manager.create_agent_worktree("multi-agents")

        agents = await manager.list_agent_worktrees("multi-agents")
        assert len(agents) == 2
        assert all(a.parent_feature == "feature/multi-agents" for a in agents)
        assert all(a.branch.startswith("worktree-agent-") for a in agents)

    async def test_agent_count_in_list(self, manager: WorktreeManager) -> None:
        """list_worktrees should show correct agent_count."""
        await manager.create_feature("counted")
        await manager.create_agent_worktree("counted")
        await manager.create_agent_worktree("counted")

        worktrees = await manager.list_worktrees()
        counted = [wt for wt in worktrees if wt.branch == "feature/counted"]
        assert len(counted) == 1
        assert counted[0].agent_count == 2

    async def test_create_agent_in_nonexistent_feature_fails(
        self, manager: WorktreeManager
    ) -> None:
        """Creating an agent in a non-existent feature should fail."""
        with pytest.raises(GitError):
            await manager.create_agent_worktree("nonexistent")


class TestMergeBranch:
    async def test_merge_success(
        self, manager: WorktreeManager, bare_repo: Path
    ) -> None:
        """Merging a branch with no conflicts should succeed."""
        # Create feature with a commit
        feature = await manager.create_feature("merge-src")
        (feature.path / "feature_file.txt").write_text("feature content\n")
        await run_git(["add", "feature_file.txt"], cwd=feature.path)
        await run_git(
            ["commit", "-m", "add feature file"],
            cwd=feature.path,
        )

        # Merge into main
        main_dir = bare_repo / "main"
        result = await manager.merge_branch("feature/merge-src", main_dir)

        assert result.success is True
        assert result.source_branch == "feature/merge-src"
        assert result.target_branch == "main"
        assert result.merge_commit is not None
        assert len(result.merge_commit) == 40
        assert result.conflicts == []

        # Verify the file is now in main
        assert (main_dir / "feature_file.txt").exists()

    async def test_merge_conflict(
        self, manager: WorktreeManager, bare_repo: Path
    ) -> None:
        """Merging conflicting changes should report conflicts."""
        main_dir = bare_repo / "main"

        # Create feature and modify README
        feature = await manager.create_feature("conflict-src")
        (feature.path / "README.md").write_text("feature version\n")
        await run_git(["add", "README.md"], cwd=feature.path)
        await run_git(
            ["commit", "-m", "change readme in feature"],
            cwd=feature.path,
        )

        # Modify same file in main
        (main_dir / "README.md").write_text("main version\n")
        await run_git(["add", "README.md"], cwd=main_dir)
        await run_git(
            ["commit", "-m", "change readme in main"],
            cwd=main_dir,
        )

        # Attempt merge
        result = await manager.merge_branch("feature/conflict-src", main_dir)

        assert result.success is False
        assert result.merge_commit is None
        assert "README.md" in result.conflicts

        # Verify main is clean after abort
        status = await run_git(["status", "--porcelain"], cwd=main_dir)
        assert status == ""


class TestRemoveFeature:
    async def test_remove_feature(self, manager: WorktreeManager) -> None:
        """Removing a feature should clean up worktree and branch."""
        info = await manager.create_feature("removable")
        assert info.path.exists()

        await manager.remove_feature("removable")

        # Path should be gone
        assert not info.path.exists()

        # Branch should be gone
        worktrees = await manager.list_worktrees()
        branches = [wt.branch for wt in worktrees]
        assert "feature/removable" not in branches

    async def test_remove_feature_with_agents(self, manager: WorktreeManager) -> None:
        """Removing a feature should also remove nested agent worktrees."""
        await manager.create_feature("with-agents")
        agent = await manager.create_agent_worktree("with-agents")
        assert agent.path.exists()

        await manager.remove_feature("with-agents")

        assert not agent.path.exists()
        agents = await manager.list_agent_worktrees("with-agents")
        assert len(agents) == 0

    async def test_remove_nonexistent_fails(self, manager: WorktreeManager) -> None:
        """Removing a non-existent feature should raise GitError."""
        with pytest.raises(GitError):
            await manager.remove_feature("ghost")

    async def test_remove_default_branch_fails(self, manager: WorktreeManager) -> None:
        """Removing the default branch worktree should raise GitError."""
        with pytest.raises(GitError, match="cannot remove the default branch"):
            await manager.remove_feature("main")


class TestRemoveAgentWorktree:
    async def test_remove_agent(self, manager: WorktreeManager) -> None:
        """Removing an agent worktree should clean up path and branch."""
        await manager.create_feature("agent-rm-test")
        agent = await manager.create_agent_worktree("agent-rm-test")
        assert agent.path.exists()

        await manager.remove_agent_worktree(agent.branch)

        assert not agent.path.exists()
        agents = await manager.list_agent_worktrees("agent-rm-test")
        assert len(agents) == 0


class TestBranchStatus:
    async def test_branch_status(
        self, manager: WorktreeManager, bare_repo: Path
    ) -> None:
        """Branch status should report correct ahead/behind counts."""
        feature = await manager.create_feature("status-test")

        # Add commits to feature (ahead of main)
        (feature.path / "f1.txt").write_text("one\n")
        await run_git(["add", "f1.txt"], cwd=feature.path)
        await run_git(["commit", "-m", "feature commit 1"], cwd=feature.path)

        (feature.path / "f2.txt").write_text("two\n")
        await run_git(["add", "f2.txt"], cwd=feature.path)
        await run_git(["commit", "-m", "feature commit 2"], cwd=feature.path)

        status = await manager.get_branch_status(feature.path)

        assert status.branch == "feature/status-test"
        assert status.ahead == 2
        assert status.behind == 0
        assert status.dirty_files == 0
        assert status.last_commit_message == "feature commit 2"
        assert len(status.last_commit_sha) == 40

    async def test_dirty_files_counted(self, manager: WorktreeManager) -> None:
        """Dirty files should be counted in branch status."""
        feature = await manager.create_feature("dirty-test")
        (feature.path / "uncommitted.txt").write_text("dirty\n")

        status = await manager.get_branch_status(feature.path)
        assert status.dirty_files == 1


class TestSyncAll:
    async def test_sync_all_no_conflicts(
        self, manager: WorktreeManager, bare_repo: Path
    ) -> None:
        """sync_all should rebase features onto main without conflicts."""
        main_dir = bare_repo / "main"

        # Create two features
        await manager.create_feature("sync-a")
        await manager.create_feature("sync-b")

        # Add a commit to main
        (main_dir / "main_update.txt").write_text("update\n")
        await run_git(["add", "main_update.txt"], cwd=main_dir)
        await run_git(
            ["commit", "-m", "main update"],
            cwd=main_dir,
        )

        results = await manager.sync_all()

        assert len(results) == 2
        assert all(r.success for r in results)
        features = {r.feature for r in results}
        assert "feature/sync-a" in features
        assert "feature/sync-b" in features
