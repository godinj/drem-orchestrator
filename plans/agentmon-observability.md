# Plan: Agentmon observability (post v12–v14 canary)

## 1. Incident summary

Between the v13 and v14 canary cuts, agentmon's Docker event
subscription matched zero events for approximately 41 hours. The
cause was a label-key mismatch between the subscription filter
(`drem.project`) and the containers the spawner was labelling. The
subscription itself was healthy — it had a live TCP session against
the docker engine — but no event ever reached the select-arm that
feeds the tail map. As a result:

- No WARN was logged; the subscription looked fine from the outside.
- No tail goroutine was started for any worker.
- agentmon had no live signal for any running container.
- The orchestrator's stuck-agent reconciler, running on stale DB
  heartbeats alone, mis-classified every live worker as dead and
  began killing them. This presented as two surface bugs
  (Bug A: 401 on agentmon → fixed in task #1; Bug B: missing
  `WorktreeBranch` on agent rows → fixed in task #2) but the root
  cause was that agentmon had been blind for 41 hours.
- Kyle additionally discovered mid-triage that
  `internal/orchestrator/orchestrator.go` guards its own Docker
  event watcher on `o.Runtime != nil`, but
  `cmd/drem/main.go` never called `SetRuntime` — so even if
  agentmon had been healthy, orch's redundant watcher would not
  have run in containerised deploys. This is the fourth gap.

Task #8 owns the label-mismatch root-cause migration. This plan
owns the **systemic sensors** so the next instance of "a class of
silent outage dressed in a different disguise" surfaces within a
minute rather than 41 hours.

## 2. Scope — Seth's three prongs + Kyle's fourth

1. **Zero-events WARN (Seth 1).** After the subscription opens,
   count events as they land in the select arm. If the count is
   still zero after `Warn0Window` (default 45s, midpoint of Seth's
   30–60s range), log a single WARN with the filter labels so the
   operator can see what the subscription was looking for. Do NOT
   fail the subscription — a flaky daemon that takes 40s to deliver
   an event is a different failure mode and should not crash.

2. **`events_matched_last_minute` counter (Seth 2).** Expose
   `DockerSource.EventsMatchedLastMinute() int` backed by a 60-slot
   per-second bucket ring. Consumers (liveness probe, HTTP expvars,
   the reconciler, or a test) decide how to surface it. No external
   metrics library — keep the import surface flat.

3. **Reconciler correlation (Seth 3).** Before the stuck-agent
   reconciler declares a container-mode agent dead, consult a
   `ContainerSightingProbe.HasSeen(containerID) bool` hook. A false
   return means agentmon has no live signal for this container —
   which is exactly the v12–v14 failure mode. Skip the kill and log
   a distinct line so operators can grep for it.

4. **Orch `SetRuntime` wiring (Kyle's fourth gap).** In
   `cmd/drem/main.go`, when the spawner socket is present AND
   `DREM_IN_CONTAINER=1` is set, instantiate a `container.Runtime`
   via `container.NewDockerRuntime()` and call
   `orch.SetRuntime(rt)`. This activates the pre-existing
   `watchDockerEvents` goroutine in containerised orch deploys.
   Host-mode dev keeps the legacy behaviour where Runtime stays nil.

## 3. Implementation

### 3.1 `internal/agentmon/docker_source.go`

New fields on `DockerSource`:

```
Warn0Window time.Duration   // 0 means defaultWarn0Window=45s
Now         func() time.Time // 0 means time.Now; tests inject
```

Run loop: a `startWarn0Goroutine` kick-off at Run entry sleeps
`Warn0Window`, then samples an `atomic.Int64 eventsSeen` counter.
If still zero, emits:

```
WARN agentmon docker source: zero events matched in 45s since subscription start
     filter_labels={"drem.project":"<p>"} window=45s project=<p>
```

Exactly once. Ctx cancel wakes the goroutine early and suppresses
the log (shutdown is not a misconfiguration signal).

Per-second bucket ring `buckets [60]bucketEntry` is incremented on
every event that arrives through the select arm, independent of
whether the event routes to a tail start/stop (so a drop-in
misconfig that matches the filter but is typed wrong still
registers). `EventsMatchedLastMinute` sums the buckets whose epoch
falls in the trailing 60 seconds.

`HasSeen(containerID)` returns true if either (a) an active tail
exists for the container, or (b) there is any traffic in the
trailing minute. (a) covers the happy path where a start event
registered; (b) is the "agentmon is alive even if we lost this
specific container's tail" guarantee — the reconciler only wants
to skip its kill when agentmon is demonstrably blind.

### 3.2 `internal/orchestrator/orchestrator.go`

New field on `Orchestrator`:

```
sightingProbe ContainerSightingProbe
```

New type (same package, no new internal import):

```
type ContainerSightingProbe interface {
    HasSeen(containerID string) bool
}
```

New setter `SetContainerSightingProbe(p)`. Nil is safe — the
reconciler only consults the probe when non-nil.

### 3.3 `internal/orchestrator/reconcile_stuck.go`

Before the existing `"detected dead agent session without
completion"` WARN, check:

```
if o.sightingProbe != nil && ag.TmuxSession != "" &&
   !o.sightingProbe.HasSeen(ag.TmuxSession) {
    o.logger.Warn("reconcile stuck: skipping dead-agent kill because agentmon has no sighting",
        "agent_id", ag.ID, "task", task.Title, "container_id", ag.TmuxSession)
    continue
}
```

`continue` rather than `return` so the loop still processes other
tasks that might be stuck for reasons unrelated to agentmon. The
task/agent are left untouched — the reconciler will revisit on the
next tick, and if agentmon recovers the next pass will make the
correct decision.

### 3.4 `cmd/drem/main.go`

After `orch.SetSpawner(...)`:

```
if os.Getenv("DREM_IN_CONTAINER") == "1" {
    rt, rterr := container.NewDockerRuntime()
    if rterr != nil {
        slog.Warn("docker runtime: could not connect; watchDockerEvents will stay disabled",
            "error", rterr)
    } else {
        orch.SetRuntime(rt)
        slog.Info("docker runtime wired for watchDockerEvents")
    }
}
```

`DREM_IN_CONTAINER=1` matches the existing convention (see
`deploy/docker/agentmon.Dockerfile` and
`cmd/drem-agentmon/main.go:defaultDockerOn`).

## 4. Tests

- `TestDockerSource_WarnOnZeroEventsAfterWindow`: assert the WARN
  fires within `Warn0Window + slack` when no events arrive. Captures
  `slog.Default()` output into a buffered handler for assertion.
- `TestDockerSource_WarnSuppressedWhenEventsArrive`: assert one
  event in the window suppresses the WARN. Prevents false-positive
  noise.
- `TestDockerSource_EventsMatchedLastMinute`: inject a stub clock,
  emit events at known epochs, assert the trailing-minute sum. Also
  asserts ageing: events past the 60s window fall off.
- `TestDockerSource_HasSeenReflectsActiveAndRecentTraffic`: before
  any event, `HasSeen` is false. After a start event, the specific
  container returns true, and any-other-container returns true via
  the recent-traffic fallback.
- `TestReconcileStuck_SkipsKillWhenAgentmonUnsighted`: install a
  probe that returns false, run `reconcileStuckAgents`, assert
  the agent's status and the task are unchanged. The probe was
  queried with the agent's `TmuxSession` (container ID).
- `TestReconcileStuck_ProcessesKillWhenAgentmonSighted`: probe
  returns true — reconciler proceeds with its normal retry path.
- `TestReconcileStuck_NilProbePreservesLegacyBehaviour`: no probe
  installed — host-mode path still kills as before.

## 5. Runbook — "is agentmon ingesting?"

### 5.1 From the orch host

```
docker logs drem-agentmon-<project> 2>&1 |
  grep 'zero events matched in 45s since subscription start'
```

If this returns any hits, agentmon was blind at some point — check
the `filter_labels` field in the log entry and correlate against the
labels on worker containers:

```
docker ps --format 'table {{.Names}}\t{{.Labels}}' |
  grep drem.project
```

A mismatch between the filter and the actual labels is the v12–v14
failure mode.

### 5.2 From inside the orch container

```
# Proxy the trailing-minute count (if a liveness endpoint is wired):
curl -s http://drem-agentmon:<port>/healthz/events-last-minute

# Or grep orchestrator logs for the reconciler skip:
docker logs drem-orch-<project> 2>&1 |
  grep 'reconcile stuck: skipping dead-agent kill because agentmon has no sighting'
```

Seeing the skip log over multiple ticks means the reconciler is
correctly NOT killing live agents — but it also means agentmon is
silent, so fix agentmon first.

### 5.3 Healthy-system baseline

On a project that is running normally, you should see:

- NO `zero events matched` WARN in agentmon logs.
- NO `skipping dead-agent kill because agentmon has no sighting`
  log in the orchestrator.
- The agentmon `EventsMatchedLastMinute` counter (if exposed) is
  non-zero any time workers are starting, committing, or dying —
  which, on an active project, is continuously.

## 6. Out of scope (future work)

- A proper `/healthz` endpoint on agentmon that surfaces
  `EventsMatchedLastMinute` so a Prometheus scrape or a compose
  healthcheck can alert on it. This plan exposes the getter; the
  HTTP route is a separate, small task.
- Per-container strict sighting (a set of seen IDs). The current
  fallback treats "any recent traffic" as sighted, which is correct
  for the reconciler's purpose but not for finer-grained queries.
- Adding a merged agentmon/orch binary that shares a single event
  subscription — currently each subscribes separately. The wiring
  in this plan activates orch's watcher; an optimisation pass could
  plumb them through a shared channel.

## 7. Files touched

- `internal/agentmon/docker_source.go` — new fields, Warn goroutine,
  bucket ring, HasSeen.
- `internal/agentmon/docker_source_test.go` — four new tests.
- `internal/orchestrator/orchestrator.go` — new field
  `sightingProbe`, new type `ContainerSightingProbe`, new setter
  `SetContainerSightingProbe`.
- `internal/orchestrator/reconcile_stuck.go` — correlation
  predicate before the dead-agent WARN.
- `internal/orchestrator/reconcile_stuck_sighting_test.go` — three
  new tests.
- `cmd/drem/main.go` — `container.NewDockerRuntime()` wiring behind
  `DREM_IN_CONTAINER=1` after `SetSpawner`.
- `plans/agentmon-observability.md` — this plan.

## 8. Constitution check

`internal/orchestrator/` package's internal-import baseline stays at
17 — `container` was already imported, so `ContainerSightingProbe`
being declared in the orchestrator package itself means no new
import was added. Verified via:

```
grep -rh '"github.com/godinj/drem-orchestrator/internal/' \
     internal/orchestrator/*.go |
  grep -v _test.go |
  grep -oP '"github.com/godinj/drem-orchestrator/internal/[^"]*"' |
  sort -u | wc -l
```

## 9. Rebase coordination

Task #8 is also editing `internal/orchestrator/orchestrator.go`
(adding a `projectName` field for the label-filter migration). This
plan adds a `sightingProbe ContainerSightingProbe` field plus an
inline type declaration and setter. At rebase time Kyle will
reconcile the two struct-edit patches — both are additive and
order-independent.
