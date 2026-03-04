"""Tests for agent_prompt.py — prompt generation for Claude Code agents."""

from __future__ import annotations

import uuid
from pathlib import Path
from unittest.mock import MagicMock

from orchestrator.agent_prompt import generate_agent_prompt, write_prompt_file
from orchestrator.models import Memory, Project, Task


def _make_project() -> Project:
    """Create a mock Project object."""
    project = MagicMock(spec=Project)
    project.id = uuid.uuid4()
    project.name = "test-project"
    project.bare_repo_path = "/home/user/git/test-project.git"
    project.default_branch = "main"
    project.description = "A test project for unit testing"
    return project


def _make_task(
    title: str = "Implement auth module",
    description: str = "Build the authentication module with JWT support",
    context: dict | None = None,
    plan: dict | None = None,
    test_plan: str | None = None,
) -> Task:
    """Create a mock Task object."""
    task = MagicMock(spec=Task)
    task.id = uuid.uuid4()
    task.project_id = uuid.uuid4()
    task.title = title
    task.description = description
    task.status = "in_progress"
    task.context = context or {}
    task.plan = plan
    task.test_plan = test_plan
    return task


def _make_memory(
    content: str = "Previous implementation used bcrypt for hashing",
    memory_type: str = "implementation_note",
) -> Memory:
    """Create a mock Memory object."""
    memory = MagicMock(spec=Memory)
    memory.id = uuid.uuid4()
    memory.content = content
    memory.memory_type = memory_type
    return memory


class TestCoderPrompt:
    def test_coder_prompt_includes_task(self) -> None:
        """Coder prompt should include the task title and description."""
        task = _make_task(
            title="Build user login",
            description="Create a login endpoint with email/password auth",
        )
        project = _make_project()
        worktree_path = Path("/tmp/worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="coder",
            worktree_path=worktree_path,
        )

        assert "Build user login" in prompt
        assert "Create a login endpoint with email/password auth" in prompt

    def test_coder_prompt_includes_build_commands(self) -> None:
        """Coder prompt should include build/test commands."""
        task = _make_task()
        project = _make_project()
        worktree_path = Path("/tmp/worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="coder",
            worktree_path=worktree_path,
        )

        assert "uv sync" in prompt
        assert "uv run pytest" in prompt

    def test_coder_prompt_includes_file_list(self) -> None:
        """Coder prompt should include file list from plan."""
        task = _make_task(
            plan={"files": ["src/auth.py", "src/models.py"]},
        )
        project = _make_project()
        worktree_path = Path("/tmp/worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="coder",
            worktree_path=worktree_path,
        )

        assert "src/auth.py" in prompt
        assert "src/models.py" in prompt

    def test_coder_prompt_includes_test_plan(self) -> None:
        """Coder prompt should include test expectations."""
        task = _make_task(
            test_plan="All endpoints must have integration tests",
        )
        project = _make_project()
        worktree_path = Path("/tmp/worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="coder",
            worktree_path=worktree_path,
        )

        assert "All endpoints must have integration tests" in prompt

    def test_coder_prompt_includes_build_verification(self) -> None:
        """Coder prompt should instruct to run build verification."""
        task = _make_task()
        project = _make_project()
        worktree_path = Path("/tmp/worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="coder",
            worktree_path=worktree_path,
        )

        assert "Build Verification" in prompt

    def test_coder_prompt_includes_commit_instructions(self) -> None:
        """Coder prompt should instruct to commit work."""
        task = _make_task()
        project = _make_project()
        worktree_path = Path("/tmp/worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="coder",
            worktree_path=worktree_path,
        )

        assert "commit" in prompt.lower()


class TestResearcherPrompt:
    def test_researcher_prompt_format(self) -> None:
        """Researcher prompt should include research-specific instructions."""
        task = _make_task(
            title="Research OAuth providers",
            description="Compare OAuth 2.0 providers for our use case",
            context={
                "research_questions": [
                    "What are the top OAuth providers?",
                    "What are their pricing models?",
                ],
            },
        )
        project = _make_project()
        worktree_path = Path("/tmp/worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="researcher",
            worktree_path=worktree_path,
        )

        assert "Researcher Instructions" in prompt
        assert "research-report.md" in prompt
        assert "What are the top OAuth providers?" in prompt
        assert "What are their pricing models?" in prompt

    def test_researcher_prompt_includes_task(self) -> None:
        """Researcher prompt should include task description."""
        task = _make_task(
            title="Investigate performance bottleneck",
            description="Profile the API and identify slow endpoints",
        )
        project = _make_project()
        worktree_path = Path("/tmp/worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="researcher",
            worktree_path=worktree_path,
        )

        assert "Investigate performance bottleneck" in prompt
        assert "Profile the API and identify slow endpoints" in prompt

    def test_researcher_no_build_verification(self) -> None:
        """Researcher prompt should not include build verification section."""
        task = _make_task()
        project = _make_project()
        worktree_path = Path("/tmp/worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="researcher",
            worktree_path=worktree_path,
        )

        # Should NOT contain "Build Verification" section heading (coder-only)
        assert "### Build Verification" not in prompt


