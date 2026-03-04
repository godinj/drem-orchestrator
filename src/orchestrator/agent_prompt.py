"""Generates the prompt file that gets passed to `claude --agent`.

Builds role-specific prompts for coder, researcher, and planner agents,
incorporating task details, project context, memories, and worktree info.
"""

from __future__ import annotations

from pathlib import Path

from orchestrator.models import Memory, Project, Task


def generate_agent_prompt(
    task: Task,
    project: Project,
    agent_type: str,
    worktree_path: Path,
    memories: list[Memory] | None = None,
    parent_context: dict | None = None,
) -> str:
    """Build the full prompt for a Claude Code agent session.

    Includes:
    - Task description and acceptance criteria
    - Project context (from project.description and task.context)
    - Relevant memories from prior work
    - Worktree info (branch, path)
    - Build/test commands from CLAUDE.md
    - Scope limitation: only modify files relevant to this task
    - Instruction to commit work and report completion

    For coder agents:
    - Include file list to modify
    - Include test expectations
    - Instruct to run build verification

    For researcher agents:
    - Include research questions
    - Instruct to write findings to a report file

    For planner agents:
    - Include high-level task description
    - Instruct to decompose into subtasks with file lists
    """
    sections: list[str] = []

    # Header
    sections.append(f"# Agent Task: {task.title}")
    sections.append("")

    # Task description
    sections.append("## Task Description")
    sections.append("")
    sections.append(task.description)
    sections.append("")

    # Project context
    if project.description:
        sections.append("## Project Context")
        sections.append("")
        sections.append(project.description)
        sections.append("")

    # Task-specific context
    if task.context:
        sections.append("## Additional Context")
        sections.append("")
        for key, value in task.context.items():
            sections.append(f"- **{key}**: {value}")
        sections.append("")

    # Parent context (from orchestrator or parent task)
    if parent_context:
        sections.append("## Parent Task Context")
        sections.append("")
        for key, value in parent_context.items():
            sections.append(f"- **{key}**: {value}")
        sections.append("")

    # Memories from prior work
    if memories:
        sections.append("## Relevant Memories from Prior Work")
        sections.append("")
        for memory in memories:
            sections.append(f"### {memory.memory_type}")
            sections.append(memory.content)
            sections.append("")

    # Worktree info
    sections.append("## Working Environment")
    sections.append("")
    sections.append(f"- **Worktree path**: `{worktree_path}`")
    sections.append(f"- **Project**: {project.name}")
    sections.append(f"- **Bare repo**: `{project.bare_repo_path}`")
    sections.append("")

    # Build/test commands
    sections.append("## Build & Test Commands")
    sections.append("")
    sections.append("```bash")
    sections.append("uv sync")
    sections.append("uv run pytest")
    sections.append("```")
    sections.append("")

    # Agent type-specific instructions
    if agent_type == "coder":
        sections.extend(_coder_instructions(task))
    elif agent_type == "researcher":
        sections.extend(_researcher_instructions(task))
    elif agent_type == "planner":
        sections.extend(_planner_instructions(task))
    else:
        sections.extend(_default_instructions())

    # Scope limitation
    sections.append("## Scope")
    sections.append("")
    sections.append(
        "Only modify files directly relevant to this task. "
        "Do not refactor unrelated code or change project configuration "
        "unless the task explicitly requires it."
    )
    sections.append("")

    # Commit instructions
    sections.append("## Completion")
    sections.append("")
    sections.append(
        "When you have completed the task, commit all changes with a "
        "descriptive commit message. Ensure all tests pass before committing."
    )
    sections.append("")

    return "\n".join(sections)


def _coder_instructions(task: Task) -> list[str]:
    """Generate coder-specific prompt sections."""
    sections: list[str] = []
    sections.append("## Coder Instructions")
    sections.append("")

    # File list from plan
    if task.plan and isinstance(task.plan, dict):
        files = task.plan.get("files", [])
        if files:
            sections.append("### Files to Modify")
            sections.append("")
            for f in files:
                sections.append(f"- `{f}`")
            sections.append("")

    # Test expectations
    if task.test_plan:
        sections.append("### Test Expectations")
        sections.append("")
        sections.append(task.test_plan)
        sections.append("")

    sections.append("### Build Verification")
    sections.append("")
    sections.append(
        "After making changes, run the build verification commands above "
        "to ensure nothing is broken. Fix any test failures before committing."
    )
    sections.append("")

    return sections


def _researcher_instructions(task: Task) -> list[str]:
    """Generate researcher-specific prompt sections."""
    sections: list[str] = []
    sections.append("## Researcher Instructions")
    sections.append("")
    sections.append(
        "Your job is to research and document findings, not to write code. "
        "Investigate the topic described in the task and write a detailed report."
    )
    sections.append("")

    # Research questions from context
    if task.context and isinstance(task.context, dict):
        questions = task.context.get("research_questions", [])
        if questions:
            sections.append("### Research Questions")
            sections.append("")
            for q in questions:
                sections.append(f"- {q}")
            sections.append("")

    sections.append("### Output")
    sections.append("")
    sections.append(
        "Write your findings to a report file at "
        "`research-report.md` in the worktree root. "
        "Use clear headings and cite sources where applicable."
    )
    sections.append("")

    return sections


def _planner_instructions(task: Task) -> list[str]:
    """Generate planner-specific prompt sections."""
    sections: list[str] = []
    sections.append("## Planner Instructions")
    sections.append("")
    sections.append(
        "Your job is to decompose this high-level task into concrete subtasks. "
        "Each subtask should be independently implementable by a coder agent."
    )
    sections.append("")
    sections.append("### Output Format")
    sections.append("")
    sections.append(
        "Write a plan as a JSON file at `plan.json` in the worktree root with "
        "the following structure:"
    )
    sections.append("")
    sections.append("```json")
    sections.append("{")
    sections.append('  "subtasks": [')
    sections.append("    {")
    sections.append('      "title": "Subtask title",')
    sections.append('      "description": "What needs to be done",')
    sections.append('      "files": ["src/file1.py", "src/file2.py"],')
    sections.append('      "dependencies": [],')
    sections.append('      "priority": 1')
    sections.append("    }")
    sections.append("  ]")
    sections.append("}")
    sections.append("```")
    sections.append("")
    sections.append(
        "For each subtask, identify which files need to be created or modified "
        "and list any dependencies on other subtasks."
    )
    sections.append("")

    return sections


def _default_instructions() -> list[str]:
    """Generate default instructions for unknown agent types."""
    sections: list[str] = []
    sections.append("## Instructions")
    sections.append("")
    sections.append(
        "Complete the task as described above. Commit your changes when done."
    )
    sections.append("")
    return sections


def write_prompt_file(worktree_path: Path, prompt: str) -> Path:
    """Write prompt to <worktree>/.claude/agent-prompt.md, return path."""
    claude_dir = worktree_path / ".claude"
    claude_dir.mkdir(parents=True, exist_ok=True)

    prompt_path = claude_dir / "agent-prompt.md"
    prompt_path.write_text(prompt)

    return prompt_path
