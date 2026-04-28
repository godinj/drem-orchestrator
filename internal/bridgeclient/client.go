package bridgeclient

import (
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/agents", nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}

	var agents []Agent
	if err := json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		return nil, fmt.Errorf("decode agents: %w", err)
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/messages?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}

	var msgs []Message
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return nil, fmt.Errorf("decode messages: %w", err)
	}
	return msgs, nil
}

// GetInboxQueue fetches live pending inbox items for a persona.
func (c *Client) GetInboxQueue(ctx context.Context, agent string, limit int) ([]InboxQueueItem, error) {
	params := url.Values{"agent": {agent}}
	if limit > 0 {
		params.Set("limit", fmt.Sprint(limit))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/inbox?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}

	var items []InboxQueueItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("decode inbox queue: %w", err)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/personas/containers", nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}

	var containers PersonaContainersResponse
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil, fmt.Errorf("decode persona containers: %w", err)
	}
	return &containers, nil
}

// ControlPersonaContainer runs a safe allowlisted persona container action.
func (c *Client) ControlPersonaContainer(ctx context.Context, reqBody PersonaControlRequest) (*PersonaControlResult, error) {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/personas/control", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}

	var result PersonaControlResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode persona control result: %w", err)
	}
	return &result, nil
}

func (c *Client) postInboxQueueAction(ctx context.Context, path string, bodyReq InboxQueueActionRequest) error {
	body, err := json.Marshal(bodyReq)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return checkStatus(resp, http.StatusOK)
}

// SendMessage creates a new message via POST /api/messages.
func (c *Client) SendMessage(ctx context.Context, req SendRequest) (*Message, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/messages", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.setAuth(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkStatus(resp, http.StatusCreated); err != nil {
		return nil, err
	}

	var msg Message
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return nil, fmt.Errorf("decode message: %w", err)
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

func checkStatus(resp *http.Response, want int) error {
	if resp.StatusCode == want {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
}
