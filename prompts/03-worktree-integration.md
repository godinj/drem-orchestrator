# Agent: Worktree Integration

You are working on **Drem Orchestrator**, a Python application that orchestrates Claude Code agents
working in parallel across git worktrees. Your task is to build the Python wrapper around the
existing `wt` shell scripts from godinj-dotfiles.

## Context

Read these before starting:
- `CLAUDE.md` (project conventions)
- `~/git/godinj-dotfiles.git/wt/wt.sh` (main router script)
- `~/git/godinj-dotfiles.git/wt/wt-new.sh` (create worktree)
- `~/git/godinj-dotfiles.git/wt/wt-rm.sh` (remove worktree)
- `~/git/godinj-dotfiles.git/wt/wt-list.sh` (list worktrees)
- `~/git/godinj-dotfiles.git/wt/wt-agent.sh` (manage agent panes)
- `~/git/godinj-dotfiles.git/wt/wt-helpers.sh` (shared utilities — branch prefix, bare root detection)
- `~/git/drem-canvas.git/main/CLAUDE.md` (example of how worktrees are used in practice)

The existing worktree layout is a 3-tier system:
```
bare-repo.git/
  main/                           ← master branch
  feature/X/                      ← feature/X branch
    .claude/worktrees/
      agent-<uuid>/               ← worktree-agent-<uuid> branch
```

## Deliverables

### New files (`src/orchestrator/`)

#### 1. `worktree.py`

Async wrapper around the `wt` shell scripts. Uses `asyncio.create_subprocess_exec` to call
them, parsing their output.

```python
from dataclasses import dataclass
from pathlib import Path

@dataclass
class WorktreeInfo:
    path: Path
    branch: str
    head: str            # commit SHA
    is_bare: bool
    agent_count: int     # number of agent panes (from wt list)
    session_active: bool

@dataclass
class AgentWorktreeInfo:
    path: Path
    branch: str
    head: str
    parent_feature: str  # the feature branch this agent belongs to

class WorktreeManager:
    def __init__(self, bare_repo: Path, wt_bin: Path):
        """
        bare_repo: absolute path to the bare git repo (e.g. ~/git/drem-canvas.git)
        wt_bin: absolute path to wt.sh (e.g. ~/git/godinj-dotfiles.git/wt/wt.sh)
        """

    async def create_feature(self, name: str, base_ref: str | None = None) -> WorktreeInfo:
        """
        Create a feature worktree. Calls: wt new <name> [base_ref]
        The wt script auto-prefixes 'feature/' if not present.
        Returns WorktreeInfo for the new worktree.
        Does NOT create a tmux session (pass --no-session or set env to suppress).
        """

    async def remove_feature(self, name: str) -> None:
        """
        Remove a feature worktree. Calls: wt rm <name>
        Handles submodule deinit, worktree metadata cleanup, branch deletion.
        """

    async def list_worktrees(self) -> list[WorktreeInfo]:
        """
        List all worktrees. Parses: git worktree list --porcelain
        Enriches with agent counts from wt list output.
        """

    async def list_agent_worktrees(self, feature_name: str) -> list[AgentWorktreeInfo]:
        """
        List agent worktrees nested inside a feature.
        Scans: <bare_repo>/feature/<name>/.claude/worktrees/agent-*/
        Parses git branch info for each.
        """

    async def create_agent_worktree(self, feature_name: str) -> AgentWorktreeInfo:
        """
        Create an agent worktree inside a feature.
        Generates UUID, creates worktree at:
          <bare_repo>/feature/<name>/.claude/worktrees/agent-<uuid>/
        Branch: worktree-agent-<uuid>
        Base: the feature branch (not master).
        Returns AgentWorktreeInfo.
        """

    async def remove_agent_worktree(self, agent_branch: str) -> None:
        """
        Remove an agent worktree.
        Calls: git worktree remove <path> && git branch -d <branch>
        """

    async def merge_branch(self, source_branch: str, target_worktree: Path) -> MergeResult:
        """
        Merge source_branch into the branch checked out at target_worktree.
        Runs: git -C <target_worktree> merge <source_branch>
        Returns MergeResult with success/failure and conflict details.
        """

    async def sync_all(self) -> list[SyncResult]:
        """
        Rebase all feature worktrees onto the default branch.
        For each feature: git -C <feature_path> rebase <default_branch>
        Returns list of results (success or conflict per feature).
        """

    async def get_branch_status(self, worktree_path: Path) -> BranchStatus:
        """
        Get commit count ahead/behind default branch, dirty file count.
        """

    async def get_default_branch(self) -> str:
        """
        Read the symbolic HEAD of the bare repo to determine default branch.
        """
```

