// direct_tool_api.go contains the OpenAI tool-calling API wire types and the
// single-shot HTTP request helper used by the direct tool-calling agent loop.
package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// toolDefinition is the OpenAI-format tool definition sent in requests.
type toolDefinition struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

// toolFunction describes a function within a tool definition.
type toolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// toolChatRequest extends chatRequest with tool definitions.
type toolChatRequest struct {
	Model              string           `json:"model"`
	Messages           []toolChatMsg    `json:"messages"`
	MaxTokens          int              `json:"max_tokens"`
	Temperature        float64          `json:"temperature"`
	Tools              []toolDefinition `json:"tools,omitempty"`
	ChatTemplateKwargs map[string]any   `json:"chat_template_kwargs,omitempty"`
}

// toolChatMsg is a chat message that supports all roles including tool
// responses. Only the fields relevant to a given role are populated.
type toolChatMsg struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`   // present when role=assistant with tool calls
	ToolCallID string     `json:"tool_call_id,omitempty"` // present when role=tool
	Name       string     `json:"name,omitempty"`         // present when role=tool (function name)
}

// toolCall represents a tool call in the assistant's response.
type toolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function toolCallFunction `json:"function"`
}

// toolCallFunction holds the function name and JSON-encoded arguments.
//
// Arguments is stored as a string to match the OpenAI tool-call protocol
// (servers return it that way). For outbound serialization, MarshalJSON
// re-emits Arguments as a JSON object when it parses as one. This is
// required for Gemma-family chat templates: the template branches on
// `function.arguments is mapping` vs `is string`. The mapping branch
// renders native `{key:<|"|>val<|"|>}` syntax (what Gemma was trained on);
// the string branch does a literal insert producing malformed
// `{{"key":"val"}}` double-brace output. Feeding a Gemma model its own
// prior tool calls in the string form causes it to fail to recognize
// them as tool calls in conversation history, which manifests as
// edit-loops and no-progress patterns. Templates that expect a string
// (e.g. OpenAI's own schema) still receive valid JSON — either a string
// or an object containing the same data.
type toolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// UnmarshalJSON accepts `arguments` as either a JSON string (OpenAI
// spec) or a JSON object (some servers — and our own test fixtures —
// emit it that way). Both are normalized into the string field so the
// rest of the agent can treat Arguments uniformly.
func (tcf *toolCallFunction) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	tcf.Name = raw.Name
	if len(raw.Arguments) == 0 {
		tcf.Arguments = ""
		return nil
	}
	var asString string
	if err := json.Unmarshal(raw.Arguments, &asString); err == nil {
		tcf.Arguments = asString
		return nil
	}
	tcf.Arguments = string(raw.Arguments)
	return nil
}

// MarshalJSON emits Arguments as a JSON object when it parses as one,
// and as a string otherwise. See type-level doc for rationale.
func (tcf toolCallFunction) MarshalJSON() ([]byte, error) {
	if tcf.Arguments != "" {
		var argsObj map[string]any
		if err := json.Unmarshal([]byte(tcf.Arguments), &argsObj); err == nil {
			return json.Marshal(struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}{
				Name:      tcf.Name,
				Arguments: argsObj,
			})
		}
	}
	return json.Marshal(struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{
		Name:      tcf.Name,
		Arguments: tcf.Arguments,
	})
}

// toolChatResponse is the API response that includes tool call information.
type toolChatResponse struct {
	Choices []toolChatChoice `json:"choices"`
	Usage   struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// toolChatChoice is one choice in the response.
type toolChatChoice struct {
	Message      toolChatMsg `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// callToolAPI makes a single chat completions request with tool definitions.
func callToolAPI(cfg DirectToolAgentConfig, messages []toolChatMsg, tools []toolDefinition) (*toolChatResponse, error) {
	reqBody := toolChatRequest{
		Model:              cfg.Model,
		Messages:           messages,
		MaxTokens:          cfg.MaxTokens,
		Temperature:        cfg.Temperature,
		Tools:              tools,
		ChatTemplateKwargs: cfg.ChatTemplateKwargs,
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, cfg.Endpoint, bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.GQCaller != "" {
		httpReq.Header.Set("X-GQ-Caller", cfg.GQCaller)
	}
	if cfg.GQPriority != "" {
		httpReq.Header.Set("X-GQ-Priority", cfg.GQPriority)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("API call: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, truncateBody(body, 500))
	}

	var chatResp toolChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &chatResp, nil
}
