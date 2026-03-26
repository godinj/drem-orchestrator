# Plan: Metrics Capture, A/B Experiments & Grafana Dashboard

> Source PRD: docs/prd-metrics-and-experiments.md

## Architectural decisions

Durable decisions that apply across all phases:

- **Schema**: Agent model enriched with `ModelID`, `Effort`, `CompletedAt`, `ExitReason`, `TotalCostUSD`, `FinalContextPct`. New `Metric` model (time-series, keyed by AgentID + Timestamp + Name). New `Experiment` model (owns variants, tracks status). New `Variant` model (links experiment to task + profile).
- **Packages**: `internal/metrics` for time-series store and query API. `internal/experiment` for experiment lifecycle management.
- **Config**: Profiles defined under `[profiles.<name>]` in `drem.toml`. Resolution via `ForAgentTypeWithProfile()` layering: profile override -> default agent config -> hardcoded defaults.
- **Grafana**: Docker Compose with Grafana OSS + SQLite datasource plugin. Reads `drem.db` directly (mounted volume). Pre-built dashboard JSON templates provisioned automatically.
- **CLI**: `drem experiment create --profiles X,Y --default X`, `drem experiment from-task <id> --profile X`.
- **Metrics collection**: Additive change to existing `contextMonitorLoop` in `internal/agent/monitor.go` -- append to metrics table alongside existing `Agent.Config` overwrite.
- **Agent pool**: Experiments own the entire agent pool. Normal task processing paused while experiments active. Pool split evenly across active variants.

---

## Phase 1: Agent Record Enrichment

**User stories**: 1 (cost after completion), 2 (model and effort per agent), 5 (duration recorded), 6 (exit reasons persisted)

### What to build

Enrich the Agent GORM model with six new fields that capture model identity at spawn time and outcome data at completion time. Populate ModelID and Effort when the agent is created in `runner.SpawnAgent()` from the resolved `AgentCLIConfig`. Populate CompletedAt, ExitReason, TotalCostUSD, and FinalContextPct in `processAgentResult()` from the context monitor's last reading and the exit info. Update the TUI agent panel to display model ID and cost columns. After this phase, `SELECT model_id, effort, total_cost_usd, exit_reason FROM agents` returns real data for every completed agent.

### Acceptance criteria

- [ ] Agent model has ModelID, Effort, CompletedAt, ExitReason, TotalCostUSD, FinalContextPct fields with GORM tags
- [ ] ModelID and Effort populated at agent spawn from resolved config
- [ ] CompletedAt, ExitReason, TotalCostUSD, FinalContextPct populated on agent completion
- [ ] TUI agent panel shows model ID and cost for each agent
- [ ] Unit tests verify field population at spawn and completion using testutil factories
- [ ] Documentation: README or walkthrough updated to describe new agent fields

---

## Phase 2: Time-Series Metrics

**User stories**: 3 (context usage curve over time), 26 (metrics retained indefinitely)

### What to build

Create the `internal/metrics` package with a Metric GORM model (ID, AgentID, Timestamp, Name, Value, Labels). Add a `Store` type with `Record(agentID, name, value, labels)` and `Query(agentID, name, timeRange)` methods. Modify the existing `contextMonitorLoop` to call `Store.Record()` for each sampled value (context_pct, cost_usd, token_input, token_output, tool_count) every 5 seconds, in addition to the existing `Agent.Config` overwrite. Add the Metric model to GORM auto-migration. After this phase, historical time-series data accumulates in the metrics table and is queryable by agent ID, metric name, and time range.

### Acceptance criteria

- [ ] Metric model with ID, AgentID, Timestamp, Name, Value, Labels fields and appropriate indexes
- [ ] `internal/metrics.Store` with Record and Query methods
- [ ] contextMonitorLoop appends 5 metric rows per tick (context_pct, cost_usd, token_input, token_output, tool_count)
- [ ] Query returns time-ordered samples filtered by agent ID, metric name, and optional time range
- [ ] Metrics survive agent completion (not cleaned up)
- [ ] Unit tests for Store.Record and Store.Query including edge cases (no data, single point)
- [ ] Documentation: metrics schema and query patterns documented

