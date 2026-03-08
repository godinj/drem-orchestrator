# Merge & Agent Lifecycle Reliability — Findings Report

**Date**: 2026-03-07
**Source**: Analysis of 27 journal entries (`journals/`) and 6 integration worktree journals (`drem-canvas.git/feature/*/integration/supervisor-journal.md`)
**Scope**: All supervisor interventions across 6 feature tasks on the drem-canvas project

---

## Executive Summary

The orchestrator's merge pipeline and agent lifecycle management are the dominant source of task failures. Across 6 feature tasks, every single one required manual supervisor intervention. The supervisor spent the vast majority of its time performing the same manual merge-and-DB-update cycle repeatedly. The fixes needed have been documented by supervisors 5+ times but never implemented.

**Key stats:**
- 6/6 feature tasks required manual supervisor merge intervention
- Automation Lanes: 0% orchestrator merge success (0/11 subtasks merged automatically, 7 consecutive supervisor sessions)
- Clip Editing: 7/10 subtasks failed on merge
- MIDI Clip Preview: 6/6 subtasks failed on merge
- Waveform Rendering: 2/7 subtasks failed on merge
- Track Freeze & Bounce: 3/6 subtasks failed on merge
- Clip Lifecycle: orchestrator retried already-merged work, causing new conflicts

---

## Problem 1: Merge Conflicts from Parallel Agents

### Description

All subtasks for a feature are scheduled concurrently from the same integration branch base commit. When agents touch overlapping files — which is common since feature subtasks naturally share model files, vim command files, and widget files — the orchestrator cannot auto-merge subsequent agent branches.

### Evidence

**Waveform Rendering**: 7 subtasks ran in parallel, 4 modified overlapping files (`WaveformCache.h`, `WaveformWidget.cpp/h`, `Theme.h`, `Canvas.h/cpp`). First 3 merged; the rest conflicted.

**MIDI Clip Preview**: 6 agents launched in parallel, all touching `Project.h`, `MidiClipWidget.h`, `VimEngine.cpp`, `VimContext.h`. Two agents produced work that was a pure subset of other agents' work.

**Clip Editing**: 7/10 subtasks failed. All agents diverged from `2ff62c4` while the integration branch advanced with earlier merges. Shared files: `AudioClip.h/cpp`, `MidiClip.h/cpp`, `Project.h`, `VimEngine.cpp`, `EditorAdapter.cpp/h`, `VimGrammar.h/cpp`, `ActionRegistry.h`, `default_keymap.yaml`.

**Track Freeze & Bounce**: 3/6 failed. Freeze/Unfreeze, Tests, and GUI subtasks re-implemented overlapping code in `Track.h/cpp`, `Project.h`, `TrackFreezeRenderer.h/cpp`.

### Supervisor resolution pattern

Every successful manual merge followed the same steps:
1. Identify all agent branches and their commits
2. Analyze overlap to determine merge order (most foundational/comprehensive first)
3. Skip agents whose work is a pure subset of another agent
4. Merge sequentially, resolving conflicts (usually trivial: keep both sides for additive changes, deduplicate identical additions)
5. Build and test after each merge
6. Update DB

### Conflict characteristics

The conflicts are almost always **additive** — multiple agents appending code to the same file section (end of a function, end of an include list, end of a method declaration block). These are mechanically resolvable without semantic understanding: keep all additions, deduplicate identical lines.

---

## Problem 2: Orchestrator Fails on Clean Merges

### Description

A separate failure mode where the orchestrator's merge step fails even when there are zero conflicts. The merge is clean (fast-forward or trivially mergeable), yet the orchestrator marks the subtask as `"merge into feature branch failed, agent branch preserved"`.

### Evidence

**Automation Lanes**: This happened on every single subtask — 11/11. The supervisor manually performed each merge and confirmed they were all clean (no conflicts). In one session the supervisor explicitly ran `git merge --no-commit --no-ff <branch>` and it succeeded immediately.

The supervisor documented this escalating across 7 consecutive sessions:
- Session 2: "merge was clean (fast-forward, merge base was integration HEAD)"
- Session 3: "all 4 build clean... all clean merges"
- Session 5: "tested merge... clean merge, no conflicts"
- Session 6: "The orchestrator's merge step consistently fails on clean merges"
- Session 7: "Every single subtask in this feature required manual merge intervention. The orchestrator has a 0% success rate."

