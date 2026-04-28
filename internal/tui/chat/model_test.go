package chat

import (
	"encoding/json"
	"strings"
	"testing"

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
