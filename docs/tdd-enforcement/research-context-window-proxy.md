# Research: API Proxy for Claude Code Context Window Monitoring

**Date**: 2026-03-11
**Status**: Research needed
**Context**: [PRD: TDD Enforcement](prd-tdd-enforcement.md) — §4.6.2 Context Window Monitoring

---

## Objective

Design and prototype a lightweight HTTP reverse proxy that sits between Claude Code CLI sessions and the Anthropic API. The proxy's job is to intercept API responses, extract token usage metrics, and write them to a per-agent file that the drem-orchestrator can poll.

The orchestrator spawns Claude Code agents in tmux sessions. It needs to know each agent's cumulative context window usage so it can intervene (stop the agent, spawn a fixer) before the agent exhausts its context.

---

## Research Questions

Answer each of these with specifics (exact field names, env vars, code examples):

### 1. Claude Code API Configuration

- How does Claude Code determine which API endpoint to call? Check for: `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_BASE`, `API_BASE`, or similar env vars.
- Does Claude Code respect `HTTP_PROXY` / `HTTPS_PROXY` env vars?
- Can the base URL be set via `~/.claude/settings.json` or another config file?
- What is the default endpoint? (`https://api.anthropic.com/v1/messages`?)
- Does Claude Code pin TLS certificates or do anything that would prevent proxying?

### 2. Anthropic API Response Format

- What does the `usage` field look like in a non-streaming Messages API response? List exact field names and types.
- For streaming responses (which Claude Code almost certainly uses), how is usage reported? Is it in a `message_start`, `message_delta`, or `message_stop` event?
- Is there a `cache_creation_input_tokens` / `cache_read_input_tokens` field? How does prompt caching affect the usage numbers?
- What is `input_tokens` measuring — just the user turn, or the full conversation context?
- What are the context window sizes for: Claude Opus 4, Claude Sonnet 4, Claude Haiku 3.5?

### 3. Proxy Design

Design a minimal reverse proxy in Go that:

- Listens on `localhost:<port>` (port per agent, or path-based routing)
- Forwards all requests to `https://api.anthropic.com` with TLS
- For streaming responses (`text/event-stream`), tees the stream — forwards to Claude Code AND parses SSE events to extract usage
- For non-streaming responses, reads the body, extracts usage, forwards to Claude Code
- Writes usage metrics to a JSON file at a configurable path (e.g., `/path/to/worktree/.claude/context-usage.json`)

The output file should look like:

```json
{
  "model": "claude-opus-4-20250514",
  "max_context_tokens": 200000,
  "turns": [
    {
      "timestamp": "2026-03-11T10:30:00Z",
      "input_tokens": 15000,
      "output_tokens": 2000,
      "cache_read_tokens": 5000,
      "cache_creation_tokens": 1000
    }
  ],
  "cumulative_input_tokens": 15000,
  "cumulative_output_tokens": 2000,
  "estimated_context_usage_pct": 8.5
}
```

Key design considerations:
- **Latency**: The proxy must add negligible latency. It's in the hot path of every API call.
- **Streaming fidelity**: The proxy must not break SSE streaming. Claude Code expects chunked responses.
- **Crash safety**: If the proxy crashes, Claude Code should fail visibly (not hang). Consider what happens.
- **Multiple agents**: The orchestrator runs up to 4 agents concurrently. Each needs isolated metrics. Options: one proxy per agent (different ports), or one shared proxy with path-based routing keyed by agent ID.
- **Context window estimation**: `input_tokens` on the latest turn should approximate current context size (it includes the full conversation). Confirm this is true for the Anthropic API.

### 4. Integration with drem-orchestrator

- How would the orchestrator configure Claude Code to use the proxy? (env var in the tmux session?)
- How would the orchestrator read the metrics file? (poll on tick? fsnotify?)
- What's the right abstraction boundary? Propose a Go interface that the orchestrator would use:

```go
type ContextMonitor interface {
    // CurrentUsage returns the latest context window usage for an agent.
    CurrentUsage(agentID uuid.UUID) (*ContextUsage, error)
}

type ContextUsage struct {
    InputTokens       int
    OutputTokens      int
    MaxContextTokens  int
    UsagePercent      float64
    LastUpdated       time.Time
}
```

### 5. Alternatives Considered

Briefly evaluate these alternatives and explain why the proxy approach is preferred (or isn't):

- **Compaction event detection via tmux capture** — monitoring for "Summarizing conversation..." messages
- **Output volume estimation** — tracking total bytes of tmux pane output
- **Claude Code hooks** — using post-message hooks to extract usage (if hooks have access to token counts)
- **Patching Claude Code** — modifying Claude Code to write usage metrics directly (maintenance burden?)

---

## Deliverables

1. Answers to all research questions above with citations (API docs, source code, testing)
2. A working Go prototype of the proxy in `internal/contextproxy/` (or a standalone `cmd/` if cleaner)
3. Integration design: how the orchestrator spawns the proxy, passes config to Claude Code, and reads metrics
4. Test plan for the proxy (unit tests for SSE parsing, integration test with a real Claude Code session)