---

## Phase 3: Model Profiles

**User stories**: 7 (named profiles in drem.toml), 8 (partial override support)

### What to build

Extend the config system to parse `[profiles.<name>]` sections from `drem.toml`. Each profile specifies model and/or effort overrides per agent role. Add a `ForAgentTypeWithProfile(agentType, profileName)` function that resolves the effective `AgentCLIConfig` by layering: profile override -> default `[agents.*]` config -> hardcoded defaults. Profiles with unspecified roles inherit from the default agent config. After this phase, operators can define named model configurations and the system can resolve them for any agent type.

### Acceptance criteria

- [ ] `[profiles.<name>]` sections parsed from drem.toml into config struct
- [ ] `ForAgentTypeWithProfile()` resolves effective config with correct layering
- [ ] Partial overrides work: unspecified roles inherit from default `[agents.*]` config
- [ ] Table-driven unit tests for profile parsing and resolution (full override, partial override, missing profile, empty profile)
- [ ] Documentation: profile configuration syntax documented with examples

---

## Phase 4: Experiment Foundation

**User stories**: 9 (create experiment with 2-3 profiles), 12 (configurable variant count), 13 (designate default variant), 28 (CLI experiment creation)

### What to build

Create the `internal/experiment` package with Experiment and Variant GORM models. Implement experiment creation: given a task description and 2-3 profile names (one designated default), create an Experiment record, then for each profile create a Variant record and a corresponding Task record with the shared description. Wire up the CLI command `drem experiment create --profiles X,Y --default X --title "name" --description "desc"`. Variant tasks enter the normal pipeline at `backlog` status and flow through standard lifecycle. After this phase, experiments can be created and their variant tasks processed (without special pool management or auto-merge behavior yet).

### Acceptance criteria

- [ ] Experiment model with ID, ProjectID, Title, Description, Status, DefaultVariant, SourceTaskID, timestamps
- [ ] Variant model with ID, ExperimentID, ProfileName, TaskID, Status, IsDefault, ReusesPlan, timestamps
- [ ] Experiment creation produces N variant tasks (one per profile) in backlog status
- [ ] CLI `drem experiment create` works end-to-end
- [ ] Default variant correctly marked with IsDefault=true
- [ ] Configurable variant count (2-3) enforced
- [ ] Unit tests for experiment creation, variant setup, and validation
- [ ] Documentation: experiment creation workflow documented

---

## Phase 5: Experiment Lifecycle

**User stories**: 10 (experiments own agent pool), 11 (up to 3 concurrent experiments), 14 (default auto-merge), 15 (challenger auto-promotion), 16 (experiment from completed task), 17 (plan reuse option), 30 (CLI from-task)

### What to build

Implement the full experiment lifecycle. When experiments are active, pause normal task processing and partition the agent pool evenly across active variants. When the default variant's task reaches `done`, auto-merge it immediately (other variants continue for data). If the default fails but a challenger succeeds, auto-promote the challenger. Enforce a max of 3 concurrent experiments. Add `drem experiment from-task <id> --profile X` CLI command to create an experiment from a completed task, with optional plan reuse (copies original plan, starts at `plan_review`). Transition experiment status through running -> review -> completed. After this phase, experiments run with fair resource allocation, auto-resolve winners, and support retroactive comparison.

### Acceptance criteria

- [ ] Normal task processing paused when experiments active
- [ ] Agent pool partitioned evenly across active experiment variants
- [ ] Default variant auto-merges on success without waiting for other variants
- [ ] Challenger auto-promoted if default fails and challenger succeeds
- [ ] Max 3 concurrent experiments enforced
- [ ] `drem experiment from-task` creates experiment from completed task
- [ ] Plan reuse copies original plan and starts variant at plan_review
- [ ] Experiment status transitions: running -> review -> completed
- [ ] All variant artifacts preserved regardless of outcome
- [ ] Unit tests for pool partitioning, auto-merge, auto-promotion, concurrent cap
- [ ] Documentation: experiment lifecycle and pool management documented

