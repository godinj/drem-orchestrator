# agentmon

agentmon owns two input channels that feed structured events into the
orchestrator:

1. **Claude transcript tailing** — the original path. The `Monitor`
   type reads `~/.claude/projects/.../*.jsonl` incrementally and
   produces an `Activity` summary used by the TUI.
2. **Docker stdout subscription** — the `DockerSource` type. Runs as a
   per-project sidecar alongside each orchestrator in a project's
   compose stack; subscribes to lifecycle events on drem-labeled
   containers, tails stdout for each, parses lines through
   `internal/extract`, and POSTs typed records to the orchestrator's
   `POST /internal/logs` endpoint.

The two channels funnel into the same server-side storage (`TaskEvent`)
so downstream consumers do not need to know which path produced a given
record.

## Binary

`cmd/drem-agentmon` is the dedicated entry point for the Docker
subscription path. The transcript-tailing path is still used as a
library by `internal/agent/monitor.go`, which runs inside the coder
worker and does not spawn this binary.

Relevant flags (see `cmd/drem-agentmon/main.go`):

- `--docker` — enable the Docker input channel. Defaults to `true`
  when `DREM_IN_CONTAINER=1` is set (the agentmon image sets this),
  `false` otherwise.
- `--orch-url` — orchestrator base URL, required when `--docker=true`.
- `--agentmon-token` — shared secret for the `X-Drem-Agentmon-Token`
  header, required when `--docker=true`.
- `--project` — project name used to filter events by the
  `drem.project` label, required when `--docker=true`.

Each flag falls back to an environment variable of the same name in
`SCREAMING_SNAKE_CASE` with the `DREM_` prefix (`DREM_ORCH_URL`,
`DREM_AGENTMON_TOKEN`, `DREM_PROJECT`).

## Per-project compose service

agentmon goes in the **per-project** compose file (see prompt 16),
not the global compose. Each orchestrator has exactly one agentmon
sidecar.

```yaml
agentmon:
  image: localhost:5000/drem-agentmon:latest
  environment:
    DREM_PROJECT: "${PROJECT_NAME}"
    DREM_ORCH_URL: "http://orch:8080"
    DREM_AGENTMON_TOKEN: "${AGENTMON_TOKEN}"
  volumes:
    - /var/run/docker.sock:/var/run/docker.sock:ro
  networks: [drem-net]
  labels:
    drem.project: "${PROJECT_NAME}"
    drem.service: agentmon
```

Notes:

- The Docker socket is mounted read-only; agentmon only reads events
  and log streams. The long-term plan is to funnel this through the
  spawner's docker-query-proxy so no service except the spawner
  touches the socket directly.
- There is no retry queue for failed ingestions; a non-2xx response
  is logged and the batch is dropped. Agents remain the source of
  truth for their state, and `/internal/logs` is an "eventual
  convergence" surface by design.
- Raw logs are not duplicated — agentmon reads Docker's log driver
  output and never persists the raw bytes. The orchestrator stores
  only the structured events (commit, push, test_result, build_error,
  heartbeat, crash, tool_call).
