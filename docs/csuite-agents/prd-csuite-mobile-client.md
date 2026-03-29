# PRD: C-Suite Mobile Voice Client

## Problem Statement

The orchestrator operator currently interacts with the C-Suite agent team exclusively through terminal sessions — either Kyle's interactive tmux session or the TUI dashboard. This requires the operator to be at their workstation. When away from the desk (driving, walking, or otherwise mobile), the operator has no visibility into C-Suite activity and no way to respond to agent messages, approve decisions, or issue commands.

The C-Suite agents already communicate via SQLite-backed messages (`CsuiteInboxMessage`), and Kyle is designed as the operator's direct contact. But the only interface to that communication layer is the terminal. The operator needs a mobile-friendly, voice-capable interface to maintain situational awareness and respond to agents from their phone.

## Solution

Build a **Progressive Web App (PWA)** that connects to a new **bridge server** (a subcommand of `csuite-watcher`) over WebSocket. The PWA provides a chat interface with per-agent conversation threads, voice controls (tap-to-talk speech-to-text and tap-to-listen text-to-speech), configurable quick-action buttons, and slash command support. The bridge server exposes the existing `CsuiteInboxMessage` SQLite store over an authenticated WebSocket and REST API.

The system consists of two components:

- **`csuite-watcher serve`** — A new subcommand that starts an HTTP/WebSocket server. It reads from and writes to the existing csuite SQLite database, pushes new messages to connected clients in real time, and validates bearer token authentication on every connection.
- **PWA client** — A lightweight web application served by the bridge server itself. It renders per-agent conversation threads, provides voice controls via the Web Speech API, and stores quick-action button configuration in browser localStorage.

The operator's messages are written to the database with `from_agent: "operator"`, consistent with the existing convention used by `csuite-launch.sh`.

## User Stories

1. As an operator, I want to open a URL on my phone and see my conversations with C-Suite agents, so that I can check on the system without being at my workstation.
2. As an operator, I want per-agent tabs (Kyle, Mike, Seth, Alex, Ross), so that I can see my conversation history with each agent separately.
3. As an operator, I want each tab to show messages between me and that agent in chronological order, so that I can follow the conversation thread naturally.
4. As an operator, I want to type a message and send it to the currently selected agent, so that I can communicate with any C-Suite agent directly.
5. As an operator, I want new messages from agents to appear in real time without refreshing, so that I have live visibility into agent activity.
6. As an operator, I want a large "play" button on each message to hear it read aloud via text-to-speech, so that I can listen to agent responses while driving.
7. As an operator, I want a "record" button that I tap to start and tap again to stop voice recording, so that I can dictate messages hands-free using speech-to-text.
8. As an operator, I want the speech-to-text transcription to appear in the message input field before sending, so that I can review and edit what I said before it goes to the agent.
9. As an operator, I want configurable quick-action buttons (e.g., "status", "check", "yes") that send preset messages to the current agent with one tap, so that I can respond quickly without typing or speaking.
10. As an operator, I want to add, remove, reorder, and edit quick-action buttons from my phone, so that I can customize my workflow without editing server configuration.
11. As an operator, I want quick-action button configuration to persist across browser sessions, so that I don't lose my customizations.
12. As an operator, I want to send slash commands (e.g., `/pause`, `/status`) as regular messages, so that agents can interpret them according to their existing protocols.
13. As an operator, I want the PWA to authenticate with the bridge server using a bearer token, so that my C-Suite communication is not exposed to unauthorized access.
14. As an operator, I want the bridge server to be accessible over the internet via my existing DNS and SSH setup, so that I can use the PWA from anywhere, not just my local network.
15. As an operator, I want unread message indicators on agent tabs, so that I can see which agents have sent me new messages.
16. As an operator, I want the PWA to be installable on my Android home screen, so that it feels like a native app.
17. As an operator, I want the interface to be usable with large touch targets, so that I can interact with it safely while driving (at stops).
18. As an operator, I want the bridge server to start as a subcommand of the existing `csuite-watcher` binary, so that no new binary or deployment is needed.
19. As an operator, I want the PWA to reconnect automatically if the WebSocket connection drops, so that I don't have to manually refresh when connectivity is intermittent.
20. As an operator, I want to scroll back through message history in each agent thread, so that I can review earlier parts of the conversation.

## Implementation Decisions

### Bridge Server (`csuite-watcher serve`)

The bridge server is a new subcommand of the existing `csuite-watcher` binary. It starts an HTTP server that serves both the PWA static assets and a WebSocket endpoint for real-time communication.

