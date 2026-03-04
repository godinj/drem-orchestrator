"""Low-level async git command helpers used by worktree.py."""

from __future__ import annotations

import asyncio
import os
from dataclasses import dataclass
from pathlib import Path


class GitError(Exception):
    """Raised when a git command exits with a non-zero return code."""

    def __init__(self, command: str, returncode: int, stderr: str) -> None:
        self.command = command
        self.returncode = returncode
        self.stderr = stderr
        super().__init__(f"git command failed (exit {returncode}): {command}\n{stderr}")


@dataclass
class CommitInfo:
    """Parsed git log entry."""

    sha: str
    short_sha: str
    author: str
    date: str
    message: str


async def run_git(args: list[str], cwd: Path | None = None) -> str:
    """Run a git command and return stdout.

    Raises GitError on non-zero exit.
    """
    proc = await asyncio.create_subprocess_exec(
        "git",
        *args,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        cwd=cwd,
    )
    stdout_bytes, stderr_bytes = await proc.communicate()
    stdout = stdout_bytes.decode().strip()
    stderr = stderr_bytes.decode().strip()

    if proc.returncode != 0:
        cmd_str = f"git {' '.join(args)}"
        raise GitError(cmd_str, proc.returncode, stderr)

    return stdout


async def run_shell(
    cmd: str, cwd: Path | None = None, env: dict[str, str] | None = None
) -> str:
    """Run a shell command (for wt scripts) and return stdout.

    Raises GitError on non-zero exit.
    """
    merged_env: dict[str, str] | None = None
    if env is not None:
        merged_env = {**os.environ, **env}

    proc = await asyncio.create_subprocess_shell(
        cmd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        cwd=cwd,
        env=merged_env,
    )
    stdout_bytes, stderr_bytes = await proc.communicate()
    stdout = stdout_bytes.decode().strip()
    stderr = stderr_bytes.decode().strip()

    if proc.returncode != 0:
        raise GitError(cmd, proc.returncode, stderr)

    return stdout


async def get_commit_log(
    worktree: Path, base_ref: str, max_count: int = 50
) -> list[CommitInfo]:
    """Parse git log for commits since base_ref.

    Returns a list of CommitInfo ordered newest-first.
    """
    # Use a delimiter that won't appear in commit messages
    sep = "---COMMIT-SEP---"
    fmt = f"%H{sep}%h{sep}%an{sep}%ai{sep}%s"

    output = await run_git(
        [
            "log",
            f"--format={fmt}",
            f"--max-count={max_count}",
            f"{base_ref}..HEAD",
        ],
        cwd=worktree,
    )

    if not output:
        return []

    commits: list[CommitInfo] = []
    for line in output.splitlines():
        parts = line.split(sep, maxsplit=4)
        if len(parts) != 5:
            continue
        commits.append(
            CommitInfo(
                sha=parts[0],
                short_sha=parts[1],
                author=parts[2],
                date=parts[3],
                message=parts[4],
            )
        )
    return commits


async def get_changed_files(worktree: Path, base_ref: str) -> list[str]:
    """List files changed since base_ref (git diff --name-only)."""
    output = await run_git(
        ["diff", "--name-only", f"{base_ref}..HEAD"],
        cwd=worktree,
    )
    if not output:
        return []
    return output.splitlines()


async def is_clean(worktree: Path) -> bool:
    """Check if worktree has no uncommitted changes."""
    output = await run_git(
        ["status", "--porcelain"],
        cwd=worktree,
    )
    return output == ""
