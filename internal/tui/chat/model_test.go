package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/godinj/drem-orchestrator/internal/bridgeclient"
)

func TestHandleWSEventAppendsActiveAgentMessage(t *testing.T) {
	m := New(nil, "")
	m.agents = []bridgeclient.Agent{{Name: "kyle"}}
	m.width = 80
	m.height = 24
	m.recalcLayout()

	msg := testMessage("msg-1", "kyle", "operator", "hello from kyle", "2026-04-23T12:00:00Z")
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}

	m.handleWSEvent(bridgeclient.WSEvent{Type: "new_message", Data: data})

	if got := len(m.messages["kyle"]); got != 1 {
		t.Fatalf("messages for kyle = %d, want 1", got)
	}
	if !strings.Contains(m.viewport.View(), "hello from kyle") {
		t.Fatalf("viewport does not contain new message: %q", m.viewport.View())
	}
}

func TestHandleWSEventDeduplicatesMessages(t *testing.T) {
	m := New(nil, "")
	m.agents = []bridgeclient.Agent{{Name: "kyle"}}

	msg := testMessage("msg-1", "kyle", "operator", "same message", "2026-04-23T12:00:00Z")
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	evt := bridgeclient.WSEvent{Type: "new_message", Data: data}

	m.handleWSEvent(evt)
	m.handleWSEvent(evt)

	if got := len(m.messages["kyle"]); got != 1 {
		t.Fatalf("messages for kyle = %d, want 1", got)
	}
}

func TestHandleMessagesLoadedMergesRefreshWithoutDroppingExisting(t *testing.T) {
	m := New(nil, "")
	m.agents = []bridgeclient.Agent{{Name: "kyle"}}
	m.width = 80
	m.height = 24
	m.recalcLayout()
	m.messages["kyle"] = []bridgeclient.Message{
		testMessage("msg-1", "operator", "kyle", "already delivered by ws", "2026-04-23T12:00:00Z"),
	}

	m.handleMessagesLoaded(messagesLoadedMsg{
		agent: "kyle",
		msgs: []bridgeclient.Message{
			testMessage("msg-2", "kyle", "operator", "new from poll", "2026-04-23T12:01:00Z"),
			testMessage("msg-1", "operator", "kyle", "already delivered by ws", "2026-04-23T12:00:00Z"),
		},
		prepend: false,
	})

	msgs := m.messages["kyle"]
	if got := len(msgs); got != 2 {
		t.Fatalf("messages for kyle = %d, want 2", got)
	}
	if msgs[0].ID != "msg-1" || msgs[1].ID != "msg-2" {
		t.Fatalf("messages order = [%s, %s], want [msg-1, msg-2]", msgs[0].ID, msgs[1].ID)
	}
}

func TestSwitchTabRefreshesLoadedConversation(t *testing.T) {
	m := New(nil, "")
	m.agents = []bridgeclient.Agent{{Name: "kyle"}, {Name: "mike"}}
	m.activeTab = 0
	m.loaded["mike"] = true

	_, cmd := m.switchTab(1)
	if cmd == nil {
		t.Fatal("switching to an already loaded tab returned nil command, want latest-message refresh")
	}
}

func TestOpenInboxQueueFetchesActiveAgentQueue(t *testing.T) {
	client, closeServer := testInboxClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/inbox" {
			t.Fatalf("request = %s %s, want GET /api/inbox", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("agent"); got != "mike" {
			t.Fatalf("agent query = %q, want mike", got)
		}
		_ = json.NewEncoder(w).Encode([]bridgeclient.InboxQueueItem{{ID: "item-1", FromAgent: "operator", Subject: "review me"}})
	})
	defer closeServer()

	m := New(client, "")
	m.agents = []bridgeclient.Agent{{Name: "mike"}}
	m.width = 80
	m.height = 24
	m.recalcLayout()

	next, cmd := m.Update(keyRunes('i'))
	m = next.(Model)
	if m.mode != modeInboxQueue {
		t.Fatalf("mode = %v, want inbox queue", m.mode)
	}
	if cmd == nil {
		t.Fatal("open inbox returned nil command, want queue fetch")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if got := len(m.inboxQueue); got != 1 {
		t.Fatalf("inbox queue length = %d, want 1", got)
	}
}

func TestInboxQueueActionRequiresSelectedItem(t *testing.T) {
	m := New(nil, "")
	m.mode = modeInboxQueue
	m.inboxQueueAgent = "mike"

	next, cmd := m.Update(keyRunes('a'))
	m = next.(Model)
	if cmd != nil {
		t.Fatal("archive without selection returned command, want nil")
	}
	if m.inboxPendingAction != "" {
		t.Fatalf("pending action = %q, want empty", m.inboxPendingAction)
	}
}

func TestInboxQueueArchiveRequiresSecondPressAndRefreshes(t *testing.T) {
	requests := make([]string, 0, 2)
	client, closeServer := testInboxClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/inbox/archive":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/inbox":
			_ = json.NewEncoder(w).Encode([]bridgeclient.InboxQueueItem{})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	defer closeServer()

	m := New(client, "")
	m.mode = modeInboxQueue
	m.inboxQueueAgent = "mike"
	m.inboxQueue = []bridgeclient.InboxQueueItem{{ID: "item-1", Subject: "archive me"}}

	next, cmd := m.Update(keyRunes('a'))
	m = next.(Model)
	if cmd != nil {
		t.Fatal("first archive press returned command, want confirmation only")
	}
	if m.inboxPendingAction != "archive" || m.inboxPendingItemID != "item-1" {
		t.Fatalf("pending = %q/%q, want archive/item-1", m.inboxPendingAction, m.inboxPendingItemID)
	}

	next, cmd = m.Update(keyRunes('a'))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("second archive press returned nil command, want archive action")
	}
	next, cmd = m.Update(cmd())
	m = next.(Model)
	if cmd == nil {
		t.Fatal("archive completion returned nil command, want refresh")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if got := len(m.inboxQueue); got != 0 {
		t.Fatalf("inbox queue length after refresh = %d, want 0", got)
	}
	if got := strings.Join(requests, ","); got != "POST /api/inbox/archive,GET /api/inbox" {
		t.Fatalf("requests = %s", got)
	}
}

