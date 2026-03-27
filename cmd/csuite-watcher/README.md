# csuite-watcher

`csuite-watcher` publishes structured events to the C-Suite event bus — a local SQLite database used to coordinate C-Suite agent activity.

## Usage

```
csuite-watcher <subcommand> [flags] [args]
```

### Subcommands

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
