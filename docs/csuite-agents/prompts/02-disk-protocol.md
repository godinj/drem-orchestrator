# Agent: Disk Protocol & Bootstrap

You are working on the `master` branch of drem-orchestrator, a Go-based multi-agent task orchestrator.
Your task is implementing the disk-based communication protocol: shell library functions for message creation, parsing, routing, and archiving, plus a bootstrap script that creates the shared directory tree.

## Context

Read these specs before starting:
- `docs/csuite-agents/prd-csuite-agents.md` (sections: "Disk Communication Protocol", "Agent Discovery and Health", "Temp Worker Framework")

## Deliverables

### New files (`scripts/`)

#### 1. `scripts/csuite-bootstrap.sh`

Creates the `~/.drem-csuite/` directory tree for all agents. Idempotent — safe to run multiple times.

```bash
#!/usr/bin/env bash
# Creates the C-Suite agent communication directory structure.
# Usage: bash scripts/csuite-bootstrap.sh
```

Directories to create:
```
~/.drem-csuite/
  ├── kyle/
  │   ├── inbox/
  │   ├── inbox/archive/
  │   ├── outbox/
  │   └── state.md           (touch if not exists — do not overwrite)
  ├── mike/
  │   ├── inbox/
  │   ├── inbox/archive/
  │   ├── outbox/
  │   └── state.md
  ├── alex/
  │   ├── inbox/
  │   ├── inbox/archive/
  │   ├── outbox/
  │   └── state.md
  ├── ross/
  │   ├── inbox/
  │   ├── inbox/archive/
  │   ├── outbox/
  │   └── state.md
  ├── seth/
  │   ├── inbox/
  │   ├── inbox/archive/
  │   ├── outbox/
  │   └── state.md
  └── temp-workers/           (just the parent dir — worker dirs created on demand)
```

Print a summary of what was created. Exit 0 on success.

#### 2. `scripts/csuite-proto.sh`

Shell library of functions for the disk communication protocol. Designed to be sourced by agent prompts or other scripts.

```bash
#!/usr/bin/env bash
# C-Suite disk communication protocol library.
# Source this file: source scripts/csuite-proto.sh
#
# Requires: CSUITE_DIR (defaults to ~/.drem-csuite)
```

**Functions to implement:**

```bash
# Send a message to an agent's inbox.
# Creates a timestamped markdown file with YAML frontmatter.
# Usage: csuite_send <from> <to> <subject> <priority> <type> <body>
#   from: sender name (kyle, mike, alex, ross, seth, temp-worker-NNN)
#   to: recipient name (kyle, mike, alex, ross, seth)
#   subject: message subject string
#   priority: low | medium | high | critical
#   type: observation | request | report | decision
#   body: message body (markdown string)
# Writes to: $CSUITE_DIR/<to>/inbox/<timestamp>-<from>.md
# Filename format: YYYYMMDD-HHMMSS-<from>.md (ensures sort order)
csuite_send() { ... }

# List unprocessed messages in an agent's inbox.
# Usage: csuite_inbox <agent>
# Output: one line per message: <filename> | <from> | <priority> | <subject>
# Sorted by filename (chronological). Excludes archive/ subdirectory.
csuite_inbox() { ... }

# Read a single message from an agent's inbox.
# Usage: csuite_read <agent> <filename>
# Output: full message content (frontmatter + body)
csuite_read() { ... }

# Archive a processed message (move from inbox to inbox/archive).
# Usage: csuite_archive <agent> <filename>
csuite_archive() { ... }

# Parse YAML frontmatter field from a message file.
# Usage: csuite_field <file> <field>
# Example: csuite_field msg.md "priority" → "high"
# Uses grep/sed — no external YAML parser needed.
csuite_field() { ... }

# Update an agent's state file with a heartbeat timestamp.
# Appends or replaces the "last_heartbeat" line in state.md.
# Usage: csuite_heartbeat <agent>
csuite_heartbeat() { ... }

# Check if an agent's heartbeat is fresh (updated within N seconds).
# Usage: csuite_is_alive <agent> <max_age_seconds>
# Exit code: 0 if alive, 1 if stale or no heartbeat found.
csuite_is_alive() { ... }

# Create a temp worker directory structure.
# Usage: csuite_create_worker <worker_id>
# Creates: $CSUITE_DIR/temp-workers/<worker_id>/{inbox,inbox/archive,outbox,state.md}
# Returns the worker directory path.
csuite_create_worker() { ... }

# Block until a message signal arrives or timeout expires.
# Usage: csuite_wait_for_inbox <agent> [timeout_seconds]
# Uses inotifywait to watch for a .signal file in the agent's inbox.
# Returns 0 if signal received, 1 on timeout.
# The .signal file carries no content — callers check unarchived .md files after waking.
csuite_wait_for_inbox() { ... }

# List all temp worker directories.
# Usage: csuite_list_workers
# Output: one line per worker: <worker_id> | <state summary>
csuite_list_workers() { ... }
```