**Helper dataclasses:**

```python
@dataclass
class MergeResult:
    success: bool
    source_branch: str
    target_branch: str
    merge_commit: str | None    # SHA if successful
    conflicts: list[str]        # conflicting file paths if failed

@dataclass
class SyncResult:
    feature: str
    success: bool
    conflicts: list[str]

@dataclass
class BranchStatus:
    branch: str
    ahead: int
    behind: int
    dirty_files: int
    last_commit_sha: str
    last_commit_message: str
```

#### 2. `git_utils.py`

Low-level async git command helpers used by `worktree.py`:

```python
async def run_git(args: list[str], cwd: Path | None = None) -> str:
    """Run a git command, return stdout. Raise GitError on failure."""

async def run_shell(cmd: str, cwd: Path | None = None, env: dict | None = None) -> str:
    """Run a shell command (for wt scripts), return stdout. Raise on failure."""

async def get_commit_log(
    worktree: Path, base_ref: str, max_count: int = 50
) -> list[CommitInfo]:
    """Parse git log --format for commits since base_ref."""

async def get_changed_files(worktree: Path, base_ref: str) -> list[str]:
    """List files changed since base_ref (git diff --name-only)."""

async def is_clean(worktree: Path) -> bool:
    """Check if worktree has no uncommitted changes."""

class GitError(Exception):
    def __init__(self, command: str, returncode: int, stderr: str): ...
```

```python
@dataclass
class CommitInfo:
    sha: str
    short_sha: str
    author: str
    date: str
    message: str
```

### Tests

#### 3. `tests/test_worktree.py`

Tests using a temporary bare git repo created in `/tmp`:
- `test_create_and_list_feature` — create a feature worktree, verify it appears in list
- `test_create_and_list_agent_worktree` — create agent worktree inside feature, verify nested path
- `test_merge_branch` — create two branches, make commits, merge, verify success
- `test_merge_conflict` — create conflicting changes, verify MergeResult reports conflicts
- `test_remove_feature` — create then remove, verify cleaned up
- `test_branch_status` — verify ahead/behind counts

Note: Tests should create a real temporary bare repo with `git init --bare` and worktrees,
not mock git. This ensures the wrapper actually works with git. Use `pytest tmp_path` fixture
for isolation.

#### 4. `tests/test_git_utils.py`

- `test_run_git_success` — simple `git --version`
- `test_run_git_failure` — invalid command raises `GitError`
- `test_is_clean` — clean and dirty worktree states
- `test_get_changed_files` — files reported correctly

## Scope Limitation

Do NOT create tmux sessions from this module. The `wt` scripts create tmux sessions
by default — when calling `wt new` programmatically, either:
1. Set environment variables to suppress tmux (check if `wt new` respects `TMUX=""`)
2. Or call `git worktree add` directly instead of `wt new` for agent worktrees

The orchestrator will manage agent processes separately via `agent_runner.py`.
Use `wt` scripts as reference implementations but call `git worktree` directly
when the wt script's tmux integration is unwanted.

## Build Verification

```bash
uv sync
uv run pytest tests/test_worktree.py tests/test_git_utils.py -v
```
