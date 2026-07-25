// direct_tool_api.go contains the OpenAI tool-calling API wire types and the
// single-shot HTTP request helper used by the direct tool-calling agent loop.
package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
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
	TopP               float64          `json:"top_p,omitempty"`
	TopK               int              `json:"top_k,omitempty"`
	Seed               *int64           `json:"seed,omitempty"`
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
// edit-loops and no-progress patterns. OpenAI-compatible servers such as
// vLLM require a string, so callToolAPI selects the representation through
// DirectToolAgentConfig.ToolArgumentsFormat.
type toolCallFunction struct {
	Name              string `json:"name"`
	Arguments         string `json:"arguments"`
	argumentsAsObject bool
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
	if tcf.argumentsAsObject && tcf.Arguments != "" {
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
	wireMessages := make([]toolChatMsg, len(messages))
	copy(wireMessages, messages)
	if cfg.ToolArgumentsFormat == ToolArgumentsObject {
		for i := range wireMessages {
			wireMessages[i].ToolCalls = append([]toolCall(nil), messages[i].ToolCalls...)
			for j := range wireMessages[i].ToolCalls {
				wireMessages[i].ToolCalls[j].Function.argumentsAsObject = true
			}
		}
	}
	maxTokens := boundedRequestMaxTokens(cfg, wireMessages, tools)
	if maxTokens <= 0 {
		return nil, fmt.Errorf("request has no completion headroom inside context_limit=%d", cfg.ContextLimit)
	}
	reqBody := toolChatRequest{
		Model:              cfg.Model,
		Messages:           wireMessages,
		MaxTokens:          maxTokens,
		Temperature:        cfg.Temperature,
		TopP:               cfg.TopP,
		TopK:               cfg.TopK,
		Seed:               cfg.Seed,
		Tools:              tools,
		ChatTemplateKwargs: cfg.ChatTemplateKwargs,
	}

	client := &http.Client{Timeout: cfg.Timeout}
	const maxContextHeadroomAttempts = 5
	for requestAttempt := 0; requestAttempt < maxContextHeadroomAttempts; requestAttempt++ {
		reqBody.MaxTokens = maxTokens
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

		resp, err := client.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("API call: %w", err)
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read response: %w", readErr)
		}
		if resp.StatusCode == http.StatusOK {
			var chatResp toolChatResponse
			if err := json.Unmarshal(body, &chatResp); err != nil {
				return nil, fmt.Errorf("parse response: %w", err)
			}
			return &chatResp, nil
		}
		if requestAttempt+1 < maxContextHeadroomAttempts && resp.StatusCode == http.StatusBadRequest {
			if reportedInput, ok := reportedContextInputTokens(body); ok {
				// The provider reports "at least" input tokens as
				// context_limit-max_tokens+1, so solving from that number merely
				// produces another request exactly one token over. Geometric
				// backoff converges without inference because these 400 responses
				// happen before generation begins.
				retryMax := maxTokens / 2
				if retryMax > 0 && retryMax < maxTokens {
					slog.Warn("direct tool agent: retrying pre-inference context rejection with provider-reported headroom",
						"reported_input_tokens", reportedInput, "old_max_tokens", maxTokens, "new_max_tokens", retryMax)
					maxTokens = retryMax
					continue
				}
			}
		}
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, truncateBody(body, 500))
	}
	return nil, fmt.Errorf("API context-headroom retries exhausted")
}

var contextInputTokensPattern = regexp.MustCompile(`prompt contains at least ([0-9]+) input tokens`)

func reportedContextInputTokens(body []byte) (int, bool) {
	match := contextInputTokensPattern.FindSubmatch(body)
	if len(match) != 2 {
		return 0, false
	}
	value, err := strconv.Atoi(string(match[1]))
	return value, err == nil && value > 0
}

// boundedRequestMaxTokens reserves room inside the model's live context
// window for each tool-loop request. Cumulative input budgets may legitimately
// exceed the live window across turns; this guard prevents replay plus a fixed
// completion allowance from being rejected before inference begins.
func boundedRequestMaxTokens(cfg DirectToolAgentConfig, messages []toolChatMsg, tools []toolDefinition) int {
	// Tiny limits are used by context-monitor unit tests as percentages, not as
	// real served-model windows. Production chat models used by this harness
	// have at least a 4K live window.
	if cfg.ContextLimit < 4096 || cfg.MaxTokens <= 0 {
		return cfg.MaxTokens
	}
	payload, err := json.Marshal(struct {
		Messages []toolChatMsg    `json:"messages"`
		Tools    []toolDefinition `json:"tools"`
	}{Messages: messages, Tools: tools})
	if err != nil {
		return cfg.MaxTokens
	}
	// The active Qwen tokenizer averages roughly four serialized bytes per
	// token for mixed C++ and tool JSON. The 4K safety reserve covers tokenizer
	// variance and server-side chat-template tokens. A 1K reserve was one token
	// too small in a live Canvas turn (25,903 actual input + 6,866 requested
	// output against a 32,768-token window), so this is deliberately generous.
	const bytesPerEstimatedToken = 4
	const contextSafetyTokens = 4096
	estimatedInput := (len(payload) + bytesPerEstimatedToken - 1) / bytesPerEstimatedToken
	available := cfg.ContextLimit - estimatedInput - contextSafetyTokens
	if available <= 0 {
		return 0
	}
	if available < cfg.MaxTokens {
		return available
	}
	return cfg.MaxTokens
}
