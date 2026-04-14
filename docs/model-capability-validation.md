# Model Capability Validation

The orchestrator validates model capabilities at startup, before any agents are dispatched. If a configured model lacks capabilities required by its agent type, the orchestrator exits immediately with a clear error message.

## Startup Behavior

After loading `drem.toml`, the orchestrator calls `ValidateModelCapabilities()` which checks:

1. **Base agent configs** (`[agents.*]` sections)
2. **All profile overrides** (`[profiles.*.agents.*]` sections)

**Errors** (known model missing required capabilities) cause `log.Fatalf` — the orchestrator will not start.

**Warnings** (unknown models that can't be verified) are logged via `log.Printf` but do not block startup.

## Capabilities Checked Per Agent Type

| Agent Type   | Required Capabilities              |
|-------------|-----------------------------------|
| classifier  | tool_calling                       |
| planner     | tool_calling, extended_thinking    |
| coder       | tool_calling, extended_thinking    |
| reviewer    | tool_calling                       |
| fixer       | tool_calling                       |
| researcher  | tool_calling                       |

## Known Models

| Model Prefix       | Capabilities                                      |
|--------------------|--------------------------------------------------|
| claude-opus-4      | tool_calling, extended_thinking, code_execution   |
| claude-sonnet-4    | tool_calling, extended_thinking, code_execution   |
| claude-haiku-4     | tool_calling, extended_thinking, code_execution   |
| claude-3-5-sonnet  | tool_calling, extended_thinking                   |
| claude-3-5-haiku   | tool_calling                                      |
| claude-3-haiku     | tool_calling                                      |

Models are matched by longest prefix first, so `claude-sonnet-4-6-20250514` matches `claude-sonnet-4`.

## Unknown Models

Models that don't match any known prefix produce a warning but do not block startup. This is intentional — new model IDs may be released before the registry is updated.

## OpenCode Provider

Agents configured with `provider = "opencode"` are skipped entirely. OpenCode uses a different capability model and the registry doesn't apply.

## Fixing Capability Errors

If the orchestrator fails with a capability validation error, update the model in `drem.toml`:

```toml
# ERROR: planner requires extended_thinking, but claude-3-haiku lacks it.
# Fix: use a model that supports extended_thinking.
[agents.planner]
  model = "claude-sonnet-4-6"  # supports all capabilities
```

For profile overrides:

```toml
[profiles.fast.agents.coder]
  model = "claude-sonnet-4-6"  # must support extended_thinking for coder
```