func TestInboxQueueEscReturnsToChatWithoutLosingInput(t *testing.T) {
	m := New(nil, "")
	m.agents = []bridgeclient.Agent{{Name: "mike"}}
	m.mode = modeInboxQueue
	m.inboxQueueAgent = "mike"
	m.input.SetValue("draft text")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if cmd != nil {
		t.Fatal("esc returned command, want nil")
	}
	if m.mode != modeChat {
		t.Fatalf("mode = %v, want chat", m.mode)
	}
	if got := m.input.Value(); got != "draft text" {
		t.Fatalf("input value = %q, want draft text", got)
	}
}

func TestOpenPersonaControlFetchesContainers(t *testing.T) {
	client, closeServer := testInboxClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/personas/containers" {
			t.Fatalf("request = %s %s, want GET /api/personas/containers", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(bridgeclient.PersonaContainersResponse{
			Available: true,
			Items:     []bridgeclient.PersonaContainer{{Target: "mike", Service: "csuite-mike", Status: "running"}},
		})
	})
	defer closeServer()

	m := New(client, "")
	m.width = 80
	m.height = 24
	m.recalcLayout()

	next, cmd := m.Update(keyRunes('c'))
	m = next.(Model)
	if m.mode != modePersonaControl {
		t.Fatalf("mode = %v, want persona control", m.mode)
	}
	if cmd == nil {
		t.Fatal("open control returned nil command, want container fetch")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if got := len(m.personaContainers); got != 2 {
		t.Fatalf("container rows = %d, want mike plus all", got)
	}
	if m.personaContainers[0].Status != "running" {
		t.Fatalf("container status = %q, want running", m.personaContainers[0].Status)
	}
}

func TestPersonaControlActionRequiresSecondPressAndRefreshes(t *testing.T) {
	requests := make([]string, 0, 2)
	client, closeServer := testInboxClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/personas/control":
			var req bridgeclient.PersonaControlRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode control request: %v", err)
			}
			if req.Target != "mike" || req.Action != "stop" {
				t.Fatalf("control request = %#v, want mike stop", req)
			}
			_ = json.NewEncoder(w).Encode(bridgeclient.PersonaControlResult{Status: "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/personas/containers":
			_ = json.NewEncoder(w).Encode(bridgeclient.PersonaContainersResponse{
				Available: true,
				Items:     []bridgeclient.PersonaContainer{{Target: "mike", Service: "csuite-mike", Status: "stopped"}},
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	defer closeServer()

	m := New(client, "")
	m.mode = modePersonaControl
	m.personaControlReady = true
	m.personaContainers = []bridgeclient.PersonaContainer{{Target: "mike", Service: "csuite-mike", Status: "running"}}

	next, cmd := m.Update(keyRunes('s'))
	m = next.(Model)
	if cmd != nil {
		t.Fatal("first stop press returned command, want confirmation only")
	}
	if m.controlPendingAction != "stop" || m.controlPendingTarget != "mike" {
		t.Fatalf("pending = %q/%q, want stop/mike", m.controlPendingAction, m.controlPendingTarget)
	}

	next, cmd = m.Update(keyRunes('s'))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("second stop press returned nil command, want control action")
	}
	next, cmd = m.Update(cmd())
	m = next.(Model)
	if cmd == nil {
		t.Fatal("control completion returned nil command, want refresh")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if got := m.personaContainers[0].Status; got != "stopped" {
		t.Fatalf("container status after refresh = %q, want stopped", got)
	}
	if got := strings.Join(requests, ","); got != "POST /api/personas/control,GET /api/personas/containers" {
		t.Fatalf("requests = %s", got)
	}
}

func TestPersonaControlFailedActionSurfacesError(t *testing.T) {
	client, closeServer := testInboxClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/personas/control" {
			t.Fatalf("request = %s %s, want POST /api/personas/control", r.Method, r.URL.Path)
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	defer closeServer()

	m := New(client, "")
	m.mode = modePersonaControl
	m.personaControlReady = true
	m.personaContainers = []bridgeclient.PersonaContainer{{Target: "mike", Service: "csuite-mike", Status: "running"}}

	next, _ := m.Update(keyRunes('R'))
	m = next.(Model)
	next, cmd := m.Update(keyRunes('R'))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("confirmed recreate returned nil command, want control action")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.err == nil {
		t.Fatal("failed action left err nil, want surfaced error")
	}
	if m.controlPendingAction != "" || m.controlPendingTarget != "" {
		t.Fatalf("pending = %q/%q, want cleared", m.controlPendingAction, m.controlPendingTarget)
	}
}

func testMessage(id, from, to, body, createdAt string) bridgeclient.Message {
	return bridgeclient.Message{
		ID:        id,
		FromAgent: from,
		ToAgent:   to,
		Subject:   "chat",
		Body:      body,
		Priority:  "normal",
		Type:      "request",
		CreatedAt: createdAt,
	}
}

func keyRunes(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func testInboxClient(t *testing.T, handler http.HandlerFunc) (*bridgeclient.Client, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	return bridgeclient.New(server.URL, ""), server.Close
}
