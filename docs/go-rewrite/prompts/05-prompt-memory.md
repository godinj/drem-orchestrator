# Agent: Prompt Generation & Memory

You are working on the `feature/go-rewrite` branch of Drem Orchestrator, a multi-agent task orchestration system being rewritten from Python to Go.
Your task is to implement agent prompt generation and the memory persistence/compaction system.

## Context

Read these before starting:
- `docs/go-rewrite/DESIGN.md` (focus on "Prompt Generation" section)
- `src/orchestrator/agent_prompt.py` (Python prompt generation to port — generate_agent_prompt, _planner_instructions, _coder_instructions, _researcher_instructions, write_prompt_file)
- `src/orchestrator/memory.py` (Python memory manager to port — MemoryManager class, store/get/compact/extract)
- `src/orchestrator/compaction.py` (Python orchestrator compaction — OrchestratorSnapshot, save/restore state, should_compact)
- `internal/model/models.go` (Go models — Task, Project, Agent, Memory, SubtaskPlan, enums)
- `CLAUDE.md` (build commands, conventions)

## Dependencies

This agent depends on Agent 01 (Models/DB). If `internal/model/` doesn't exist yet, create minimal stubs with the types you need.

## Deliverables

### New file: `internal/prompt/prompt.go`

Port `agent_prompt.py` — builds markdown prompt strings for Claude Code agents.

```go
package prompt

import (
    "fmt"
    "os"
    "path/filepath"
    "strings"

    "github.com/godinj/drem-orchestrator/internal/model"
)

// Opts contains all inputs needed to generate an agent prompt.
type Opts struct {
    Task         *model.Task
    Project      *model.Project
    AgentType    model.AgentType
    WorktreePath string
    Memories     []model.Memory
    ParentCtx    map[string]any
}

// Generate builds a full markdown prompt for a Claude Code agent.
func Generate(opts Opts) string
```

The generated prompt should be structured markdown with these sections:

1. **Role & Task** — "You are a {agentType} agent working on: {task.Title}"
2. **Project Context** — Project name, description, bare repo path
3. **Task Details** — Full task description, parent task context if subtask
4. **Worktree Info** — Working directory path, branch name
5. **Agent-Type Instructions** — type-specific instructions (see below)
6. **Prior Context** — Agent memories if any
7. **Build & Verify** — Build/test commands from CLAUDE.md if readable

#### Planner Instructions

When `agentType == AgentPlanner`:

```markdown
## Instructions

You are a planner agent. Decompose this task into implementable subtasks.

Analyze the codebase and produce a `plan.json` file in the working directory root with this format:

\```json
{
  "subtasks": [
    {
      "title": "Short descriptive title",
      "description": "Detailed implementation description",
      "agent_type": "coder",
      "files": ["path/to/file1.go", "path/to/file2.go"],
      "dependencies": [],
      "priority": 1
    }
  ]
}
\```

Rules:
- Each subtask should be independently implementable by one agent
- List specific files each subtask will create or modify
- Set dependencies between subtasks where order matters (use 0-based indices)
- Use agent_type "coder" for implementation, "researcher" for investigation
- Priority 1 = highest priority
```

#### Coder Instructions

When `agentType == AgentCoder`:

```markdown
## Instructions

You are a coder agent. Implement the described task.

Files to create/modify: {task.Context["estimated_files"] if present}

After implementation:
1. Run the build command to verify compilation
2. Run tests if applicable
3. Commit your changes with a descriptive message
4. Do NOT push to remote
```

If `task.TestPlan` is set, include it as "## Test Plan\n{testPlan}".

#### Researcher Instructions

When `agentType == AgentResearcher`:

```markdown
## Instructions

You are a researcher agent. Investigate the topic and document findings.

Write your findings to `research-report.md` in the working directory.

Structure your report with:
1. Summary of findings
2. Detailed analysis
3. Recommendations
4. References to relevant files/code
```

#### `WritePromptFile(worktreePath, prompt string) (string, error)`

Write the prompt to `<worktree>/.claude/agent-prompt.md`, creating directories as needed. Return the full path.

### New file: `internal/memory/memory.go`

Port `MemoryManager` from Python:

```go
package memory

import (
    "time"

    "github.com/google/uuid"
    "gorm.io/gorm"

    "github.com/godinj/drem-orchestrator/internal/model"
)

// Manager handles agent memory persistence and retrieval.
type Manager struct {
    db *gorm.DB
}

// NewManager creates a MemoryManager.
func NewManager(db *gorm.DB) *Manager
```

