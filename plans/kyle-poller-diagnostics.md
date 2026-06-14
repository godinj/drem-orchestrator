# Kyle Poller Diagnostics

Status: active runbook
Owner: Kyle / operator agents
Scope: diagnosing stuck or silent container-Kyle turns

## Problem

Container-Kyle can look idle or unresponsive from inbox/outbox state while he is actually inside a long-running `opencode run` turn. Future agents should check the persona process tree before spending time on watcher metrics, outbox delivery, or C-Suite database archaeology.

## Fast Diagnosis

When a Kyle inbox message is not archived and no outbox reply appears, run these checks in this order.

1. Check C-Suite agent status.

```bash
TOKEN='<DREM_BEARER_TOKEN from watcher env>'
curl -sS -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8092/api/agents
```

Key Kyle fields:

- `status: stale` means the dashboard thinks the current activity is old.
- `current_activity: replying to <file>` means the persona poller has leased or is processing that inbox item.
- `unread_count: 1` can remain true while the current turn is still running; it does not prove Kyle is idle.

2. Check the actual Kyle process tree.

```bash
docker top drem-orchestrator-csuite-kyle-1
```

Expected healthy-but-busy shape:

```text
/usr/bin/tini -- /usr/local/bin/csuite-entrypoint
/usr/local/bin/csuite-persona -persona kyle ...
opencode run --format json ...
```

If child commands under `opencode run` include loops such as `sleep`, repeated `dremctl tasks`, or `dremctl worker`, Kyle is in the middle of a turn. Treat this as a stuck or over-monitoring turn, not as a delivery failure.

3. Check queue counts only after process state.

```bash
TOKEN=$(tr -d '\n' < ~/.drem/csuite-watcher.token)
curl -sS -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8092/v1/queue
```

Useful interpretation:

- `kyle inbox count: 1` plus an active `opencode run` means the current message is still being processed.
- `kyle inbox count: 1` plus no `opencode run` means the poller may be idle, crashed, or unable to pick up the message.
- Watcher `/v1/queue` reports filesystem queue state; it does not show Kyle turn duration.

4. Check Kyle container logs last.

```bash
docker logs drem-orchestrator-csuite-kyle-1 --since 15m
```

No recent logs does not prove idleness. `docker top` is the faster source of truth.

## Metrics Caveat

Do not rely on watcher `turn_metrics` for Kyle. The legacy watcher lifecycle path intentionally does not record Kyle turns in this containerized persona path. For Kyle, the practical metrics are:

- `/api/agents` for current activity and stale/online status.
- `/v1/queue` for inbox/outbox/quarantine counts.
- `docker top drem-orchestrator-csuite-kyle-1` for whether a turn is actively running.
- Kyle inbox/outbox files for delivery artifacts after the turn exits.

## Restart Behavior

Restarting `drem-orchestrator-csuite-kyle-1` restarts the whole Kyle container, including `csuite-persona -persona kyle`.

The process tree is:

```text
tini -> csuite-entrypoint -> csuite-persona -persona kyle -> opencode run
```

A restart kills the stuck `opencode run` child and brings the poller back through the entrypoint. The current inbox message remains unless it was successfully archived, so Kyle may pick it up again after restart.

## Known Failure Mode

The 2026-05-06 autonomy smoke test showed Kyle receiving a mission and then running synchronous monitoring loops inside one turn instead of scheduling follow-up and exiting. This made `/api/agents` report `status: stale` while `docker top` showed active `opencode run` and child `dremctl` polling commands.

Diagnosis: Kyle was not missing the inbox message. Kyle was over-monitoring inside the turn.

Preferred product fix: make Kyle recovery turns bounded. Kyle should observe once, take one action or schedule a follow-up, write outbox/state, and exit. Long polling should move into a separate periodic/event-triggered recovery loop.