### Likely root causes (from supervisor analysis)

1. The orchestrator runs `git merge` in the wrong working directory (agent worktree instead of integration worktree)
2. The agent branch ref isn't visible in the integration worktree at merge time (missing `git fetch` or ref resolution issue in a bare repo)
3. Race condition between agent completion detection and merge attempt
4. Branch name resolution fails — the orchestrator may need the full ref path in a bare repo context

### Critical gap

The orchestrator does not log the actual git error output when merge fails. The failure reason is always the generic string `"merge into feature branch failed, agent branch preserved"` with no underlying git stderr. This makes diagnosis impossible without manual investigation.

---

## Problem 3: Agent Completion Detection Is Unreliable

### Description

The orchestrator fails to detect when agents have finished their work. Agents complete, commit, and exit their tmux sessions, but the orchestrator never triggers the merge step.

### Evidence

**Automation Lanes session 3 (20:00)**: 4 agents had all completed with clean builds and correct changes. No agent branches were merged. "The orchestrator failed to detect that agent tmux sessions had finished and didn't trigger the merge step."

**Automation Lanes session 4 (22:00)**: 2 more agents completed but weren't detected. "Same recurring issue — the orchestrator fails to detect agent completion."

**Automation Lanes session 5 (01:30)**: Agent committed work, orchestrator marked subtask as `failed` instead of attempting merge. "5th consecutive session with the same merge detection failure."

### A distinct sub-failure: agents never spawned

**Automation Lanes session 4 (23:30)**: Two subtasks transitioned to `in_progress` but no agents were ever created. "No agent records were created in the DB, no worktrees were set up, and no tmux sessions were started. The subtasks sat idle until timeout marked them as failed."

---

## Problem 4: Destructive Retry Logic

### Description

The orchestrator retries failed subtasks without checking whether their prior work was already merged into the integration branch. The new agent re-does the same work, which conflicts with the already-merged version.

### Evidence

**Clip Lifecycle**: "Two subtasks were marked `failed`, but the code from both was already successfully merged into the integration branch. The orchestrator retried them and the new agent branches had merge conflicts with the already-merged work."

**MIDI Clip Preview (code agent entries)**: Two code agents were spawned for subtasks whose work already existed. One "exited immediately without performing any work" and the other found "nothing to do" — both marked as failures.

---

## Problem 5: Planner Misalignment

### Description

The planner agent generates subtask plans that don't match the parent task's actual requirements, or that lack cross-cutting integration concerns.

### Evidence

**MIDI Clip Preview**: "The planner decomposed 'MIDI Clip Preview' into clip lifecycle subtasks (naming, split, join, delete, creation, tests) instead of the actual MIDI preview rendering feature described in the acceptance criteria. None of the 6 subtasks addressed miniature piano-roll preview, display modes, or piano roll open/close interaction."

**Automation Lanes**: After all 11 subtasks were merged and all tests passed, user testing found the feature completely non-functional. "The original subtask implementations each worked in isolation but the cross-cutting wiring between VimContext state and the widget rendering layer was never implemented." Two missing wiring calls prevented any visual output.

### Follow-up planning issue

When the MIDI Clip Preview task was reset to `backlog` for replanning, the planner saw 6 existing `done` subtasks and auto-advanced without generating a new plan (plan field was NULL). The old subtasks had to be manually detached before replanning would work.

---

## Problem 6: Prompt Delivery Truncation

### Description

Agent prompts are being truncated mid-sentence, leaving agents with incomplete or no actionable instructions.

### Evidence

**MIDI Clip Preview — Implement clip join operation (16:51)**: "Agent session started but exited immediately without performing any work. The task description appears truncated in the output."

**MIDI Clip Preview — Implement clip join operation (17:00)**: "Task description was truncated mid-sentence in the prompt delivered to the agent, so it lacked the detailed implementation spec (adjacency validation, audio simple join logic, MIDI concatenation, mixed-type handling). The agent had no actionable instructions."

---

## Problem 7: Parent Task Status Cascading

### Description

The parent task transitions to `failed` as soon as any subtask fails, even while other subtasks are still `in_progress` with agents actively running. This orphans running agents and prevents the task from completing naturally.

### Evidence

**Waveform Rendering**: "Two other subtasks were still in_progress with agents running, but the parent was already marked failed."

