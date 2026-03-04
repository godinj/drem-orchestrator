"""Async wrapper around git worktree operations.

Uses asyncio.create_subprocess_exec to call git directly, following the
conventions established by the wt shell scripts from godinj-dotfiles.

Worktree layout (3-tier):
    bare-repo.git/
      main/                           <- default branch
      feature/X/                      <- feature/X branch
        .claude/worktrees/
          agent-<uuid>/               <- worktree-agent-<uuid> branch
"""

from __future__ import annotations

import uuid
from dataclasses import dataclass, field
from pathlib import Path

from orchestrator.git_utils import GitError, run_git

FEATURE_PREFIX = "feature/"


@dataclass
class WorktreeInfo:
    """Information about a top-level worktree (feature or default branch)."""

    path: Path
    branch: str
    head: str  # commit SHA
    is_bare: bool
    agent_count: int  # number of agent worktrees nested inside
    session_active: bool


@dataclass
class AgentWorktreeInfo:
    """Information about an agent worktree nested inside a feature."""

    path: Path
    branch: str
    head: str
    parent_feature: str  # the feature branch this agent belongs to


@dataclass
class MergeResult:
    """Result of a merge operation."""

    success: bool
    source_branch: str
    target_branch: str
    merge_commit: str | None  # SHA if successful
    conflicts: list[str] = field(default_factory=list)


@dataclass
class SyncResult:
    """Result of rebasing a feature onto the default branch."""

    feature: str
    success: bool
    conflicts: list[str] = field(default_factory=list)


@dataclass
class BranchStatus:
    """Commit count ahead/behind default branch and dirty file info."""

    branch: str
    ahead: int
    behind: int
    dirty_files: int
    last_commit_sha: str
    last_commit_message: str