class TestPlannerPrompt:
    def test_planner_prompt_format(self) -> None:
        """Planner prompt should include decomposition instructions."""
        task = _make_task(
            title="Implement user management",
            description="Full user CRUD with roles and permissions",
        )
        project = _make_project()
        worktree_path = Path("/tmp/worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="planner",
            worktree_path=worktree_path,
        )

        assert "Planner Instructions" in prompt
        assert "decompose" in prompt.lower()
        assert "subtask" in prompt.lower()
        assert "plan.json" in prompt

    def test_planner_prompt_includes_json_format(self) -> None:
        """Planner prompt should show expected JSON output format."""
        task = _make_task()
        project = _make_project()
        worktree_path = Path("/tmp/worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="planner",
            worktree_path=worktree_path,
        )

        assert '"subtasks"' in prompt
        assert '"title"' in prompt
        assert '"files"' in prompt


class TestMemoriesIncluded:
    def test_memories_included(self) -> None:
        """Prior memories should appear in the prompt."""
        task = _make_task()
        project = _make_project()
        worktree_path = Path("/tmp/worktree")

        memories = [
            _make_memory(
                content="The auth module uses bcrypt for password hashing",
                memory_type="implementation_note",
            ),
            _make_memory(
                content="API rate limiting is set to 100 req/min",
                memory_type="configuration",
            ),
        ]

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="coder",
            worktree_path=worktree_path,
            memories=memories,
        )

        assert "bcrypt for password hashing" in prompt
        assert "100 req/min" in prompt
        assert "Relevant Memories" in prompt
        assert "implementation_note" in prompt
        assert "configuration" in prompt

    def test_no_memories_section_when_empty(self) -> None:
        """Memories section should not appear when no memories provided."""
        task = _make_task()
        project = _make_project()
        worktree_path = Path("/tmp/worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="coder",
            worktree_path=worktree_path,
            memories=None,
        )

        assert "Relevant Memories" not in prompt

    def test_empty_memories_list_no_section(self) -> None:
        """Memories section should not appear when empty list provided."""
        task = _make_task()
        project = _make_project()
        worktree_path = Path("/tmp/worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="coder",
            worktree_path=worktree_path,
            memories=[],
        )

        assert "Relevant Memories" not in prompt


class TestProjectContext:
    def test_project_description_included(self) -> None:
        """Project description should appear in the prompt."""
        task = _make_task()
        project = _make_project()
        project.description = "An orchestrator for Claude Code agents"
        worktree_path = Path("/tmp/worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="coder",
            worktree_path=worktree_path,
        )

        assert "An orchestrator for Claude Code agents" in prompt

    def test_task_context_included(self) -> None:
        """Task context dict should appear in the prompt."""
        task = _make_task(
            context={"architecture": "microservices", "language": "Python 3.12"},
        )
        project = _make_project()
        worktree_path = Path("/tmp/worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="coder",
            worktree_path=worktree_path,
        )

        assert "microservices" in prompt
        assert "Python 3.12" in prompt

    def test_parent_context_included(self) -> None:
        """Parent context should appear in the prompt."""
        task = _make_task()
        project = _make_project()
        worktree_path = Path("/tmp/worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="coder",
            worktree_path=worktree_path,
            parent_context={"scope": "backend only", "deadline": "Q1"},
        )

        assert "backend only" in prompt
        assert "Q1" in prompt
        assert "Parent Task Context" in prompt

    def test_worktree_info_included(self) -> None:
        """Worktree path and project info should appear in the prompt."""
        task = _make_task()
        project = _make_project()
        worktree_path = Path("/tmp/my-worktree")

        prompt = generate_agent_prompt(
            task=task,
            project=project,
            agent_type="coder",
            worktree_path=worktree_path,
        )

        assert "/tmp/my-worktree" in prompt
        assert project.name in prompt


class TestWritePromptFile:
    def test_prompt_file_written(self, tmp_path: Path) -> None:
        """Prompt file should be created at expected path."""
        worktree_path = tmp_path / "my-worktree"
        worktree_path.mkdir()

        prompt_text = "# Test Prompt\n\nDo the thing."

        result_path = write_prompt_file(worktree_path, prompt_text)

        expected_path = worktree_path / ".claude" / "agent-prompt.md"
        assert result_path == expected_path
        assert expected_path.exists()
        assert expected_path.read_text() == prompt_text

    def test_prompt_file_creates_directory(self, tmp_path: Path) -> None:
        """write_prompt_file should create .claude directory if missing."""
        worktree_path = tmp_path / "fresh-worktree"
        worktree_path.mkdir()

        prompt_text = "test prompt"
        result_path = write_prompt_file(worktree_path, prompt_text)

        assert result_path.exists()
        assert (worktree_path / ".claude").is_dir()

    def test_prompt_file_overwrites(self, tmp_path: Path) -> None:
        """Writing a prompt file twice should overwrite the first."""
        worktree_path = tmp_path / "overwrite-test"
        worktree_path.mkdir()

        write_prompt_file(worktree_path, "first prompt")
        write_prompt_file(worktree_path, "second prompt")

        result_path = worktree_path / ".claude" / "agent-prompt.md"
        assert result_path.read_text() == "second prompt"
