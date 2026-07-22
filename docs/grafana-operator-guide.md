# Grafana Operator Guide

This guide explains how to use Grafana as the Drem operator dashboard. Grafana is a read-only observability surface: use it to decide where to look next, then use the Drem TUI, `dremctl`, or C-Suite operators to take action.

## What Grafana Shows

Grafana reads Drem's SQLite databases through the `frser-sqlite-datasource` plugin.

| Datasource | UID | Contents |
|------------|-----|----------|
| `DremDB` | `drem-sqlite` | Project tasks, task events, agents, agent metrics, costs, context usage, and temp worker token records. |
| `CsuiteDB` | `csuite-sqlite` | C-Suite watcher data such as persona turn history and token usage when the watcher database is mounted. |

Current provisioned dashboards live under `grafana/provisioning/dashboards/json/`:

| Dashboard | Use it for |
|-----------|------------|
| `Model Scorecard` | Overall project activity: task count, event count, model mix, status breakdown, completions by model, and exit reasons. |
| `Cost Over Time` | Spend trend, per-agent cost outliers, and cost by model. |
| `Token Usage by Agent Role` | Token input/output by agent type and agent-role distribution. |
| `Context % Curves per Agent` | Final context usage, context growth over time, and context pressure by exit reason. |
| `Temp Worker Token Usage` | Temporary worker token/cost accounting when temp worker rows are present. |

## Start Grafana

For the standalone repo-level Grafana compose file:

```bash
docker compose -f docker-compose.grafana.yml up -d
```

Then open:

```text
http://localhost:3000
```

Anonymous viewer access is enabled by the compose file. The default home dashboard is `Model Scorecard`.

For a registered project, confirm the project orchestrator and support containers are running before trusting Grafana freshness:

```bash
docker compose -f ~/.drem/projects/<project-name>/compose.yml ps
```

Do not use a broad `docker compose up` against the project stack unless you intend to start dependencies. Prefer scoped service operations with `--no-deps` when restarting a single service.

## Verify Data Is Connected

List the dashboards Grafana can see:

```bash
curl -s http://localhost:3000/api/search
```

Check the project database directly from the host:

```bash
sqlite3 ~/.drem/projects/<project-name>/data/drem.db \
  "select count(*) as tasks from tasks; \
   select count(*) as agents from agents; \
   select name, count(*) from metrics group by name order by name;"
```

If Grafana shows empty panels but the database has rows, check the datasource path in `grafana/provisioning/datasources/sqlite.yml` and the volume mount used by the Grafana container.

## Operator Workflow

Use Grafana as the first pass for operational triage:

1. Open `Model Scorecard` to check whether the system is moving.
2. Look at `Task Status Breakdown` for piles of `failed`, `rejected`, `testing_ready`, or `in_progress` work.
3. Look at `Task Events per Day` and `Agent Completions by Model Over Time` to see whether activity stopped or only slowed down.
4. Open `Cost Over Time` when the run is moving but spend looks suspicious.
5. Open `Token Usage by Agent Role` when one role appears to be burning context or repeatedly retrying.
6. Open `Context % Curves per Agent` when agents fail late, hit context pressure, or produce incomplete work.
7. Use `dremctl`, the TUI, or Kyle/Mike/Seth/Alex to inspect and act on the exact task or agent lane.

Grafana should answer these questions quickly:

| Question | Dashboard/panel |
|----------|-----------------|
| Is work still flowing? | `Model Scorecard`: task events and completions over time. |
| Which state is backing up? | `Model Scorecard`: task status breakdown. |
| Which model or role is producing errors? | `Model Scorecard`: exit reasons by model; `Token Usage by Agent Role`. |
| Are costs increasing unexpectedly? | `Cost Over Time`: cumulative cost, cost by model, max cost agent. |
| Are agents running out of context? | `Context % Curves per Agent`: final context percent and exit reason panels. |
| Did temporary workers burn tokens? | `Temp Worker Token Usage`: temp worker cost and token breakdown. |

## Acting On Findings

Grafana is not the control plane. After spotting a problem, switch to supported Drem surfaces.

List tasks:

```bash
docker exec drem-orchestrator-csuite-kyle-1 dremctl tasks --limit 40
```

Inspect recent events:

```bash
docker exec drem-orchestrator-csuite-kyle-1 dremctl events --limit 80
```

Approve or reject a gate only after reviewing the task evidence:

```bash
docker exec -e DREM_ACTOR='operator:<name-or-session>' drem-orchestrator-csuite-kyle-1 dremctl approve <task-id>
docker exec -e DREM_ACTOR='operator:<name-or-session>' drem-orchestrator-csuite-kyle-1 dremctl reject <task-id> --reason "<reason>"
```

Retry failed work only when the failure mode is understood:

```bash
docker exec -e DREM_ACTOR='operator:<name-or-session>' drem-orchestrator-csuite-kyle-1 dremctl retry <task-id>
```

## Common Interpretations

| Symptom | Likely meaning | Next check |
|---------|----------------|------------|
| `in_progress` count grows but completions stop | Workers may be failing, stuck, or unable to report results. | Check `dremctl tasks`, `dremctl events`, and agentmon/orch logs. |
| `testing_ready` count grows | Deterministic gate preparation is not draining. | Inspect gate/worker events; do not manufacture verification with `pass`. |
| `verification_ready` count grows | Exact artifacts are waiting for native evidence. | Use `dremctl artifact`, verify the exact binary with Computer Use, then submit `dremctl verify`. |
| `failed` count spikes after retry activity | Scheduler, merge, test, or dependency failure may be systemic. | Ask Mike/Seth to investigate the failure pattern before mass retries. |
| Cost rises without completions | Agents may be retrying, looping, or producing rejected work. | Compare `Cost Over Time`, token panels, and exit reasons. |
| Context percent trends high before failures | Tasks may be too large or prompts may be accumulating irrelevant context. | Break work into smaller slices or route a prompt/context investigation. |
| Grafana is blank | Grafana may be pointed at the wrong SQLite file or the DB may have no rows yet. | Verify the datasource path, container mounts, and direct `sqlite3` counts. |

## Boundaries

- Grafana currently reads SQLite-backed operational data; it is not the authoritative task state machine.
- The operator metrics package under `pkg/operator/metrics` provides Prometheus collectors for code-level metric registration, but the current dashboards are provisioned around SQLite datasources.
- Do not edit task state directly in SQLite to fix dashboard symptoms. Use `dremctl`, the TUI, or supported orchestrator endpoints.
- Do not restart `drem-sglang` just to refresh Grafana. Grafana reads persisted databases and does not require SGLang restarts.