class WorktreeManager:
    """Manages git worktrees for the orchestrator.

    Calls git commands directly via asyncio subprocess rather than
    going through the wt shell scripts (which create tmux sessions).
    """

    def __init__(self, bare_repo: Path, wt_bin: Path | None = None) -> None:
        """Initialize the worktree manager.

        Args:
            bare_repo: Absolute path to the bare git repo
                       (e.g. ~/git/drem-canvas.git).
            wt_bin: Absolute path to wt.sh. Currently unused since we call
                    git directly to avoid tmux side effects, but kept for
                    potential future use.
        """
        self.bare_repo = bare_repo.resolve()
        self.wt_bin = wt_bin

    def _ensure_prefix(self, name: str) -> str:
        """Add feature/ prefix if not already present."""
        if name.startswith(FEATURE_PREFIX):
            return name
        return f"{FEATURE_PREFIX}{name}"

    async def get_default_branch(self) -> str:
        """Read the symbolic HEAD of the bare repo to determine default branch."""
        try:
            output = await run_git(
                ["symbolic-ref", "--short", "HEAD"],
                cwd=self.bare_repo,
            )
            return output
        except GitError:
            return "main"

    async def create_feature(
        self, name: str, base_ref: str | None = None
    ) -> WorktreeInfo:
        """Create a feature worktree.

        The feature/ prefix is auto-added if not present.
        Does NOT create a tmux session.

        Args:
            name: Feature name (e.g. "auth" -> "feature/auth").
            base_ref: Base ref to branch from. Defaults to HEAD.

        Returns:
            WorktreeInfo for the new worktree.
        """
        branch = self._ensure_prefix(name)
        worktree_dir = self.bare_repo / branch

        if worktree_dir.exists():
            raise GitError(
                f"git worktree add {worktree_dir}",
                1,
                f"worktree '{worktree_dir}' already exists",
            )

        # Create the worktree with a new branch
        args = ["worktree", "add", "-b", branch, str(worktree_dir)]
        if base_ref:
            args.append(base_ref)

        await run_git(args, cwd=self.bare_repo)

        # Get the HEAD commit of the new worktree
        head = await run_git(["rev-parse", "HEAD"], cwd=worktree_dir)

        return WorktreeInfo(
            path=worktree_dir,
            branch=branch,
            head=head,
            is_bare=False,
            agent_count=0,
            session_active=False,
        )

    async def remove_feature(self, name: str) -> None:
        """Remove a feature worktree.

        Handles worktree removal, prune, and branch deletion.
        Does not prompt interactively.

        Args:
            name: Feature name (with or without feature/ prefix).
        """
        branch = self._ensure_prefix(name)
        worktree_dir = self.bare_repo / branch

        # Prevent removing the default branch (check before existence
        # so that passing "main" is always rejected regardless of prefix)
        default_branch = await self.get_default_branch()
        if branch == default_branch or name == default_branch:
            raise GitError(
                f"git worktree remove {worktree_dir}",
                1,
                f"cannot remove the default branch worktree ('{default_branch}')",
            )

        if not worktree_dir.exists():
            raise GitError(
                f"git worktree remove {worktree_dir}",
                1,
                f"worktree '{worktree_dir}' does not exist",
            )

        # Remove any nested agent worktrees first
        agent_worktrees = await self.list_agent_worktrees(name)
        for agent in agent_worktrees:
            await self.remove_agent_worktree(agent.branch)

        # Remove the worktree
        try:
            await run_git(
                ["worktree", "remove", "--force", str(worktree_dir)],
                cwd=self.bare_repo,
            )
        except GitError:
            # Fallback: manual removal + prune
            import shutil

            shutil.rmtree(worktree_dir, ignore_errors=True)
            await run_git(["worktree", "prune"], cwd=self.bare_repo)

        # Delete the branch
        try:
            await run_git(
                ["branch", "-D", branch],
                cwd=self.bare_repo,
            )
        except GitError:
            pass  # Branch may already be gone

    async def list_worktrees(self) -> list[WorktreeInfo]:
        """List all top-level worktrees.

        Parses git worktree list --porcelain and enriches with agent counts.

        Returns:
            List of WorktreeInfo for each worktree.
        """
        output = await run_git(
            ["worktree", "list", "--porcelain"],
            cwd=self.bare_repo,
        )

        worktrees: list[WorktreeInfo] = []
        wt_path: str | None = None
        head: str = ""
        branch: str = ""
        is_bare = False

        for line in output.splitlines() + [""]:
            if line.startswith("worktree "):
                wt_path = line[len("worktree ") :]
            elif line.startswith("HEAD "):
                head = line[len("HEAD ") :]
            elif line.startswith("branch "):
                ref = line[len("branch ") :]
                branch = ref.removeprefix("refs/heads/")
            elif line == "bare":
                is_bare = True
            elif line == "" and wt_path is not None:
                # End of record
                path = Path(wt_path)

                # Count nested agent worktrees
                agent_count = 0
                if not is_bare and branch.startswith(FEATURE_PREFIX):
                    agent_dir = path / ".claude" / "worktrees"
                    if agent_dir.exists():
                        agent_count = sum(
                            1
                            for d in agent_dir.iterdir()
                            if d.is_dir() and d.name.startswith("agent-")
                        )

                worktrees.append(
                    WorktreeInfo(
                        path=path,
                        branch=branch,
                        head=head,
                        is_bare=is_bare,
                        agent_count=agent_count,
                        session_active=False,  # No tmux integration
                    )
                )

                # Reset for next record
                wt_path = None
                head = ""
                branch = ""
                is_bare = False

        return worktrees

    async def list_agent_worktrees(self, feature_name: str) -> list[AgentWorktreeInfo]:
        """List agent worktrees nested inside a feature.

        Scans: <bare_repo>/feature/<name>/.claude/worktrees/agent-*/

        Args:
            feature_name: Feature name (with or without feature/ prefix).

        Returns:
            List of AgentWorktreeInfo for each agent worktree found.
        """
        branch = self._ensure_prefix(feature_name)
        feature_dir = self.bare_repo / branch
        agent_base = feature_dir / ".claude" / "worktrees"

        if not agent_base.exists():
            return []

        agents: list[AgentWorktreeInfo] = []
        for agent_dir in sorted(agent_base.iterdir()):
            if not agent_dir.is_dir() or not agent_dir.name.startswith("agent-"):
                continue

            try:
                head = await run_git(["rev-parse", "HEAD"], cwd=agent_dir)
                agent_branch = await run_git(
                    ["rev-parse", "--abbrev-ref", "HEAD"], cwd=agent_dir
                )
            except GitError:
                continue

            agents.append(
                AgentWorktreeInfo(
                    path=agent_dir,
                    branch=agent_branch,
                    head=head,
                    parent_feature=branch,
                )
            )

        return agents

    async def create_agent_worktree(self, feature_name: str) -> AgentWorktreeInfo:
        """Create an agent worktree inside a feature.

        Generates a UUID and creates a worktree at:
            <bare_repo>/feature/<name>/.claude/worktrees/agent-<uuid>/
        Branch: worktree-agent-<uuid>
        Base: the feature branch (not the default branch).

        Args:
            feature_name: Feature name (with or without feature/ prefix).

        Returns:
            AgentWorktreeInfo for the new agent worktree.
        """
        branch = self._ensure_prefix(feature_name)
        feature_dir = self.bare_repo / branch

        if not feature_dir.exists():
            raise GitError(
                f"create agent worktree in {feature_dir}",
                1,
                f"feature worktree '{feature_dir}' does not exist",
            )

        agent_uuid = uuid.uuid4().hex[:8]
        agent_branch = f"worktree-agent-{agent_uuid}"
        agent_dir = feature_dir / ".claude" / "worktrees" / f"agent-{agent_uuid}"

        # Ensure parent directory exists
        agent_dir.parent.mkdir(parents=True, exist_ok=True)

        # Create the worktree branching from the feature branch
        await run_git(
            [
                "worktree",
                "add",
                "-b",
                agent_branch,
                str(agent_dir),
                branch,
            ],
            cwd=self.bare_repo,
        )

        head = await run_git(["rev-parse", "HEAD"], cwd=agent_dir)

        return AgentWorktreeInfo(
            path=agent_dir,
            branch=agent_branch,
            head=head,
            parent_feature=branch,
        )

    async def remove_agent_worktree(self, agent_branch: str) -> None:
        """Remove an agent worktree.

        Removes the worktree and deletes the branch.

        Args:
            agent_branch: The branch name (e.g. "worktree-agent-abc12345").
        """
        # Find the worktree path for this branch
        worktrees = await self._parse_worktree_list()
        agent_path: Path | None = None
        for wt_path, wt_branch in worktrees:
            if wt_branch == agent_branch:
                agent_path = wt_path
                break

        if agent_path is None:
            raise GitError(
                f"remove agent worktree {agent_branch}",
                1,
                f"no worktree found for branch '{agent_branch}'",
            )

        # Remove the worktree
        try:
            await run_git(
                ["worktree", "remove", "--force", str(agent_path)],
                cwd=self.bare_repo,
            )
        except GitError:
            import shutil

            shutil.rmtree(agent_path, ignore_errors=True)
            await run_git(["worktree", "prune"], cwd=self.bare_repo)

        # Delete the branch
        try:
            await run_git(
                ["branch", "-D", agent_branch],
                cwd=self.bare_repo,
            )
        except GitError:
            pass  # Branch may already be gone

    async def merge_branch(
        self, source_branch: str, target_worktree: Path
    ) -> MergeResult:
        """Merge source_branch into the branch checked out at target_worktree.

        Args:
            source_branch: Branch name to merge from.
            target_worktree: Path to the worktree to merge into.

        Returns:
            MergeResult with success/failure and conflict details.
        """
        target_branch = await run_git(
            ["rev-parse", "--abbrev-ref", "HEAD"],
            cwd=target_worktree,
        )

        try:
            await run_git(
                ["merge", source_branch, "--no-edit"],
                cwd=target_worktree,
            )
            merge_commit = await run_git(
                ["rev-parse", "HEAD"],
                cwd=target_worktree,
            )
            return MergeResult(
                success=True,
                source_branch=source_branch,
                target_branch=target_branch,
                merge_commit=merge_commit,
            )
        except GitError:
            # Get list of conflicting files
            conflicts: list[str] = []
            try:
                status_output = await run_git(
                    ["diff", "--name-only", "--diff-filter=U"],
                    cwd=target_worktree,
                )
                if status_output:
                    conflicts = status_output.splitlines()
            except GitError:
                pass

            # Abort the merge
            try:
                await run_git(["merge", "--abort"], cwd=target_worktree)
            except GitError:
                pass

            return MergeResult(
                success=False,
                source_branch=source_branch,
                target_branch=target_branch,
                merge_commit=None,
                conflicts=conflicts,
            )

    async def sync_all(self) -> list[SyncResult]:
        """Rebase all feature worktrees onto the default branch.

        Returns:
            List of SyncResult (success or conflict per feature).
        """
        default_branch = await self.get_default_branch()
        worktrees = await self.list_worktrees()
        results: list[SyncResult] = []

        for wt in worktrees:
            if wt.is_bare:
                continue
            if not wt.branch.startswith(FEATURE_PREFIX):
                continue

            try:
                await run_git(
                    ["rebase", default_branch],
                    cwd=wt.path,
                )
                results.append(SyncResult(feature=wt.branch, success=True))
            except GitError:
                # Get conflicts
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

        return results

    async def get_branch_status(self, worktree_path: Path) -> BranchStatus:
        """Get commit count ahead/behind default branch and dirty file count.

        Args:
            worktree_path: Path to the worktree to inspect.

        Returns:
            BranchStatus with ahead/behind counts and dirty files.
        """
        branch = await run_git(
            ["rev-parse", "--abbrev-ref", "HEAD"],
            cwd=worktree_path,
        )

        default_branch = await self.get_default_branch()

        # Get ahead/behind counts
        try:
            rev_list = await run_git(
                ["rev-list", "--left-right", "--count", f"{default_branch}...{branch}"],
                cwd=worktree_path,
            )
            parts = rev_list.split()
            behind = int(parts[0])
            ahead = int(parts[1])
        except (GitError, IndexError, ValueError):
            ahead = 0
            behind = 0

        # Count dirty files
        status_output = await run_git(
            ["status", "--porcelain"],
            cwd=worktree_path,
        )
        dirty_files = len(status_output.splitlines()) if status_output else 0

        # Last commit info
        last_sha = await run_git(
            ["rev-parse", "HEAD"],
            cwd=worktree_path,
        )
        last_message = await run_git(
            ["log", "-1", "--format=%s"],
            cwd=worktree_path,
        )

        return BranchStatus(
            branch=branch,
            ahead=ahead,
            behind=behind,
            dirty_files=dirty_files,
            last_commit_sha=last_sha,
            last_commit_message=last_message,
        )

    async def _parse_worktree_list(self) -> list[tuple[Path, str]]:
        """Parse git worktree list --porcelain into (path, branch) tuples."""
        output = await run_git(
            ["worktree", "list", "--porcelain"],
            cwd=self.bare_repo,
        )

        results: list[tuple[Path, str]] = []
        wt_path: str | None = None
        branch: str = ""

        for line in output.splitlines() + [""]:
            if line.startswith("worktree "):
                wt_path = line[len("worktree ") :]
            elif line.startswith("branch "):
                ref = line[len("branch ") :]
                branch = ref.removeprefix("refs/heads/")
            elif line == "bare":
                branch = ""
            elif line == "" and wt_path is not None:
                if branch:
                    results.append((Path(wt_path), branch))
                wt_path = None
                branch = ""

        return results
