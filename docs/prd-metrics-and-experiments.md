# PRD: Metrics Capture, A/B Experiments & Grafana Dashboard

## Problem Statement

Drem Orchestrator captures rich agent metrics — context window usage, cost, tool call activity, test results, merge outcomes — but overwrites them every 5-second tick cycle. After an agent completes or a run ends, the data is gone. There is no way to answer questions like "how did that run go?", "which task was most expensive?", or "are agents getting faster over time?"

More critically, there is no way to compare model performance. The team wants to experiment with cheaper models like Haiku for some or all agent roles, but currently has no mechanism to measure the impact. Without controlled comparison data, switching models is a leap of faith.

The team needs:
- Persistent time-series metrics that survive agent completion
- A/B experiment capability to compare model configurations head-to-head on identical tasks
- Visual dashboards (Grafana) to overlay metrics and compare outcomes
- Artifact preservation so human reviewers and c-suite Claude Code agents can inspect code output alongside metrics

## Solution

### 1. Metrics Foundation

Add a time-series metrics table to SQLite that captures sampled data points (context %, cost, token counts, tool calls) every 5 seconds during agent execution. Persist final outcome values (exit reason, duration, test results, constraint violations) on the Agent and Task records. Tag every agent record with the model and effort level it ran with.

### 2. Experiment System

Introduce an "experiment" concept that runs 2-3 variants of the same task through completely independent pipelines, each with a different model configuration profile. Each variant gets its own plan, subtasks, worktrees, and branches. A default variant auto-merges on success; if the default fails but a challenger succeeds, the challenger is auto-promoted. All variant artifacts are preserved regardless of outcome for human review.

### 3. Grafana Integration

Run Grafana OSS as a Docker container alongside Drem, reading directly from `drem.db` via the SQLite datasource plugin. Ship pre-built dashboard JSON templates for A/B comparison: overlaid context % curves, cost comparison panels, success rate by model, and annotation markers for key lifecycle events.

## User Stories

