package chat

import (
	"context"
	"encoding/json"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/godinj/drem-orchestrator/internal/bridgeclient"
)

// --- Messages (tea.Msg) ---

type agentsLoadedMsg struct{ agents []bridgeclient.Agent }
type agentsErrMsg struct{ err error }

type messagesLoadedMsg struct {
	agent   string
	msgs    []bridgeclient.Message
	prepend bool // true when loading older pages
}
type messagesErrMsg struct{ err error }

type inboxQueueLoadedMsg struct {
	agent string
	items []bridgeclient.InboxQueueItem
}
type inboxQueueErrMsg struct{ err error }
type inboxQueueActionDoneMsg struct{ agent string }
type inboxQueueActionErrMsg struct{ err error }

type personaContainersLoadedMsg struct {
	available bool
	reason    string
	items     []bridgeclient.PersonaContainer
}
type personaContainersErrMsg struct{ err error }
type personaControlDoneMsg struct{}
type personaControlErrMsg struct{ err error }

type personaModelsLoadedMsg struct{ models map[string]string }
type personaModelsErrMsg struct{ err error }
type personaModelSetMsg struct {
	agent string
	model string
}
type personaModelSetErrMsg struct{ err error }

type messageSentMsg struct{ msg bridgeclient.Message }
type messageSendErrMsg struct{ err error }

type wsConnectedMsg struct{ ws *bridgeclient.WSConn }
type wsEventMsg struct{ evt bridgeclient.WSEvent }
type wsDisconnectedMsg struct{ err error }

type tickRefreshAgentsMsg struct{}
type tickRefreshMessagesMsg struct{}

// --- Commands (tea.Cmd) ---

func fetchAgents(client *bridgeclient.Client) tea.Cmd {
	return func() tea.Msg {
		agents, err := client.GetAgents(context.Background())
		if err != nil {
			return agentsErrMsg{err}
		}
		return agentsLoadedMsg{agents}
	}
}

func fetchMessages(client *bridgeclient.Client, agent string, limit int, beforeID string, prepend bool) tea.Cmd {
	return func() tea.Msg {
		msgs, err := client.GetMessages(context.Background(), "operator", agent, limit, beforeID)
		if err != nil {
			return messagesErrMsg{err}
		}
		return messagesLoadedMsg{agent: agent, msgs: msgs, prepend: prepend}
	}
}

func sendMessage(client *bridgeclient.Client, req bridgeclient.SendRequest) tea.Cmd {
	return func() tea.Msg {
		msg, err := client.SendMessage(context.Background(), req)
		if err != nil {
			return messageSendErrMsg{err}
		}
		return messageSentMsg{*msg}
	}
}

func fetchInboxQueue(client *bridgeclient.Client, agent string, limit int) tea.Cmd {
	return func() tea.Msg {
		items, err := client.GetInboxQueue(context.Background(), agent, limit)
		if err != nil {
			return inboxQueueErrMsg{err}
		}
		return inboxQueueLoadedMsg{agent: agent, items: items}
	}
}

func archiveInboxItem(client *bridgeclient.Client, agent, id string) tea.Cmd {
	return func() tea.Msg {
		if err := client.ArchiveInboxItem(context.Background(), agent, id, "operator restart review"); err != nil {
			return inboxQueueActionErrMsg{err}
		}
		return inboxQueueActionDoneMsg{agent: agent}
	}
}

func ignoreInboxItem(client *bridgeclient.Client, agent, id string) tea.Cmd {
	return func() tea.Msg {
		if err := client.IgnoreInboxItem(context.Background(), agent, id, "operator restart review"); err != nil {
			return inboxQueueActionErrMsg{err}
		}
		return inboxQueueActionDoneMsg{agent: agent}
	}
}

func fetchPersonaContainers(client *bridgeclient.Client) tea.Cmd {
	return func() tea.Msg {
		containers, err := client.GetPersonaContainers(context.Background())
		if err != nil {
			return personaContainersErrMsg{err}
		}
		return personaContainersLoadedMsg{
			available: containers.Available,
			reason:    containers.Reason,
			items:     containers.Items,
		}
	}
}

func controlPersonaContainer(client *bridgeclient.Client, target, action string) tea.Cmd {
	return func() tea.Msg {
		_, err := client.ControlPersonaContainer(context.Background(), bridgeclient.PersonaControlRequest{
			Target: target,
			Action: action,
		})
		if err != nil {
			return personaControlErrMsg{err}
		}
		return personaControlDoneMsg{}
	}
}

func fetchPersonaModels(client *bridgeclient.Client) tea.Cmd {
	return func() tea.Msg {
		models, err := client.GetPersonaModels(context.Background())
		if err != nil {
			return personaModelsErrMsg{err}
		}
		return personaModelsLoadedMsg{models: models}
	}
}

func setPersonaModel(client *bridgeclient.Client, agent, model string) tea.Cmd {
	return func() tea.Msg {
		if err := client.SetPersonaModel(context.Background(), agent, model); err != nil {
			return personaModelSetErrMsg{err}
		}
		return personaModelSetMsg{agent: agent, model: model}
	}
}

func connectWS(wsURL string) tea.Cmd {
	return func() tea.Msg {
		ws, err := bridgeclient.DialWS(context.Background(), wsURL)
		if err != nil {
			return wsDisconnectedMsg{err}
		}
		return wsConnectedMsg{ws}
	}
}

func listenWS(ws *bridgeclient.WSConn) tea.Cmd {
	return func() tea.Msg {
		evt, err := ws.ReadEvent(context.Background())
		if err != nil {
			return wsDisconnectedMsg{err}
		}
		return wsEventMsg{evt}
	}
}

func reconnectWS(wsURL string, backoff time.Duration) tea.Cmd {
	return tea.Tick(backoff, func(time.Time) tea.Msg {
		ws, err := bridgeclient.DialWS(context.Background(), wsURL)
		if err != nil {
			return wsDisconnectedMsg{err}
		}
		return wsConnectedMsg{ws}
	})
}

func tickRefreshAgents(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg {
		return tickRefreshAgentsMsg{}
	})
}

func tickRefreshMessages(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg {
		return tickRefreshMessagesMsg{}
	})
}

// parseWSMessage extracts a Message from a "new_message" WebSocket event.
func parseWSMessage(data json.RawMessage) (bridgeclient.Message, error) {
	var msg bridgeclient.Message
	err := json.Unmarshal(data, &msg)
	return msg, err
}