**Automation Lanes**: Multiple sessions found the parent in `failed` while agents were actively committing correct work.

### State machine issues

- `failed → backlog` triggers the full lifecycle including replanning, which gates on human approval even when the existing plan is fine. For resuming, `failed → in_progress` should be used as a supervisor override.
- Setting status to `in_progress` via DB gets overwritten by the orchestrator on next poll if it sees all remaining subtasks as `done` — it re-sets to `testing_ready`.

---

## Problem 8: Supervisor Stub Entries (Noise)

### Description

22 of 27 entries in `journals/` are identical boilerplate stubs — "Interactive supervisor session spawned" with no findings, actions, or outcomes. Meanwhile, the real substantive journals are written into the integration worktrees, which are ephemeral.

### Evidence

The `journals/` directory has 22 entries like:
```
# 2026-03-07 16:36:45 — on_demand_session
- **Agent**: supervisor
- **Task**: Automation Lanes & Modes (f8fc2547-...)
- **Summary**: Interactive supervisor session spawned
- **Status**: failed
- **Outcome**: Session started — supervisor will document findings in this journal
```

All real work is logged in `drem-canvas.git/feature/*/integration/supervisor-journal.md`, which will be lost when worktrees are cleaned up.

---

## Summary of Recommended Fixes (from supervisor journals)

These recommendations appear repeatedly across multiple journals. The number indicates how many independent journal entries suggest each fix.

### Merge Pipeline

| # | Recommendation | Occurrences |
|---|---------------|-------------|
| 1 | **Dependency-aware scheduling**: serialize subtasks that touch the same files | 5/6 features |
| 2 | **Rebase-before-merge**: rebase agent branch onto current integration HEAD before merging | 4/6 features |
| 3 | **Log actual git error output** on merge failure (currently a generic string) | 3/6 features |
| 4 | **Conflict resolution agent**: spawn a merge-resolution agent instead of immediately failing | 3/6 features |
| 5 | **Merge order optimization**: merge most comprehensive agent first, skip pure subsets | 2/6 features |
| 6 | **Don't fail parent while subtasks are in_progress** | 2/6 features |
| 7 | **Retry merge at least once** (with `git fetch` first) before marking failed | 2/6 features |
| 8 | **Periodic sweep for unmerged agent commits** independent of session detection | 2/6 features |

### Agent Lifecycle

| # | Recommendation | Occurrences |
|---|---------------|-------------|
| 9 | **Verify agent record + worktree exist** shortly after `in_progress` transition | 1/6 features |
| 10 | **Check if work is already merged** before retrying a failed subtask | 2/6 features |
| 11 | **Fallback completion detection** beyond tmux (poll for commits, sentinel files) | 2/6 features |

### Planning

| # | Recommendation | Occurrences |
|---|---------------|-------------|
| 12 | **Cross-reference acceptance criteria** against subtasks during planning | 1/6 features |
| 13 | **Add integration wiring subtask** for cross-cutting concerns | 1/6 features |
| 14 | **Auto-detach old subtasks** when replanning with `plan_feedback` | 1/6 features |
| 15 | **Schedule tests last** after all implementation subtasks merge | 1/6 features |

---

## Appendix: Journal Source Inventory

### Main repo journals (`drem-orchestrator.git/journals/`)
- 22 supervisor stub entries (session-spawned, no content)
- 2 substantive supervisor entries (`supervisor-journal1.md`, `supervisor-20260307-192300.md`)
- 4 code agent empty-work diagnosis entries

### Integration worktree journals (`drem-canvas.git/feature/*/integration/supervisor-journal.md`)
- **Automation Lanes** (`f8fc2547`): 8 session entries, most substantive — covers merge failures, agent spawn failures, plan revision, state machine issues, user testing bug fix
- **Waveform Rendering** (`ca8352b9`): 1 entry — parallel agent merge conflict resolution
- **Clip Lifecycle** (`9eb98010`): 1 entry — merging state with no local master branch
- **Clip Editing** (`4ce28240`): 2 entries — merge conflict resolution + slip hotkey post-merge bug fix
- **MIDI Clip Preview** (`590e8a0c`): 2 entries — planner misalignment + merge conflict resolution
- **Track Freeze & Bounce** (`d6af2685`): 1 entry — parallel agent merge conflict resolution