1. As an orchestrator operator, I want to see how much each agent cost after it completes, so that I can track spend over time.
2. As an orchestrator operator, I want to see which model and effort level each agent ran with, so that I can correlate model choice with outcomes.
3. As an orchestrator operator, I want to see context window usage over time as a curve (not just the final value), so that I can understand how quickly different models consume context.
4. As an orchestrator operator, I want to see test pass/fail results persisted on task records, so that I can query historical test reliability.
5. As an orchestrator operator, I want to see agent duration (spawn to completion) recorded, so that I can compare wall-clock performance across models.
6. As an orchestrator operator, I want to see agent exit reasons persisted on the agent record, so that I can query failure modes without digging through JSONL files.
7. As an orchestrator operator, I want to define named model profiles in drem.toml (e.g., "sonnet-baseline", "full-haiku", "haiku-coders"), so that I can reuse configurations across experiments.
8. As an orchestrator operator, I want profiles to support partial overrides (only specify roles I want to change), so that I don't have to spell out every role for each profile.
9. As an orchestrator operator, I want to create an experiment that runs the same task through 2-3 independent pipelines with different profiles, so that I can compare model performance on identical work.
10. As an orchestrator operator, I want experiments to own the entire agent pool while running, so that variants get fair resource allocation without interference from normal work.
11. As an orchestrator operator, I want to run up to 3 experiments concurrently, so that I can test multiple tasks in parallel.
12. As an orchestrator operator, I want to configure the number of variants per experiment (2-3), so that the c-suite team can adjust experiment scope as needed.
13. As an orchestrator operator, I want to designate a default variant in each experiment, so that the baseline auto-merges without blocking the pipeline.
14. As an orchestrator operator, I want the default variant to merge immediately when it passes all gates, even if other variants are still running, so that experiments don't block delivery.
15. As an orchestrator operator, I want the orchestrator to auto-promote a challenger variant if the default variant fails but the challenger succeeds, so that surprising results are captured automatically.
16. As an orchestrator operator, I want to create an experiment from an already-completed task, re-running it with a different profile, so that I can retroactively compare models without waiting for new work.
17. As an orchestrator operator, I want to configure whether a re-run experiment reuses the original plan or generates a fresh plan, so that I can control what is being compared (execution quality vs. end-to-end quality).
18. As an orchestrator operator, I want all variant artifacts (branches, worktrees, agent records, metrics) preserved regardless of winner/loser, so that I can review everything with the c-suite team.
19. As an orchestrator operator, I want experiment variants displayed as grouped entries under an experiment header in the existing TUI task board, so that I can see experiment status without switching views.
20. As an orchestrator operator, I want a TUI comparison summary table showing key metrics side-by-side across variants (success rate, cost, duration, fixer rate, context usage), so that I can quickly assess outcomes.
21. As an orchestrator operator, I want artifact paths shown in the TUI so I can externally diff the code output of competing variants.
22. As an orchestrator operator, I want Grafana dashboards that overlay time-series metrics (context %, cost accumulation, token counts) across variants on the same chart, so that I can visually compare model behavior.
23. As an orchestrator operator, I want pre-built Grafana dashboard templates shipped with Drem, so that I get A/B comparison panels out of the box without building them manually.
24. As an orchestrator operator, I want Grafana to read directly from drem.db via the SQLite plugin, so that I don't need to run Prometheus or any additional data stores.
25. As an orchestrator operator, I want Grafana running as a Docker container alongside Drem, so that setup is simple and isolated.
26. As an orchestrator operator, I want metrics data retained indefinitely, so that I can compare experiments across weeks and months.
27. As a c-suite Claude Code agent, I want to query experiment metrics and outcomes via SQL against drem.db, so that I can participate in experiment review and analysis.
28. As an orchestrator operator, I want to create experiments via CLI (`drem experiment create --profiles sonnet-baseline,full-haiku --default sonnet-baseline`), so that I can set up experiments programmatically.
29. As an orchestrator operator, I want to create experiments from the TUI by selecting a task and picking profiles, so that I can launch experiments interactively.
30. As an orchestrator operator, I want to create experiments from completed tasks via CLI (`drem experiment from-task <task-id> --profile full-haiku`), so that I can retroactively test alternative models.
31. As an orchestrator operator, I want constraint violation frequency tracked per agent, so that I can see if cheaper models produce sloppier code.
32. As an orchestrator operator, I want fixer spawn rate tracked per model, so that I can see if cheaper models need more rescue interventions.
33. As an orchestrator operator, I want planner rejection rate tracked per model, so that I can see if cheaper planners produce worse plans.
34. As an orchestrator operator, I want merge conflict rate tracked per model, so that I can see if model quality affects integration reliability.

## Implementation Decisions

### Schema Changes

**Agent model additions:**
- `ModelID` (string) — the Claude model ID used for this agent (e.g., "claude-haiku-4-5-20251001"). Populated at spawn time from the resolved AgentCLIConfig.
- `Effort` (string) — the effort level used ("low", "medium", "high"). Populated at spawn time.
- `CompletedAt` (nullable timestamp) — when the agent finished. Populated on completion.
- `ExitReason` (string) — structured exit reason from the Claude exit log. Populated on completion.
- `TotalCostUSD` (float64) — final cost. Populated on completion from the last context monitor reading.
- `FinalContextPct` (int) — final context window usage percentage. Populated on completion.

**New Metric model:**
- `ID` (UUID, primary key)
- `AgentID` (UUID, indexed) — which agent this sample belongs to
- `Timestamp` (time.Time, indexed) — when the sample was taken
- `Name` (string, indexed) — metric name (e.g., "context_pct", "cost_usd", "token_input", "token_output", "tool_count")
- `Value` (float64) — the metric value
- `Labels` (JSONField) — optional key-value labels for additional dimensions