#### `StoreMemory(agentID uuid.UUID, content, memoryType string, taskID *uuid.UUID, metadata map[string]any) (*model.Memory, error)`

Create and save a Memory record. Set CreatedAt to now.

#### `GetMemories(agentID *uuid.UUID, taskID *uuid.UUID, memoryType string, limit int) ([]model.Memory, error)`

Query memories with optional filters. Order by CreatedAt DESC. If limit <= 0, default to 50.

#### `GetProjectMemories(projectID uuid.UUID, memoryTypes []string, limit int) ([]model.Memory, error)`

Get memories across all agents in a project. Join through Agent table on ProjectID.

#### `CompactAgentMemory(agentID uuid.UUID) (string, error)`

Compact an agent's memories into a summary:

1. Get all non-archived memories for the agent
2. Group by MemoryType
3. Build a summary string:
   ```
   ## Decisions
   - <decision 1>
   - <decision 2>

   ## File Changes
   - <change 1>

   ## Lessons
   - <lesson 1>
   ```
4. Update the Agent's MemorySummary field in DB
5. Rename old memory types to `archived_<type>` (update in DB)
6. Return the summary

#### `BuildAgentContext(agentID, taskID uuid.UUID, maxTokens int) (string, error)`

Build context string for prompt injection:

1. Get agent's MemorySummary from DB
2. Get recent task-specific memories (last 20)
3. Get project-wide decisions and lessons (last 10)
4. Concatenate, estimating ~4 chars per token
5. Truncate if over maxTokens (default 8000)

#### `ExtractMemoriesFromOutput(agentID, taskID uuid.UUID, output string) ([]model.Memory, error)`

Parse agent output for structured memories using regex patterns:

```go
var decisionPatterns = []string{
    `(?i)(?:decided to|chose|approach:)\s*(.+)`,
}
var blockerPatterns = []string{
    `(?i)(?:blocked by|need|waiting for)\s*(.+)`,
}
var fileChangePatterns = []string{
    `(?i)(?:created|modified|updated|deleted)\s+(?:file\s+)?(\S+\.\w+)`,
}
var completionPatterns = []string{
    `(?i)(?:completed|finished|done:)\s*(.+)`,
}
```

For each pattern match, call `StoreMemory` with the appropriate type. Return all created memories.

### New file: `internal/memory/compaction.go`

Port orchestrator state checkpointing:

```go
package memory

import (
    "github.com/google/uuid"
    "gorm.io/gorm"

    "github.com/godinj/drem-orchestrator/internal/model"
)

// OrchestratorSnapshot represents a checkpoint of orchestrator state.
type OrchestratorSnapshot struct {
    ActiveTasks    []uuid.UUID
    PendingReviews []uuid.UUID
    ActiveAgents   []uuid.UUID
    StaleAgents    []uuid.UUID
    LastCheckpoint time.Time
}

// SaveOrchestratorState saves a checkpoint of the current orchestrator state.
func SaveOrchestratorState(db *gorm.DB, orchestratorAgentID uuid.UUID) error

// RestoreOrchestratorState loads the last checkpoint and reconciles stale agents.
func RestoreOrchestratorState(db *gorm.DB, orchestratorAgentID uuid.UUID) (*OrchestratorSnapshot, error)

// ShouldCompact returns true if an agent's memory count exceeds thresholds.
// Threshold: memory_count > 50 * compactionThreshold OR estimated tokens > 8000 * compactionThreshold
func ShouldCompact(db *gorm.DB, agentID uuid.UUID, compactionThreshold float64) (bool, error)
```

`SaveOrchestratorState`:
1. Query active tasks (BACKLOG, PLANNING, IN_PROGRESS, MERGING)
2. Query pending reviews (PLAN_REVIEW, MANUAL_TESTING)
3. Query active agents (WORKING status)
4. Store as a Memory with type "orchestrator_state" and metadata containing the snapshot

`RestoreOrchestratorState`:
1. Load latest "orchestrator_state" memory
2. Parse snapshot from metadata
3. Check heartbeats for active agents — mark stale ones

`ShouldCompact`:
1. Count non-archived memories for the agent
2. Estimate tokens as `sum(len(content)) / 4`
3. Return true if either threshold exceeded

## Conventions

- Go 1.22+, standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Build verification: `go build ./... && go vet ./...`
