package bridgeclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Client communicates with the drem-bridge REST API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// New creates a Client targeting the given base URL with bearer-token auth.
// The baseURL should include scheme and host (e.g. "http://localhost:8080").
func New(baseURL, token string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: &http.Client{},
	}
}

// GetAgents returns the agent dashboard from GET /api/agents.
func (c *Client) GetAgents(ctx context.Context) ([]Agent, error) {
	var agents []Agent
	if err := c.getJSON(ctx, "/api/agents", nil, &agents, "agents"); err != nil {
		return nil, err
	}
	return agents, nil
}

// GetMessages fetches messages between the operator and an agent.
// Messages are returned newest-first by the server. Pass an empty beforeID
// for the first page, or the ID of the oldest message to paginate backwards.
func (c *Client) GetMessages(ctx context.Context, from, to string, limit int, beforeID string) ([]Message, error) {
	params := url.Values{
		"from":  {from},
		"to":    {to},
		"limit": {fmt.Sprint(limit)},
	}
	if beforeID != "" {
		params.Set("before_id", beforeID)
	}

	var msgs []Message
	if err := c.getJSON(ctx, "/api/messages", params, &msgs, "messages"); err != nil {
		return nil, err
	}
	return msgs, nil
}

// GetInboxQueue fetches live pending inbox items for a persona.
func (c *Client) GetInboxQueue(ctx context.Context, agent string, limit int) ([]InboxQueueItem, error) {
	params := url.Values{"agent": {agent}}
	if limit > 0 {
		params.Set("limit", fmt.Sprint(limit))
	}

	var items []InboxQueueItem
	if err := c.getJSON(ctx, "/api/inbox", params, &items, "inbox queue"); err != nil {
		return nil, err
	}
	return items, nil
}

// ArchiveInboxItem moves a live persona inbox item to inbox/.archive/.
func (c *Client) ArchiveInboxItem(ctx context.Context, agent, id, reason string) error {
	return c.postInboxQueueAction(ctx, "/api/inbox/archive", InboxQueueActionRequest{
		Agent:  agent,
		ID:     id,
		Reason: reason,
	})
}

// IgnoreInboxItem moves a live persona inbox item to inbox/.ignored/.
func (c *Client) IgnoreInboxItem(ctx context.Context, agent, id, reason string) error {
	return c.postInboxQueueAction(ctx, "/api/inbox/ignore", InboxQueueActionRequest{
		Agent:  agent,
		ID:     id,
		Reason: reason,
	})
}

// GetPersonaContainers returns the allowlisted persona container control state.
func (c *Client) GetPersonaContainers(ctx context.Context) (*PersonaContainersResponse, error) {
	var containers PersonaContainersResponse
	if err := c.getJSON(ctx, "/api/personas/containers", nil, &containers, "persona containers"); err != nil {
		return nil, err
	}
	return &containers, nil
}

// ControlPersonaContainer runs a safe allowlisted persona container action.
func (c *Client) ControlPersonaContainer(ctx context.Context, reqBody PersonaControlRequest) (*PersonaControlResult, error) {
	var result PersonaControlResult
	if err := c.doJSON(ctx, http.MethodPost, "/api/personas/control", nil, reqBody, http.StatusOK, &result, "persona control result"); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetPersonaModels returns the current model per persona from GET /api/personas/models.
func (c *Client) GetPersonaModels(ctx context.Context) (map[string]string, error) {
	var models map[string]string
	if err := c.getJSON(ctx, "/api/personas/models", nil, &models, "persona models"); err != nil {
		return nil, err
	}
	return models, nil
}

// SetPersonaModel updates one persona's model via PUT /api/personas/model.
func (c *Client) SetPersonaModel(ctx context.Context, target, model string) error {
	return c.doJSON(ctx, http.MethodPut, "/api/personas/model", nil, SetPersonaModelRequest{Target: target, Model: model}, http.StatusOK, nil, "")
}

func (c *Client) postInboxQueueAction(ctx context.Context, path string, bodyReq InboxQueueActionRequest) error {
	return c.doJSON(ctx, http.MethodPost, path, nil, bodyReq, http.StatusOK, nil, "")
}

// SendMessage creates a new message via POST /api/messages.
func (c *Client) SendMessage(ctx context.Context, req SendRequest) (*Message, error) {
	var msg Message
	if err := c.doJSON(ctx, http.MethodPost, "/api/messages", nil, req, http.StatusCreated, &msg, "message"); err != nil {
		return nil, err
	}
	return &msg, nil
}

// WSURL returns the WebSocket URL for this client, including the token
// as a query parameter (matching the server's auth model).
func (c *Client) WSURL() string {
	u := strings.Replace(c.baseURL, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	return u + "/api/ws?token=" + url.QueryEscape(c.token)
}

func (c *Client) setAuth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, into any, decodeName string) error {
	return c.doJSON(ctx, http.MethodGet, path, query, nil, http.StatusOK, into, decodeName)
}

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, bodyReq any, wantStatus int, into any, decodeName string) error {
	var body io.Reader
	if bodyReq != nil {
		data, err := json.Marshal(bodyReq)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := c.newRequest(ctx, method, path, query, body)
	if err != nil {
		return err
	}
	if bodyReq != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := checkStatus(resp, wantStatus); err != nil {
		return err
	}
	if into == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return fmt.Errorf("decode %s: %w", decodeName, err)
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values, body io.Reader) (*http.Request, error) {
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	return req, nil
}

func checkStatus(resp *http.Response, want int) error {
	if resp.StatusCode == want {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
}
