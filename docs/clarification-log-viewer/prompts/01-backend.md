# Agent: Backend — Clarification Flow & Agent Log Viewer

You are working on the `master` branch of Drem Orchestrator, a Python FastAPI application that orchestrates Claude Code agents via a task board with a state machine lifecycle.
Your task is implementing two backend features: (1) a clarification question flow for planner agents, and (2) an agent log viewer API endpoint.

## Context

Read these files before starting:
- `CLAUDE.md` (project conventions, build commands)
- `src/orchestrator/enums.py` (current `TaskStatus` enum)
- `src/orchestrator/state_machine.py` (transition map, human gates)
- `src/orchestrator/models.py` (Task ORM model — `status` Enum column at line 55, `context` JSON column at line 84)
- `src/orchestrator/schemas.py` (Pydantic models — `TaskResponse`, `PlanReview`, `TestResult`)
- `src/orchestrator/orchestrator.py` (`_on_planner_completed()` at line 718, `HUMAN_OWNED_STATES` at line 40)
- `src/orchestrator/agent_prompt.py` (`_planner_instructions()` at line 206)
- `src/orchestrator/routers/tasks.py` (`review_plan()` at line 302 — pattern to follow for new endpoint)
- `src/orchestrator/routers/agents.py` (existing agent endpoints)
- `alembic/versions/001_initial_schema.py` (migration pattern)

## Deliverables

### 1. `src/orchestrator/enums.py`

Add `NEEDS_CLARIFICATION` to `TaskStatus` enum after `PLANNING`:

```python
NEEDS_CLARIFICATION = "needs_clarification"  # human gate: answer agent questions
```

### 2. `src/orchestrator/state_machine.py`

Update `VALID_TRANSITIONS` — add `TaskStatus.NEEDS_CLARIFICATION` as a valid target from `PLANNING`, and add `NEEDS_CLARIFICATION -> [PLANNING]`:

```python
TaskStatus.PLANNING: [TaskStatus.PLAN_REVIEW, TaskStatus.NEEDS_CLARIFICATION, TaskStatus.FAILED],
TaskStatus.NEEDS_CLARIFICATION: [TaskStatus.PLANNING],
```

Add `TaskStatus.NEEDS_CLARIFICATION` to `HUMAN_GATES` set.

### 3. `src/orchestrator/models.py`

Add `"needs_clarification"` to the `Enum()` values in the Task `status` column definition (line 56), between `"planning"` and `"plan_review"`.

### 4. `alembic/versions/002_add_needs_clarification.py` (new file)

Create Alembic migration adding the enum value to PostgreSQL. Follow the pattern from `001_initial_schema.py`:

```python
"""Add needs_clarification status to taskstatus enum.

Revision ID: 002
Revises: 001
Create Date: 2026-03-03
"""
from typing import Sequence, Union
from alembic import op

revision: str = "002"
down_revision: Union[str, None] = "001"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None

def upgrade() -> None:
    op.execute("ALTER TYPE taskstatus ADD VALUE IF NOT EXISTS 'needs_clarification' AFTER 'planning'")

def downgrade() -> None:
    pass  # PostgreSQL does not support removing enum values
```

### 5. `src/orchestrator/schemas.py`

Add three new Pydantic models after `TestResult` (line 85):

```python
class ClarificationQuestion(BaseModel):
    """A single clarification question from the planner agent."""
    id: str
    question: str
    context: str | None = None


class ClarificationAnswer(BaseModel):
    """Human answer to a single clarification question."""
    question_id: str
    answer: str


class ClarificationSubmission(BaseModel):
    """Human submits answers to clarification questions."""
    answers: list[ClarificationAnswer]
```

Also add `plan_feedback` and `test_feedback` to `TaskResponse` (currently missing but the UI expects them):

```python
plan_feedback: str | None = None
test_feedback: str | None = None
```

And update `_task_response()` in `routers/tasks.py` to include these fields.

### 6. `src/orchestrator/orchestrator.py`

**a)** Add `TaskStatus.NEEDS_CLARIFICATION` to `HUMAN_OWNED_STATES` (line 40).

**b)** Modify `_on_planner_completed()` (line 718). Before checking for `plan.json`, check for `clarifications.json`:

```python
async def _on_planner_completed(
    self, session: AsyncSession, agent: Agent, task: Task
) -> None:
    """Handle planner agent completion: check for clarifications first, then plan.json."""
    worktree_path = Path(agent.worktree_path) if agent.worktree_path else None

    # Check for clarifications.json first
    if worktree_path:
        clarifications_path = worktree_path / "clarifications.json"
        if clarifications_path.exists():
            try:
                raw = clarifications_path.read_text()
                clarification_data = json.loads(raw)
                questions = clarification_data.get("questions", [])
                if questions:
                    await self._on_planner_asked_questions(session, agent, task, questions)
                    return
            except (json.JSONDecodeError, OSError):
                logger.exception(f"Task {task.id}: Failed to parse clarifications.json")

    # ... existing plan.json logic unchanged ...
```

**c)** Add new method `_on_planner_asked_questions()`:

```python
async def _on_planner_asked_questions(
    self,
    session: AsyncSession,
    agent: Agent,
    task: Task,
    questions: list[dict[str, Any]],
) -> None:
    """Handle planner agent producing clarification questions instead of a plan."""
    task.context = task.context or {}
    clarification_round = {
        "round": len(task.context.get("clarifications", [])) + 1,
        "questions": questions,
        "answers": None,
    }
    if "clarifications" not in task.context:
        task.context["clarifications"] = []
    task.context["clarifications"].append(clarification_round)

    # Clean up planner agent worktree
    if agent.worktree_branch:
        try:
            await self.worktree_manager.remove_agent_worktree(agent.worktree_branch)
        except Exception:
            logger.exception("Failed to remove planner agent worktree")

    agent.status = AgentStatus.IDLE.value
    agent.current_task_id = None
    task.assigned_agent_id = None

    event = transition_task(
        task,
        TaskStatus.NEEDS_CLARIFICATION,
        actor="orchestrator",
        details={"questions": questions, "round": clarification_round["round"]},
    )
    session.add(event)

    logger.info(
        f"Task {task.id}: PLANNING -> NEEDS_CLARIFICATION "
        f"({len(questions)} questions, round {clarification_round['round']})"
    )
    await self.broadcast_fn({
        "type": "clarification_needed",
        "task_id": str(task.id),
        "title": task.title,
        "questions": questions,
    })
```

### 7. `src/orchestrator/agent_prompt.py`

Replace `_planner_instructions()` (line 206-243). The updated version must:

**a)** If `task.context` has `"clarifications"`, render prior Q&A rounds at the top:

```python
clarifications = (task.context or {}).get("clarifications", [])
if clarifications:
    sections.append("## Prior Clarification Q&A")
    sections.append("")
    for round_data in clarifications:
        sections.append(f"### Round {round_data.get('round', '?')}")
        for q in round_data.get("questions", []):
            sections.append(f"**Q:** {q.get('question', '')}")
            if q.get("context"):
                sections.append(f"  _{q['context']}_")
        answers = round_data.get("answers")
        if answers:
            for ans in answers:
                sections.append(f"**A ({ans.get('question_id', '?')}):** {ans.get('answer', '')}")
        sections.append("")
    sections.append(
        "Use the above clarifications to inform your plan. Do NOT re-ask these questions."
    )
    sections.append("")
```

**b)** After the existing decomposition instructions, add a section about the clarification option:

```
### Asking Clarification Questions

If the task description is ambiguous or missing critical details,
you may ask clarification questions INSTEAD of producing a plan.
Write a JSON file at `clarifications.json` in the worktree root:

{
  "questions": [
    {
      "id": "q1",
      "question": "Should we use JWT or session-based auth?",
      "context": "The task mentions authentication but does not specify."
    }
  ]
}

Only ask questions when genuinely needed. If you have enough information,
produce the plan directly. Write either `clarifications.json` OR `plan.json`, not both.
```

**c)** Keep the existing plan.json format section unchanged.

### 8. `src/orchestrator/routers/tasks.py`

Add new endpoint `POST /{task_id}/clarifications`. Follow the exact pattern of `review_plan()` (line 302):

```python
@router.post("/{task_id}/clarifications")
async def answer_clarifications(
    task_id: uuid.UUID,
    body: ClarificationSubmission,
    db: AsyncSession = Depends(get_db),
) -> TaskResponse:
    """Human provides answers to planner agent's clarification questions."""
    # ... same query/404 pattern as review_plan ...
    # Validate status is NEEDS_CLARIFICATION
    # Store answers in task.context["clarifications"][-1]["answers"]
    # Use flag_modified(task, "context") to detect JSON mutation
    # Transition NEEDS_CLARIFICATION -> PLANNING
    # Create TaskEvent with event_type="clarification_answered"
    # Broadcast task_updated
```

Import `ClarificationSubmission` from schemas and `flag_modified` from `sqlalchemy.orm.attributes`.

Also update `_task_response()` to pass `plan_feedback` and `test_feedback`.

### 9. `src/orchestrator/routers/agents.py`

Add new endpoint `GET /{agent_id}/log`:

```python
@router.get("/{agent_id}/log")
async def get_agent_log(
    agent_id: uuid.UUID,
    db: AsyncSession = Depends(get_db),
) -> dict[str, str]:
    """Read the agent's log file and return contents."""
    stmt = select(Agent).where(Agent.id == agent_id)
    result = await db.execute(stmt)
    agent = result.scalar_one_or_none()
    if agent is None:
        raise HTTPException(status_code=404, detail="Agent not found")

    log_content = ""
    if agent.worktree_path:
        log_path = Path(agent.worktree_path) / ".claude" / "agent.log"
        if log_path.exists():
            log_content = log_path.read_text()

    return {"log": log_content}
```

Import `Path` from `pathlib`.

## Conventions

- Async everywhere (asyncio, async SQLAlchemy, async subprocess)
- Type hints on all public functions
- snake_case for functions/variables, PascalCase for classes
- `pathlib.Path` for file paths
- f-strings for formatting
- `ruff` for linting and formatting

## Build Verification

```bash
uv sync
uv run ruff check src/
uv run pytest
```