**Endpoints:**

- `GET /` — Serves the PWA (HTML/JS/CSS, either embedded via `embed.FS` or served from a static directory)
- `GET /ws` — WebSocket endpoint for real-time bidirectional message streaming
- `GET /api/messages?agent=<name>&limit=<n>&before=<id>` — REST endpoint for paginated message history (initial load and scroll-back)
- `GET /api/agents` — REST endpoint listing all agents with unread counts
- `POST /api/messages` — REST endpoint for sending a message (fallback if WebSocket is unavailable)

**WebSocket protocol:**

Messages between the PWA and bridge server are JSON-encoded:

```json
// Client → Server: send a message
{"type": "send", "to": "kyle", "body": "status"}

// Server → Client: new message arrived
{"type": "message", "msg": { CsuiteInboxMessage fields }}

// Server → Client: agent status update (unread counts)
{"type": "agents", "agents": [{"name": "kyle", "unread": 3}, ...]}
```

**Real-time delivery:**

The bridge server polls the csuite SQLite database on a short interval (1-2 seconds) for new messages where `to_agent = "operator"` and pushes them to connected WebSocket clients. This avoids adding change-notification infrastructure to the existing store — polling is simple and the message volume is low.

**Authentication:**

Every HTTP request and WebSocket upgrade must include a bearer token. The token is configured in `drem.toml` under a new `[serve]` section:

```toml
[serve]
  listen = ":8443"
  token  = "generated-secret-here"
```

A convenience subcommand `csuite-watcher token` generates a cryptographically random token and prints it to stdout. The operator copies this into their config and into the PWA's login screen on their phone.

**TLS:**

The bridge server should support TLS directly (cert/key paths in config) since it will be internet-exposed. The operator's existing external DNS setup provides the hostname; the bridge server provides the HTTPS termination.

```toml
[serve]
  tls_cert = "/path/to/cert.pem"
  tls_key  = "/path/to/key.pem"
```

### PWA Client

The PWA is a single-page application built with vanilla HTML, CSS, and JavaScript (no framework). It is served by the bridge server itself, so there is no separate deployment.

**Layout:**

- **Top bar** — Agent tab strip (Kyle, Mike, Seth, Alex, Ross) with unread badges
- **Middle** — Scrollable message thread for the selected agent. Operator messages on the right, agent messages on the left. Each agent message has a play button (speaker icon) for TTS.
- **Bottom bar** — Quick-action buttons row, then the input area with a text field, record button (microphone icon), and send button

**Voice controls:**

- **Text-to-speech (TTS):** Each agent message bubble has a speaker icon button. Tapping it reads that message aloud using the Web Speech API (`SpeechSynthesis`). Tapping again stops playback. No auto-play.
- **Speech-to-text (STT):** A microphone button next to the text input. Tap to start recording, tap again to stop. The Web Speech API (`SpeechRecognition`) transcribes speech into the text input field. The operator can review/edit the transcription before tapping send.

**Quick-action buttons:**

- A horizontal row of buttons above the input area. Each button has a label and a message payload. Tapping a button sends the payload as a message to the currently selected agent.
- Default buttons: "status", "check", "yes" (configured on first load).
- Configuration screen (accessible via a gear icon) allows adding, removing, reordering, and editing buttons. Both the label and message payload are editable.
- Configuration is stored in browser `localStorage`. No server-side persistence.

**Message rendering:**

- Messages are rendered as markdown (agent messages often contain structured content). A lightweight markdown renderer (e.g., marked.js) converts message bodies to HTML.
- Timestamps are shown in the operator's local timezone.
- Messages are loaded in reverse-chronological pages (newest at bottom, scroll up for history) via the REST `/api/messages` endpoint on initial load, then appended in real time via WebSocket.

**Unread tracking:**

- The PWA tracks the last-seen message ID per agent in `localStorage`.
- Agent tabs show a badge count of messages newer than the last-seen ID.
- Switching to a tab marks all messages in that thread as seen.

**Offline and reconnection:**

- The PWA shows a visible "disconnected" indicator when the WebSocket drops.
- It attempts automatic reconnection with exponential backoff (1s, 2s, 4s, up to 30s max).
- On reconnection, it fetches any missed messages via the REST endpoint.

**Installability:**

- The PWA includes a `manifest.json` and service worker sufficient for Android "Add to Home Screen" installation.
- The service worker caches the static assets (HTML, JS, CSS) for fast startup. It does not cache message data.

### Database Interaction