**New Experiment model:**
- `ID` (UUID, primary key)
- `ProjectID` (UUID, indexed)
- `Title` (string) — human-readable experiment name
- `Description` (string) — the task description shared by all variants
- `Status` (string) — "running", "review", "completed", "cancelled"
- `DefaultVariant` (string) — profile name of the default variant
- `SourceTaskID` (nullable UUID) — if created from a completed task, points to the original
- `CreatedAt`, `UpdatedAt` (timestamps)

**New Variant model:**
- `ID` (UUID, primary key)
- `ExperimentID` (UUID, indexed)
- `ProfileName` (string) — name of the model profile used
- `TaskID` (UUID) — the Task record for this variant's pipeline
- `Status` (string) — "running", "passed", "failed", "winner", "rejected"
- `IsDefault` (bool) — whether this is the default variant
- `ReusesPlan` (bool) — whether this variant reuses a plan from the source task
- `CreatedAt`, `UpdatedAt` (timestamps)

### Metrics Collection

The existing `contextMonitorLoop` in `internal/agent/monitor.go` already polls every 5 seconds and has access to context usage and activity data. The change is additive: in addition to overwriting `Agent.Config`, also append a row to the `metrics` table for each sampled value.

**Time-series metrics (sampled every 5 seconds):**
- `context_pct` — context window usage percentage
- `cost_usd` — cumulative cost
- `token_input` — cumulative input tokens
- `token_output` — cumulative output tokens
- `tool_count` — cumulative tool call count

**Final snapshot metrics (captured once at agent completion):**
- Exit reason, total duration, test pass/fail, constraint violation count, files modified count — stored directly on the Agent record.

### Profiles

Profiles are defined in `drem.toml` under `[profiles.<name>]` sections. Each profile specifies model and/or effort per agent role, with partial override support (unspecified roles inherit from the default `[agents.*]` config).

```toml
[profiles.sonnet-baseline]
  planner_model = "claude-sonnet-4-6"
  coder_model = "claude-sonnet-4-6"

[profiles.full-haiku]
  planner_model = "claude-haiku-4-5-20251001"
  coder_model = "claude-haiku-4-5-20251001"
  reviewer_model = "claude-haiku-4-5-20251001"

[profiles.haiku-coders]
  coder_model = "claude-haiku-4-5-20251001"
  # everything else inherits defaults
```

A `ForAgentTypeWithProfile` function resolves the effective AgentCLIConfig by layering: profile override → default agent config → hardcoded defaults.

### Experiment Lifecycle

1. **Creation**: User creates experiment via CLI or TUI, specifying task description (or source task ID) and 2-3 profile names. One profile is designated as default.
2. **Variant setup**: For each profile, the orchestrator creates a new Task record with the shared description. If created from a completed task with plan reuse enabled, the original plan is copied to the new task and it starts at `plan_review` status. Otherwise, it starts at `backlog`.
3. **Execution**: Experiments own the entire agent pool. Normal (non-experiment) task processing is paused while experiments are active. The agent pool is split evenly across active variants.
4. **Default auto-merge**: When the default variant's task passes all gates (tests, constraints, merge), it merges immediately. Other variants continue running for comparison data.
5. **Auto-promotion**: If the default variant fails but a challenger passes all gates, the challenger is automatically promoted to winner and merged.
6. **Review**: When all variants complete (pass or fail), the experiment moves to "review" status. Human reviews via TUI summary + Grafana dashboards + artifact inspection.
7. **Completion**: Human marks experiment as completed. All artifacts (branches, worktrees, metrics, agent records) are retained indefinitely.

### Agent Pool Management

- When experiments are active, normal task processing is paused (backlog tasks wait).
- Up to 3 experiments can run concurrently.
- `max_concurrent_agents` is divided evenly across all active variants across all active experiments.
- Example: 5 agents, 1 experiment with 2 variants = 2 agents per variant (1 spare).

### Grafana Integration

