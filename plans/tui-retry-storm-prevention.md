# TUI retry-storm prevention

## Context

On 2026-04-21 a single `drem --tui-only` dashboard client brought the
orchestrator to its knees — sustained 413% CPU on the orchestrator
process, `/tasks` connection-reset bursts for other clients, and a
roughly-minute-long outage before the TUI was killed. The TUI was
itself the source of the DDoS; no server-side attack or runaway
background job was involved.

Seth (CTO) ran the RCA. Three amplifying bugs in
`internal/tui/app.go`'s `Update()` method combined into an unbounded
client-side retry storm:

1. **Site 1 — completion-edge re-fire** (app.go line ~160). The
   `tasksLoadedMsg` handler returned `m.refreshData()`, so every
   successful load immediately fired the next fetch with zero pacing.
   Under degraded conditions this became "completion is the rate
   limiter" — a guaranteed hot loop.

2. **Site 2 — tick compounding** (app.go line ~174, **primary
   engine**). Every `dataErrMsg` scheduled a new `tea.Tick` that
   re-armed itself via `periodicRefreshMsg`. The existing periodic
   tick at line ~205 also re-armed itself. `tea.Tick` does NOT
   cancel prior ticks. N errors → N+1 concurrent periodic loops,
   each re-arming forever. This is the unbounded-growth term.

3. **Site 3 — EventMsg amplifier** (app.go line ~200).
   `tea.Batch(m.refreshData(), listenForEvents(...))` on every event
   added a parallel refresh. Amplifier, not root cause.

Full design writeup: `/home/godinj/.drem-csuite/seth/outbox/20260421T235746Z-seth-to-kyle-tui-retry-storm-design.md`.

## Design

Per-endpoint single-flight gate + backoff hold window.

### Model additions

```go
tasksInflight      bool        // gate for /tasks
agentsInflight     bool        // gate for /workers
nextAllowedRefresh time.Time   // wall-clock hold window (zero = no hold)
nowFunc            func() time.Time  // test-injected clock; nil = time.Now
```

### Dispatch helpers (new, in `data_cmds.go`)

`dispatchTasksRefresh()` and `dispatchAgentsRefresh()` check
`canDispatch(inflight, nextAllowed, now)` — a pure predicate —
and either return the existing `loadTasks` / `loadAgents` Cmd
(having set `inflight = true`) or return nil (a no-op `tea.Cmd`).

Per-endpoint gating (two bools, not one) preserves today's
semantics where tasks and agents fetch independently: a slow
`/tasks` does not stall `/workers` and vice versa.

### Update handler changes

| Branch              | Before                                           | After                                                              |
| ------------------- | ------------------------------------------------ | ------------------------------------------------------------------ |
| `tasksLoadedMsg`    | `return m, m.refreshData()`                      | clear gate + backoff + hold; `return m, nil`                       |
| `agentsLoadedMsg`   | `return m, nil` (already fine)                   | clear gate + backoff + hold; `return m, nil`                       |
| `dataErrMsg`        | `return m, tea.Tick(m.dataBackoff, …)`           | clear BOTH gates, bump backoff, set nextAllowedRefresh; `return m, nil` |
| `EventMsg`          | `tea.Batch(m.refreshData(), listenForEvents(…))` | `tea.Batch(m.dispatchTasksRefresh(), listenForEvents(…))`          |
| `periodicRefreshMsg`| `tea.Batch(m.refreshData(), tea.Tick(…))`        | `tea.Batch(dispatchTasks, dispatchAgents, schedulePeriodicTick())` |

The periodic tick is a **singleton by construction**: scheduled
once in `Init()`, re-armed at exactly one call site in Update's
`periodicRefreshMsg` branch. Error paths do not schedule new ticks.
Adding a second `schedulePeriodicTick()` call anywhere else
re-introduces Site 2.

### Gate pre-arming in NewModel

`NewModel` starts with `tasksInflight = true` and
`agentsInflight = true`. `Init()` launches `loadTasks` / `loadAgents`
directly (bypassing the dispatch helpers), and the first
`periodicRefreshMsg` could otherwise fire a duplicate fetch before
the Init loads return. The first `tasksLoadedMsg` / `agentsLoadedMsg`
releases each gate.

