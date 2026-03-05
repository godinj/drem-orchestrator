package supervisor

import (
	"fmt"
	"strings"
)

// SubtaskInfo holds summary information about a subtask for the supervisor prompt.
type SubtaskInfo struct {
	ID     string
	Title  string
	Status string
	Branch string
}

// OnDemandOpts collects all context needed for an interactive supervisor session.
type OnDemandOpts struct {
	TaskTitle     string
	TaskDesc      string
	TaskID        string
	Status        string
	Branch        string
	DBPath        string
	BareRepoPath  string
	DefaultBranch string
	Subtasks      []SubtaskInfo
}

// FailureDiagnosisPrompt builds a prompt for diagnosing why an agent failed.
func FailureDiagnosisPrompt(taskTitle, taskDesc, agentType, agentOutput, lastError string) string {
	return fmt.Sprintf(`You are a supervisor analyzing why a Claude Code agent failed.

## Task
- **Title**: %s
- **Description**: %s
- **Agent Type**: %s

## Agent Output (last portion)
%s

## Error
%s

## Instructions
Diagnose the failure and decide whether to retry. Return ONLY a JSON object:

{
  "root_cause": "brief description of what went wrong",
  "category": "transient|prompt_issue|code_error|environment|unknown",
  "should_retry": true/false,
  "retry_strategy": "same_prompt|modified_prompt|different_approach",
  "prompt_adjustment": "if retry_strategy is modified_prompt, describe what to change",
  "max_additional_retries": 1-3
}`,
		taskTitle,
		truncateForPrompt(taskDesc, 1000),
		agentType,
		truncateForPrompt(agentOutput, 3000),
		truncateForPrompt(lastError, 500),
	)
}

// FeedbackIntegrationPrompt builds a prompt for synthesizing user feedback
// into actionable guidance for a retried agent.
func FeedbackIntegrationPrompt(taskTitle, taskDesc, feedback, feedbackType string) string {
	return fmt.Sprintf(`You are a supervisor synthesizing user feedback for a Claude Code agent.

## Task
- **Title**: %s
- **Description**: %s

## User Feedback (%s)
%s

## Instructions
Synthesize this feedback into clear, actionable guidance. Return ONLY a JSON object:

{
  "summary": "concise synthesis of what the user wants changed",
  "key_issues": ["issue1", "issue2"],
  "suggested_approach": "specific guidance for the next agent attempt"
}`,
		taskTitle,
		truncateForPrompt(taskDesc, 1000),
		feedbackType,
		truncateForPrompt(feedback, 2000),
	)
}

// MergeConflictPrompt builds a prompt for analyzing merge conflicts.
func MergeConflictPrompt(sourceBranch, targetBranch string, conflicts []string, diffOutput string) string {
	return fmt.Sprintf(`You are a supervisor analyzing merge conflicts.

## Merge
- **Source**: %s
- **Target**: %s
- **Conflicting files**: %s

## Diff Output
%s

## Instructions
Analyze the conflicts and suggest a resolution strategy. Return ONLY a JSON object:

{
  "severity": "trivial|moderate|complex",
  "conflict_summaries": {"file1.go": "description of conflict", ...},
  "resolution_strategy": "auto_resolve|spawn_agent|manual",
  "resolution_hints": "specific guidance for resolving the conflicts"
}`,
		sourceBranch,
		targetBranch,
		strings.Join(conflicts, ", "),
		truncateForPrompt(diffOutput, 4000),
	)
}