The bridge server reuses the existing `csuite.Store` from `internal/csuite/store.go`. It calls:

- `Store.GetMessagesByAgent(agentName)` — for thread loading (filtered to operator conversations)
- `Store.CreateMessage(msg)` — for operator-sent messages
- `Store.ListAgents()` — for the agent tab list
- `Store.UnreadCountByAgent()` — for badge counts (filtered to operator)

The bridge server may need a small number of additional query methods on the store to support operator-scoped filtering (messages between "operator" and a specific agent) and cursor-based pagination. These should be added to the existing `Store` type rather than creating a new data access layer.

### Configuration

All bridge server configuration lives under a new `[serve]` section in `drem.toml`:

```toml
[serve]
  listen   = ":8443"          # listen address
  token    = ""                # bearer token (required)
  tls_cert = ""                # path to TLS certificate
  tls_key  = ""                # path to TLS private key
  db_path  = "~/.drem-csuite/csuite.db"  # csuite database (defaults to watcher db)
```

## Testing Decisions

A good test for this feature verifies observable behavior through the module's public interface — HTTP responses, WebSocket message sequences, and database state transitions — without mocking internal implementation details or requiring a real browser.

### Modules to test

**Bridge server HTTP/WebSocket handlers:** Test that REST endpoints return correct JSON for known database states. Test that the WebSocket endpoint rejects unauthenticated connections. Test that sending a message via WebSocket results in a `CsuiteInboxMessage` row in the database with the correct fields. Test that new messages addressed to "operator" are pushed to connected WebSocket clients. Prior art: existing watcher integration tests in `internal/watcher/` that set up test databases and verify behavior through public interfaces.

**Store query extensions:** Test any new query methods added to `csuite.Store` for operator-scoped filtering and pagination. Verify correct filtering (only messages between "operator" and a specific agent), correct ordering (chronological), and correct cursor behavior (before/after a given message ID). Prior art: existing store tests in `internal/csuite/store_test.go`.

**Authentication middleware:** Test that requests without a token receive 401. Test that requests with an invalid token receive 401. Test that requests with a valid token proceed. Test that WebSocket upgrade requests are subject to the same validation.

**PWA client:** The PWA is vanilla HTML/JS with no build step. Manual testing on an Android device is the primary verification method. Automated browser testing is out of scope for the initial implementation.

## Out of Scope

- **Disk-to-SQLite message migration** — Agents that still write to disk inboxes are not synced to SQLite by this feature. That is a separate concern.
- **Push notifications** — The PWA requires the browser/tab to be open. OS-level push notifications (via service worker push API) are a future enhancement.
- **iOS-specific optimizations** — The PWA targets Android. It may work on iOS Safari but iOS-specific issues are not addressed.
- **Multiple simultaneous operators** — The system assumes a single operator. There is no multi-user auth, per-user read tracking, or session isolation.
- **Message editing or deletion from the PWA** — Messages are append-only from the operator's perspective.
- **Agent lifecycle control from the PWA** — The operator cannot start, stop, or restart agents from the mobile client. That remains a terminal operation.
- **Video, images, or file attachments** — Messages are text/markdown only.
- **End-to-end encryption** — TLS provides transport encryption. Messages are stored in plaintext in SQLite, consistent with the existing system.

## Further Notes

- The bridge server is the first HTTP-serving component in the drem-orchestrator codebase. The Go standard library `net/http` and `golang.org/x/net/websocket` (or `github.com/gorilla/websocket`) are sufficient — no web framework is needed. This establishes a pattern for any future web-facing features.
- The polling approach for real-time delivery (bridge polls SQLite every 1-2s) is intentionally simple. The message volume is low (dozens per hour, not thousands per second). If latency becomes a concern, the watcher's existing event delivery mechanism could be extended to notify the bridge server directly, but this optimization is not needed initially.
- The quick-action buttons are a low-cost, high-value feature for driving scenarios. The operator will likely discover their most-used commands quickly and configure buttons accordingly. The default set ("status", "check", "yes") covers the common case of checking in and approving agent decisions.
- Web Speech API browser support: `SpeechSynthesis` (TTS) is well-supported on Android Chrome. `SpeechRecognition` (STT) is supported on Android Chrome but requires an internet connection (it uses Google's cloud speech service). This is acceptable since the PWA already requires connectivity for the WebSocket.
- The bearer token authentication is minimal but appropriate for a single-operator system. The token should be long (32+ bytes, base64-encoded) and transmitted only over TLS. If the operator's threat model changes (e.g., shared access), a more robust auth system can be added later.