**Message file format:**

```markdown
---
from: mike
to: alex
timestamp: 2026-03-23T14:30:00Z
subject: "High failure rate in merge phase"
priority: high
type: observation
---

Message body in markdown.
```

Implementation notes:
- Use `date -u +%Y%m%d-%H%M%S` for timestamp generation in filenames
- Use `date -u +%Y-%m-%dT%H:%M:%SZ` for the YAML timestamp field
- `csuite_field` should handle quoted and unquoted values
- `csuite_inbox` should use `find` with `-maxdepth 1` to exclude archive/
- All functions should validate that `$CSUITE_DIR` exists and the target agent directory exists before operating

#### 3. `scripts/csuite-proto_test.sh`

Test script that validates the protocol library. Uses a temporary directory as `CSUITE_DIR` to avoid touching the real `~/.drem-csuite/`.

```bash
#!/usr/bin/env bash
# Tests for csuite-proto.sh
# Usage: bash scripts/csuite-proto_test.sh
# Exit code: 0 if all tests pass, 1 if any fail.
```

**Test cases:**

- `test_bootstrap` — Run bootstrap in temp dir. Verify all expected directories exist. Verify state.md files exist. Run again to verify idempotency (no errors, no data loss).
- `test_send_creates_message` — Send a message from mike to alex. Verify file exists in alex's inbox. Verify filename matches `YYYYMMDD-HHMMSS-mike.md` pattern. Verify frontmatter contains correct from/to/subject/priority/type fields. Verify body content is present.
- `test_inbox_lists_messages` — Send 3 messages to kyle from different agents. Run `csuite_inbox kyle`. Verify 3 lines output. Verify chronological order.
- `test_archive_moves_message` — Send a message. Archive it. Verify it's no longer in inbox listing. Verify it exists in inbox/archive/.
- `test_field_parsing` — Create a message with known frontmatter. Verify `csuite_field` extracts each field correctly. Test both quoted and unquoted values.
- `test_heartbeat_and_alive` — Run `csuite_heartbeat kyle`. Verify `csuite_is_alive kyle 60` returns 0. Sleep 2 seconds. Verify `csuite_is_alive kyle 1` returns 1 (stale).
- `test_create_worker` — Create a temp worker. Verify directory structure exists. Verify state.md exists.
- `test_list_workers` — Create 2 workers. Verify `csuite_list_workers` shows both.

Use a simple test harness pattern:
```bash
PASS=0; FAIL=0
assert_eq() { if [ "$1" = "$2" ]; then ((PASS++)); else echo "FAIL: expected '$2', got '$1'"; ((FAIL++)); fi }
# ... run tests ...
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ] || exit 1
```

## Scope Limitation

- Do NOT write any Go code — this is purely shell scripting.
- Do NOT implement agent logic — this is infrastructure only.
- Do NOT install external tools — use only bash builtins, `date`, `find`, `grep`, `sed`, `mv`, `mkdir`, `cat`, `stat`.
- The protocol library must work on Linux (the target platform). macOS `date` compatibility is not required.

## Conventions

- Scripts use `#!/usr/bin/env bash`
- Use `set -euo pipefail` in executable scripts (not in the library that gets sourced)
- Quote all variable expansions
- Functions that fail should return non-zero exit codes, not call `exit`
- No file should exceed 800 lines
- Verification: `bash scripts/csuite-proto_test.sh`
