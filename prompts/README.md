# Drem Orchestrator — Agent Prompts

## Overview

10 agent prompts to build a multi-agent orchestration system with a kanban task board UI,
human-in-the-loop plan review and manual testing gates, and integration with the existing
`wt` worktree workflow.

## Task Lifecycle

```
backlog → planning → plan_review → in_progress → testing_ready → manual_testing → merging → done
                        ↑ HUMAN       ↑                              ↑ HUMAN
                        └─ reject ────┘                              └─ fail → in_progress
```

## Prompt Index

| # | Name | Tier | Dependencies | New Files | Modified Files |
|---|------|------|-------------|-----------|----------------|
| 01 | Project Scaffold | 1 | — | pyproject.toml, CLAUDE.md, server.py, config.py, db.py, bootstrap.sh, ui skeleton | — |
| 02 | Data Model | 1 | — | models.py, enums.py, state_machine.py, schemas.py, alembic migration | — |
| 03 | Worktree Integration | 1 | — | worktree.py, git_utils.py | — |
| 04 | API Server | 2 | 02 | routers/tasks.py, routers/agents.py, routers/projects.py, routers/ws.py | server.py |
| 05 | Agent Runner | 2 | 02, 03 | agent_runner.py, agent_prompt.py | — |
| 06 | Orchestrator Core | 2 | 02, 03 | orchestrator.py, scheduler.py | — |
| 07 | Memory & Compaction | 2 | 02 | memory.py, compaction.py | — |
| 08 | Inter-Agent Messaging | 2 | 02, 05 | messaging.py, overlap_detector.py, heartbeat.py | — |
| 09 | Merge Orchestration | 2 | 03, 05 | merge.py | — |
| 10 | Task Board UI | 3 | 04 | types.ts, api.ts, hooks/, components/ (Board, TaskCard, TaskCreateDialog, AgentSidebar, TaskDetail) | App.tsx |

## Dependency Graph

```
Tier 1 (parallel):     01   02   03
                         \   |\ / |\
                          \  | X  | \
                           \ |/ \ |  \
Tier 2 (after T1):     04  05  06  07  08  09
                          \  \ \ | /   /
                           \  \ \|/   /
Tier 3 (after T2):           10
```

## Execution

### Tier 1 — All parallel, no dependencies

```bash
# These three can run simultaneously
claude --agent prompts/01-project-scaffold.md
claude --agent prompts/02-data-model.md
claude --agent prompts/03-worktree-integration.md
```

### Tier 2 — After Tier 1 merges

```bash
# These six can run simultaneously (each reads Tier 1 outputs)
claude --agent prompts/04-api-server.md
claude --agent prompts/05-agent-runner.md
claude --agent prompts/06-orchestrator-core.md
claude --agent prompts/07-memory-compaction.md
claude --agent prompts/08-inter-agent-messaging.md
claude --agent prompts/09-merge-orchestration.md
```

### Tier 3 — After Tier 2 merges

```bash
claude --agent prompts/10-task-board-ui.md
```

## Human Gates

Two points in the task lifecycle require explicit human approval:

1. **Plan Review** (after `planning` → before `in_progress`): The orchestrator decomposes a
   high-level task and presents the plan in the UI. You approve or reject with feedback.

2. **Manual Testing** (after `testing_ready` → before `merging`): All agent work is merged into
   the feature branch. The UI shows a test plan with steps. You test manually and pass or fail
   with feedback.

Both gates block all automated progress until the human acts.