---

## Phase 6: Quality Metrics by Model

**User stories**: 31 (constraint violation frequency), 32 (fixer spawn rate), 33 (planner rejection rate), 34 (merge conflict rate), 27 (c-suite agents query via SQL)

### What to build

Track quality outcome metrics per agent and persist them so they can be aggregated by model ID. Record constraint violation count on the agent record at the constraint gate. Track fixer spawn events as metrics (agent spawned with type=fixer, linked to parent agent's model). Track planner rejection events (plan_review -> rejected transition, linked to planner agent's model). Track merge conflict occurrences in the merge phase. All data stored in existing tables (Agent fields or Metric rows) so c-suite agents and Grafana can query by model ID. After this phase, `SELECT model_id, COUNT(*) FROM agents WHERE exit_reason = 'constraint_violation' GROUP BY model_id` and similar queries return meaningful comparison data.

### Acceptance criteria

- [ ] Constraint violation count recorded on agent record at constraint gate
- [ ] Fixer spawn events tracked with parent agent's model ID
- [ ] Planner rejection rate queryable by model (join agents + task_events)
- [ ] Merge conflict occurrences recorded with agent/model context
- [ ] All metrics queryable via SQL against drem.db
- [ ] Unit tests for each metric capture path
- [ ] Documentation: quality metrics schema and example queries documented

---

## Phase 7: TUI Experiment Views

**User stories**: 19 (experiment header rows in task board), 20 (comparison summary table), 21 (artifact paths), 29 (create experiment from TUI)

### What to build

Add experiment awareness to the TUI. In the task board, display experiment variants as indented entries grouped under an experiment header row showing experiment title and overall status. Add a comparison summary view accessible from the experiment header, rendering a side-by-side ASCII table of key outcome metrics (success/fail, cost, duration, fixer count, context usage) across variants. Show artifact paths (worktree locations) in the detail panel for each variant. Add a TUI flow for creating experiments: select a task (or enter description), pick profiles from configured list, designate default. After this phase, operators can manage and review experiments entirely from the TUI.

### Acceptance criteria

- [ ] Experiment header rows appear in task board with grouped variant entries
- [ ] Comparison summary table shows side-by-side metrics across variants
- [ ] Artifact paths displayed in variant detail panel
- [ ] Create experiment flow works from TUI (description + profile selection + default designation)
- [ ] Experiment status visible at a glance (running/review/completed)
- [ ] Unit tests for TUI model updates and rendering logic
- [ ] Documentation: TUI experiment features walkthrough

---

## Phase 8: Grafana Integration

**User stories**: 22 (overlaid time-series charts), 23 (pre-built dashboards), 24 (SQLite plugin), 25 (Docker container)

### What to build

Ship a Docker Compose file that runs Grafana OSS with the SQLite datasource plugin pre-installed, reading from `drem.db` (mounted volume). Create pre-built dashboard JSON templates provisioned automatically on startup: A/B Context Curve (overlaid context_pct time-series filtered by experiment, colored by variant), A/B Cost Comparison (cumulative cost curves), Model Scorecard (summary table of success rate, avg cost, avg duration, fixer rate grouped by model), Agent Lifecycle Timeline (annotation markers for spawn, context warning, stuck, completed, killed). Dashboards use Grafana variables for experiment ID and date range filtering. After this phase, `docker-compose up` alongside Drem gives operators rich visual dashboards out of the box.

### Acceptance criteria

- [ ] Docker Compose file defines Grafana container with SQLite plugin
- [ ] Grafana reads from drem.db via mounted volume
- [ ] A/B Context Curve dashboard template works with real data
- [ ] A/B Cost Comparison dashboard template works with real data
- [ ] Model Scorecard dashboard template aggregates by model ID
- [ ] Agent Lifecycle Timeline shows annotation markers
- [ ] Grafana variables for experiment ID and date range filtering functional
- [ ] Setup documented: single `docker-compose up` to start
- [ ] Documentation: Grafana setup guide and dashboard descriptions