- Docker Compose file ships with Drem, defining a Grafana OSS container with the SQLite datasource plugin pre-installed.
- Grafana is configured to read from `drem.db` (mounted as a volume).
- Pre-built dashboard JSON templates are provisioned automatically:
  - **A/B Context Curve**: overlaid context % time-series, filtered by experiment ID, colored by variant/profile
  - **A/B Cost Comparison**: cumulative cost curves overlaid
  - **Model Scorecard**: summary table of success rate, avg cost, avg duration, fixer rate — grouped by model ID
  - **Agent Lifecycle Timeline**: annotations for spawn, context warning, stuck detected, completed, killed
- Dashboard templates use Grafana variables for experiment ID and date range filtering.

### TUI Changes

- Task board displays experiment variants as indented entries under an experiment header row.
- New comparison summary view accessible from the experiment header, showing a side-by-side ASCII table of key outcome metrics across variants.
- Artifact paths (worktree locations) displayed in the detail panel for each variant, enabling external diffing.

## Testing Decisions

Tests should verify external behavior through the public interfaces of each module, not implementation details. Use the existing testutil patterns: `testutil.NewTestDB(t)` for isolated in-memory SQLite, `testutil.CreateProject/CreateTask/CreateAgent` factories, table-driven tests with `t.Run()`, and hand-written mock structs with compile-time interface assertions.

### Modules to test

**`internal/metrics`** — the time-series store:
- Recording samples and querying by agent ID, time range, metric name
- Aggregation queries (avg, sum, count grouped by model)
- Edge cases: no data, single data point, high volume
- Similar pattern to `internal/model/models_test.go` (GORM operations against test DB)

**`internal/experiment`** — the experiment lifecycle:
- Experiment creation from description and from completed task
- Variant setup with profile resolution and plan reuse
- Default variant auto-merge trigger
- Auto-promotion when default fails but challenger succeeds
- Agent pool partitioning across experiments and variants
- Concurrent experiment cap enforcement (max 3)
- State transitions: running → review → completed
- Similar pattern to `internal/orchestrator/orchestrator_test.go` (state machine tests with DB assertions)

**`cmd/drem/config.go` — profile parsing:**
- Profile TOML parsing with partial overrides
- `ForAgentTypeWithProfile` resolution layering
- Similar pattern to existing `config_test.go` (table-driven TOML parsing tests)

## Out of Scope

- **Prometheus exporter**: The metrics are stored in SQLite and consumed by Grafana via the SQLite plugin. A `/metrics` Prometheus endpoint is a future enhancement if external monitoring systems need to scrape Drem.
- **Web-based TUI replacement**: Grafana handles rich visualization. The TUI gets summary tables but not interactive charts.
- **Automated experiment scheduling**: Experiments are created manually. Automated "run this experiment every week to track regression" is a future feature.
- **Log aggregation**: Switching slog to JSON format and aggregating agent logs into SQLite is related but separate work. This PRD focuses on metrics and experiments.
- **Audit trail for agents**: Agent lifecycle events (spawn, context warning, stuck detected, killed) as a persistent event log is related but separate work.
- **Metric retention policies and cloud backup**: Data is retained indefinitely in SQLite for now. AWS backup or retention windows are future work driven by observed data volume.
- **Cost alerting or budget caps**: Tracking cost is in scope; alerting when cost exceeds thresholds is not.

## Further Notes

- The existing `Agent.Config` JSONField overwrite pattern in `monitor.go` should continue to work alongside the new metrics table — the TUI's real-time agent panel reads from `Agent.Config` for live display, while the metrics table provides historical data.
- The `AgentCLIConfig` struct and `ForAgentType()` function already handle per-role model configuration. Profiles build on this foundation by adding named configurations that can be selected at experiment creation time.
- C-suite Claude Code agents can participate in experiment review by querying `drem.db` directly with SQL. The schema is designed to be self-describing: join `experiments` → `variants` → `tasks` → `agents` → `metrics` for full experiment analysis.
- The variant count per experiment (2-3) should be configurable in `drem.toml` so the c-suite team can adjust it without code changes.
- When creating an experiment from a completed task, the original task's metrics may not have time-series data (since this feature didn't exist when it ran). The comparison will only have final snapshot values for the original run. This is acceptable for early adoption.