### Gate release on error — CRITICAL

`dataErrMsg` clears BOTH gates. The msg does not carry its origin
(loadTasks and loadAgents both funnel into dataErrMsg), so we
conservatively clear both. Leaving either gate inflight would
strand it forever, silently freezing the dashboard.

## Test coverage matrix

File: `internal/tui/retry_storm_test.go`. Altitude-matched to
`datasource_backoff_test.go` where possible.

| # | Test                                            | Proves                                           |
| - | ----------------------------------------------- | ------------------------------------------------ |
| 1 | `SingleFlight_TasksDispatch` + `AgentsDispatch` + `PerEndpoint` | per-endpoint single-flight, independent gates |
| 2 | `TasksLoadedMsgReleasesGate` / `AgentsLoadedMsgReleasesGate`    | gate + hold + backoff cleared on success      |
| 3 | `DataErrMsgReleasesGateAndSetsBackoff`          | gate released on error, nextAllowedRefresh seeded |
| 4 | `CanDispatch_BackoffGate` + `Dispatch_HonoursBackoffWindow`     | pure predicate; injected-clock backoff walk   |
| 5 | **`SlowBackend_BoundsConcurrentDispatches`** (MANDATORY) | 30 ticks under 10s-latency fake → max 1 concurrent |
| 6 | `EventStorm_BoundsFetchInvocations` + `EventStorm_GateOpen_FiresOnce` | 100 events → ≤1 fetch         |
| 7 | `ConsecutiveErrors_NoTickCompounding`           | 5 consecutive err → 0 replacement ticks; Site 2 regression |
| 8 | `FastRecoveryAfterBackoff`                      | success after N errors → next dispatch fires immediately |

Test #5 is the production-mirror regression test. It simulates the
exact scenario from the 413%-CPU incident (a `/tasks` that takes
longer than the client timeout) and asserts the single-flight gate
holds concurrent dispatches to at most one across 30 periodic
ticks. Don't accept a PR without it.

## Risks addressed

1. **Gate leak on Cmd panic**: the dispatch helpers invoke the
   existing `loadTasks` / `loadAgents` Cmds, which always route
   through `dataFetchTimeout` (5s) and always emit either a
   `tasksLoadedMsg` / `agentsLoadedMsg` or `dataErrMsg`. No third
   path. Invariant documented at `dispatchTasksRefresh` in
   `data_cmds.go`.

2. **Reduced responsiveness perception**: removing completion-edge
   re-fire slows maximum refresh rate from "every load" to "every
   `periodicRefreshInterval`" (2 s). This is net good — the prior
   behaviour was a bug. Do not restore completion-edge re-fire.

3. **EventMsg swallow**: events arriving while tasksInflight=true
   now drop their fetch trigger. The data lands on the next
   periodic tick at most 2 s later. Acceptable for a dashboard;
   comment in `app.go` EventMsg branch explains the deliberate gate.

4. **Constitution**: no new internal imports added to
   `internal/tui/`. `time` is stdlib. File length ceiling
   respected: `app.go` grew 532 → 602 lines (under 800 cap). The
   growth is documentation comments explaining each site —
   load-bearing context that prevents a future "helpful" revert.

## Open followups

- **Watchdog** (Seth's risk #1 belt-and-braces): if `tasksInflight`
  is true but no msg has arrived in 2× the 5 s client timeout
  (10 s), treat as a synthetic `dataErrMsg("local stall")`.
  Intentionally NOT implemented this turn — the per-Cmd 5 s
  timeout is the real fix. File as a followup if a local stall
  ever actually strands the gate in production.

- **"Polling paused, waiting Ns" banner polish**: the existing
  `connLine` banner in `app.go View()` reads `m.dataErr` and
  `m.dataBackoff`. Could optionally render
  `time.Until(m.nextAllowedRefresh)` when non-zero for a stronger
  degraded-state signal. ~5 LOC. Not required.

- **Server-side load-shedding (Bug E)**: unrelated, being filed in
  parallel. This TUI fix removes the client-side DDoS vector; the
  orchestrator should still refuse to melt under a misbehaving
  client, but that is a separate task.
