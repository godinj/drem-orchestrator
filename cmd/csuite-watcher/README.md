# csuite-watcher

`csuite-watcher` publishes structured events to the C-Suite event bus — a local SQLite database used to coordinate C-Suite agent activity.

## Usage

```
csuite-watcher <subcommand> [flags] [args]
```

### Subcommands

#### run

Start the watcher main loop. This starts all trigger sources (inbox signals,
event delivery, safety timer), routes wake-ups to the lifecycle manager, and
writes heartbeats to prove liveness:

```
csuite-watcher run [--config <path>]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `drem.toml` | Path to the drem.toml config file |

The `run` command reads the `[watcher]` section from drem.toml:

```toml
[watcher]
  db_path             = "~/.drem-csuite/watcher.db"
  inbox_base_dir      = "~/.drem-csuite"
  allowed_agents      = ["mike", "alex", "seth"]
  inbox_poll_interval = "2s"
  safety_interval     = "5m"
  heartbeat_interval  = "30s"
```

| Field | Default | Description |
|-------|---------|-------------|
| `db_path` | `~/.drem-csuite/watcher.db` | SQLite database for metrics and heartbeats |
| `inbox_base_dir` | `~/.drem-csuite` | Root directory for `<agent>/inbox/` signal files |
| `allowed_agents` | `["mike", "alex", "seth"]` | Agents permitted to run turns |
| `inbox_poll_interval` | `2s` | How often to scan for `.signal` files |
| `safety_interval` | `5m` | How often the safety timer wakes "mike" |
| `heartbeat_interval` | `30s` | How often heartbeats are written to DB |

**Signal handling:** SIGTERM and SIGINT cause a clean shutdown. All triggers are
stopped, in-flight turns complete, and the process exits with code 0.

**Exit codes:**

| Code | Meaning |
|------|---------|
| `0` | Clean shutdown (SIGTERM/SIGINT or context cancellation) |
| non-zero | Error occurred during startup; error message written to stderr |

#### event

Publish a single event from a JSON argument:

```
csuite-watcher event [--db <path>] [--routing <path>] '<json>'
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--db` | `~/.drem-csuite/csuite.db` | Path to the event bus SQLite database |
| `--routing` | *(none)* | Path to the event routing config file |

**Exit codes:**

| Code | Meaning |
|------|---------|
| `0` | Event published successfully |
| non-zero | Error occurred; error message written to stderr |

## JSON Payload Format

The `event` subcommand accepts a single JSON argument with the following fields:

```json
{
  "type":        "task_status_changed",
  "task_id":     "42",
  "from_status": "planning",
  "to_status":   "failed",
  "details":     "context exhaustion",
  "timestamp":   "2026-03-26T14:30:00Z"
}
```

| Field | Description |
|-------|-------------|
| `type` | Event type discriminator (e.g. `task_status_changed`) |
| `task_id` | ID of the task this event relates to |
| `from_status` | Previous task status |
| `to_status` | New task status |
| `details` | Free-text detail string |
| `timestamp` | RFC 3339 timestamp; maps to `CreatedAt` in the event record |

The `Source` field is automatically set to `"csuite-watcher"` by the binary.
Event `ID` is auto-assigned by the bus — do not include it in the JSON payload.

## Examples

Publish a task-status-changed event:

```sh
csuite-watcher event '{"type":"task_status_changed","task_id":"99","from_status":"in_progress","to_status":"done","details":"","timestamp":"2026-03-26T10:00:00Z"}'
```

Use a custom database path:

```sh
csuite-watcher event --db /tmp/test.db '{"type":"task_status_changed","task_id":"1","from_status":"backlog","to_status":"planning","details":"","timestamp":"2026-03-26T09:00:00Z"}'
```

---

## Trigger System (`internal/watcher`)

The watcher package provides a generic trigger system for waking C-Suite agents
based on external signals. This section documents the interfaces and the first
trigger implementation.

### Trigger interface

```go
type Trigger interface {
    Start(ctx context.Context) error
    Stop() error
    Events() <-chan TriggerEvent
}
```

**Lifecycle contract:**

1. Call `Start(ctx)` to begin monitoring. Monitoring runs in a background goroutine; `Start` is non-blocking.
2. Read from `Events()` to receive `TriggerEvent` values as agents are woken.
3. Call `Stop()` to halt monitoring. `Stop` is idempotent. After `Stop` returns the `Events()` channel is closed, so a `range` loop over `Events()` terminates naturally.

```go
t := NewInboxSignalTrigger(baseDir, pollInterval)
if err := t.Start(ctx); err != nil {
    log.Fatal(err)
}
defer t.Stop()

for event := range t.Events() {
    // event.AgentName — agent that should be woken
    // event.Source    — "inbox-signal"
    // event.Timestamp — when the signal was detected
    waker.Wake(event.AgentName)
}
```

### AgentWaker interface

```go
type AgentWaker interface {
    Wake(agentName string) error
}
```

`AgentWaker` is the consumption-site interface used by trigger consumers to
wake agents without importing the lifecycle package directly. It is satisfied
by a thin adapter over `LifecycleManager.TriggerAgent` (Phase 4 task 4).
This decoupling keeps the trigger package testable without a real lifecycle
manager.

### InboxSignalTrigger

`InboxSignalTrigger` watches for `*.signal` files in `<baseDir>/<agent>/inbox/`
directories. It is the primary wake mechanism for C-Suite agents.

**Polling interval:** The trigger polls at a configurable `pollInterval`
(default 2 seconds) for signal files. Agent directories created after `Start`
are discovered automatically — no restart is required.

**Signal detection:** On each poll tick, `InboxSignalTrigger` globs
`<baseDir>/*/inbox/*.signal`. Any matching file causes a `TriggerEvent` to be
emitted for the owning agent.

**Cleanup semantics:** After a `.signal` file is detected and the wake event is
emitted, the file is removed to prevent re-triggering. If removal fails (e.g.,
a concurrent cleanup already deleted it), the error is logged and the event is
still emitted.

**Graceful handling of missing directories:** If `<baseDir>` does not exist or
contains no agent directories, `InboxSignalTrigger` starts without error and
delivers no events. New agent directories are discovered on subsequent polls.

#### Waking an agent from the shell

To wake a specific C-Suite agent, create a `.signal` file in its inbox:

```sh
# Wake the "planner" agent
mkdir -p ~/.drem-csuite/planner/inbox
touch ~/.drem-csuite/planner/inbox/wake.signal
```

This matches the `_csuite_notify` function used by `csuite-proto.sh`. Any
filename ending in `.signal` is treated as a wake signal — `wake.signal`,
`message.signal`, etc. all work equivalently.

#### Wiring InboxSignalTrigger to a consumer

```go
baseDir := filepath.Join(os.Getenv("HOME"), ".drem-csuite")
trigger := watcher.NewInboxSignalTrigger(baseDir, 2*time.Second)

ctx, cancel := context.WithCancel(context.Background())
defer cancel()

if err := trigger.Start(ctx); err != nil {
    log.Fatal(err)
}
defer trigger.Stop()

for event := range trigger.Events() {
    if err := lifecycleMgr.Wake(event.AgentName); err != nil {
        log.Printf("wake %s: %v", event.AgentName, err)
    }
}
```
