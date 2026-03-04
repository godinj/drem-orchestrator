"""Tests for git_utils.py — low-level async git helpers."""

from __future__ import annotations

from pathlib import Path

import pytest

from orchestrator.git_utils import (
    GitError,
    get_changed_files,
    get_commit_log,
    is_clean,
    run_git,
)


async def _init_repo(path: Path) -> Path:
    """Create a non-bare git repo at path with an initial commit."""
    repo = path / "repo"
    repo.mkdir()
    await run_git(["init"], cwd=repo)
    await run_git(["config", "user.email", "test@test.com"], cwd=repo)
    await run_git(["config", "user.name", "Test"], cwd=repo)

    # Create initial commit
    readme = repo / "README.md"
    readme.write_text("# Test\n")
    await run_git(["add", "README.md"], cwd=repo)
    await run_git(["commit", "-m", "initial commit"], cwd=repo)

    return repo


class TestRunGit:
    async def test_run_git_success(self) -> None:
        """Simple git --version should succeed."""
        output = await run_git(["--version"])
        assert output.startswith("git version")

    async def test_run_git_failure(self) -> None:
        """Invalid git command should raise GitError."""
        with pytest.raises(GitError) as exc_info:
            await run_git(["nonsense-command-that-does-not-exist"])
        assert exc_info.value.returncode != 0
        assert "nonsense-command-that-does-not-exist" in exc_info.value.command

    async def test_run_git_with_cwd(self, tmp_path: Path) -> None:
        """Git command should respect cwd parameter."""
        repo = await _init_repo(tmp_path)
        output = await run_git(["rev-parse", "--is-inside-work-tree"], cwd=repo)
        assert output == "true"


class TestIsClean:
    async def test_clean_repo(self, tmp_path: Path) -> None:
        """Freshly committed repo should be clean."""
        repo = await _init_repo(tmp_path)
        assert await is_clean(repo) is True

    async def test_dirty_repo(self, tmp_path: Path) -> None:
        """Repo with uncommitted changes should be dirty."""
        repo = await _init_repo(tmp_path)
        (repo / "new_file.txt").write_text("dirty\n")
        assert await is_clean(repo) is False

    async def test_staged_is_dirty(self, tmp_path: Path) -> None:
        """Repo with staged but uncommitted changes should be dirty."""
        repo = await _init_repo(tmp_path)
        (repo / "staged.txt").write_text("staged\n")
        await run_git(["add", "staged.txt"], cwd=repo)
        assert await is_clean(repo) is False


class TestGetChangedFiles:
    async def test_changed_files(self, tmp_path: Path) -> None:
        """Files changed since a base ref should be reported."""
        repo = await _init_repo(tmp_path)

        # Record base
        base = await run_git(["rev-parse", "HEAD"], cwd=repo)

        # Make a change and commit
        (repo / "new_file.py").write_text("print('hello')\n")
        await run_git(["add", "new_file.py"], cwd=repo)
        await run_git(["commit", "-m", "add new_file"], cwd=repo)

        changed = await get_changed_files(repo, base)
        assert "new_file.py" in changed

    async def test_no_changes(self, tmp_path: Path) -> None:
        """No changes since HEAD should return empty list."""
        repo = await _init_repo(tmp_path)
        base = await run_git(["rev-parse", "HEAD"], cwd=repo)
        changed = await get_changed_files(repo, base)
        assert changed == []


class TestGetCommitLog:
    async def test_commit_log(self, tmp_path: Path) -> None:
        """Commit log should contain commits since base ref."""
        repo = await _init_repo(tmp_path)
        base = await run_git(["rev-parse", "HEAD"], cwd=repo)

        # Make two commits
        (repo / "a.txt").write_text("a\n")
        await run_git(["add", "a.txt"], cwd=repo)
        await run_git(["commit", "-m", "add a"], cwd=repo)

        (repo / "b.txt").write_text("b\n")
        await run_git(["add", "b.txt"], cwd=repo)
        await run_git(["commit", "-m", "add b"], cwd=repo)

        log = await get_commit_log(repo, base)
        assert len(log) == 2
        assert log[0].message == "add b"
        assert log[1].message == "add a"
        assert len(log[0].sha) == 40
        assert len(log[0].short_sha) >= 7

    async def test_empty_log(self, tmp_path: Path) -> None:
        """No commits since HEAD should return empty list."""
        repo = await _init_repo(tmp_path)
        base = await run_git(["rev-parse", "HEAD"], cwd=repo)
        log = await get_commit_log(repo, base)
        assert log == []