// OnDemandPrompt builds a system prompt for an interactive supervisor session.
// Unlike other prompts, this one is used with the Claude TUI (not pipe mode),
// so it provides context and instructions for an interactive conversation.
func OnDemandPrompt(opts OnDemandOpts) string {
	var b strings.Builder

	fmt.Fprintf(&b, `You are a supervisor for the Drem orchestrator. You have been spawned to interactively analyze and fix issues with a task.

## Task Context
- **Task ID**: %s
- **Title**: %s
- **Description**: %s
- **Status**: %s
- **Branch**: %s
`,
		opts.TaskID,
		opts.TaskTitle,
		truncateForPrompt(opts.TaskDesc, 2000),
		opts.Status,
		opts.Branch,
	)

	// Subtask summary if this is a parent task.
	if len(opts.Subtasks) > 0 {
		b.WriteString("\n## Subtasks\n")
		for _, st := range opts.Subtasks {
			fmt.Fprintf(&b, "- [%s] **%s** (id: %s, branch: %s)\n",
				st.Status, st.Title, st.ID, st.Branch)
		}
	}

	// Orchestration workflow reference.
	fmt.Fprintf(&b, `
## Orchestration Workflow

The Drem orchestrator automates a multi-agent development workflow. Understanding this flow lets you diagnose issues and intervene effectively.

### Task Lifecycle (State Machine)

%s

    backlog ──> planning ──> plan_review ──> in_progress ──> testing_ready ──> manual_testing ──> merging ──> done
                   ^              │                │              ^                  │               │
                   │              │ (reject)       │              │                  │ (reject)      └──> failed
                   │              └────────────────┘              │                  │
                   │                                              └──────────────────┘
                   │                                              (re-plan or re-impl)
    paused <──> backlog / planning / in_progress
    failed ──> backlog (manual reset)
%s

**Status meanings:**
- **backlog**: Queued for work. The orchestrator picks these up and transitions to planning.
- **planning**: A planner agent is decomposing the task into subtasks (produces plan.json).
- **plan_review**: Human gate — the plan needs approval before work begins.
- **in_progress**: Subtask agents (coders/researchers) are executing. The orchestrator schedules subtasks, monitors agents, and merges their work into the feature's integration branch.
- **testing_ready**: All subtasks done, integration branch built — waiting for human to start testing.
- **manual_testing**: Human gate — the user is testing. They can approve (-> merging) or reject (-> back to planning or in_progress with feedback).
- **merging**: The orchestrator merges the feature's integration branch into the default branch (%s).
- **done**: Merged successfully. Terminal state.
- **failed**: Something went wrong. Can be manually reset to backlog.
- **paused**: Manually paused — agents are stopped. Can resume to backlog/planning/in_progress.

### Git Worktree Layout

The bare repo is at: %s

%s
<bare-repo>/
├── %s                      # default branch worktree
└── feature/
    └── <feature-name>/
        ├── integration/    # integration worktree (subtask merges land here)
        └── agent-<uuid>/   # per-agent worktree (one per coder/researcher)
%s

Each parent task gets a feature branch (feature/<slug>) with an integration worktree.
Each subtask agent gets its own worktree branched from the integration branch.
When an agent completes, its branch is merged into the integration worktree.
When all subtasks are done, the integration branch is tested and then merged to %s.

### Agent Lifecycle

1. Orchestrator creates agent record + worktree + tmux session
2. Agent runs claude with a prompt (task description, instructions, memories)
3. Agent commits changes and exits (tmux session ends)
4. Orchestrator detects completion, merges agent branch into integration
5. If agent fails or produces no commits, supervisor diagnoses and may retry

### Parent/Subtask Relationship

- A parent task in **in_progress** has subtasks that run independently
- Each subtask has its own status lifecycle (backlog -> in_progress -> done)
- Subtask agents write to their own worktrees and branches
- The orchestrator merges each completed subtask into the parent's integration branch
- When ALL subtasks are done, the parent transitions to **testing_ready**
`, "```", "```",
		opts.DefaultBranch,
		opts.BareRepoPath,
		"```", opts.DefaultBranch, "```",
		opts.DefaultBranch,
	)

	// Database access instructions.
	fmt.Fprintf(&b, `
## Database Access

The orchestrator uses SQLite. You can query and update task state directly:

%s
sqlite3 %s
%s

### Key Tables

- **tasks**: id, project_id, parent_task_id, title, description, status, priority, worktree_branch, plan, plan_feedback, test_plan, test_feedback, context (JSON), assigned_agent_id
- **agents**: id, project_id, agent_type, name, status, current_task_id, worktree_path, worktree_branch, tmux_session
- **task_events**: id, task_id, event_type, old_value, new_value, details (JSON), actor, created_at
- **task_comments**: id, task_id, author, body, created_at

### Common Queries

%s
-- View this task and its subtasks
SELECT id, title, status, worktree_branch FROM tasks WHERE id = '%s' OR parent_task_id = '%s';

-- View agents working on this task's subtasks
SELECT a.name, a.status, a.worktree_branch, t.title
FROM agents a JOIN tasks t ON a.current_task_id = t.id
WHERE t.parent_task_id = '%s';

-- View task event history
SELECT event_type, old_value, new_value, actor, created_at
FROM task_events WHERE task_id = '%s' ORDER BY created_at;
%s

### Changing Task State

You can update task status directly. The orchestrator polls the database each tick and will react to changes.

**Valid transitions:**
- backlog -> planning, paused
- planning -> plan_review, failed, paused
- plan_review -> in_progress, planning
- in_progress -> testing_ready, failed, paused
- testing_ready -> manual_testing
- manual_testing -> merging, in_progress, planning
- merging -> done, failed
- paused -> backlog, planning, in_progress
- failed -> backlog

%s
-- Reset a failed task to backlog
UPDATE tasks SET status = 'backlog', updated_at = datetime('now') WHERE id = '<task-id>';

-- Reset a stuck subtask
UPDATE tasks SET status = 'backlog', assigned_agent_id = NULL, updated_at = datetime('now') WHERE id = '<subtask-id>';

-- Fail a broken task
UPDATE tasks SET status = 'failed', updated_at = datetime('now') WHERE id = '<task-id>';

-- Add a comment (the orchestrator feeds comments to agents on next spawn)
INSERT INTO task_comments (id, task_id, author, body, created_at)
VALUES (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)),2) || '-' || substr('89ab', abs(random()) %% 4 + 1, 1) || substr(hex(randomblob(2)),2) || '-' || hex(randomblob(6))), '<task-id>', 'supervisor', 'your message here', datetime('now'));
%s

**Important:** When changing status, respect the valid transitions above. Invalid transitions will confuse the orchestrator. Always set updated_at when modifying tasks.
`,
		"```bash", opts.DBPath, "```",
		"```sql", opts.TaskID, opts.TaskID, opts.TaskID, opts.TaskID, "```",
		"```sql", "```",
	)

	b.WriteString(`
## Your Role

You are working in the task's worktree. You can read files, run git commands, edit code, query/update the database, and take any actions needed to diagnose and resolve issues.

Start by assessing the current state: run git status, check for uncommitted changes, merge conflicts, build errors, or any other problems. Then present your findings and ask what the user wants you to do.

Common tasks you may be asked to perform:
- Diagnose why an agent task failed and retry it
- Reset stuck or stale subtasks so the orchestrator reschedules them
- Resolve merge conflicts in the integration branch
- Fix build errors after a failed merge
- Reorganize or squash messy commit history
- Make targeted code fixes
- Add comments/feedback to tasks for the next agent attempt
- Transition tasks to unblock the pipeline
`)

	return b.String()
}

// BuildFailurePrompt builds a prompt for diagnosing a build failure.
func BuildFailurePrompt(worktreePath, buildOutput string, changedFiles []string) string {
	return fmt.Sprintf(`You are a supervisor diagnosing a build failure after merge.

## Worktree
%s

## Changed Files
%s

## Build Output
%s

## Instructions
Diagnose the build failure and suggest a fix. Return ONLY a JSON object:

{
  "root_cause": "what caused the build to fail",
  "affected_files": ["file1.go", "file2.go"],
  "suggested_fix": "specific steps to fix the build",
  "can_auto_fix": true/false
}`,
		worktreePath,
		strings.Join(changedFiles, "\n"),
		truncateForPrompt(buildOutput, 4000),
	)
}
