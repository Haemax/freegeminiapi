package api

import (
	"encoding/json"
	"strings"
)

type OpenAIToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

// OpenAIToolCall is used in non-streaming messages (Index nil, omitted) and in
// streaming deltas (Index MUST be set â€” the OpenAI spec and harnesses such as
// DeepSeek Harness / the OpenAI SDK reject or mis-parse deltas without it).
type OpenAIToolCall struct {
	Index    *int                   `json:"index,omitempty"`
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type,omitempty"`
	Function OpenAIToolCallFunction `json:"function"`
}

type OpenAIContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type OpenAIChatMessage struct {
	Role             string           `json:"role"`
	Content          *string          `json:"content"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
	Name             string           `json:"name,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
}

// UnmarshalJSON accepts content as a string, null, or an array of parts
// ({"type":"text","text":"..."}). DeepSeek Harness and the OpenAI SDK send
// part arrays for multimodal turns; the previous *string field returned 400.
func (m *OpenAIChatMessage) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role             string           `json:"role"`
		Content          json.RawMessage  `json:"content"`
		ReasoningContent *string          `json:"reasoning_content,omitempty"`
		Name             string           `json:"name,omitempty"`
		ToolCallID       string           `json:"tool_call_id,omitempty"`
		ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	m.ReasoningContent = raw.ReasoningContent
	m.Name = raw.Name
	m.ToolCallID = raw.ToolCallID
	m.ToolCalls = raw.ToolCalls
	m.Content = parseMessageContent(raw.Content)
	return nil
}

func parseMessageContent(raw json.RawMessage) *string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return &s
	}
	var parts []OpenAIContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				b.WriteString(p.Text)
			}
		}
		out := b.String()
		return &out
	}
	out := string(raw)
	return &out
}

type OpenAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type OpenAIChatCompletionRequest struct {
	Model             string               `json:"model"`
	Messages          []OpenAIChatMessage  `json:"messages"`
	Tools             []map[string]any     `json:"tools,omitempty"`
	ToolChoice        any                  `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool                `json:"parallel_tool_calls,omitempty"`
	Stream            bool                 `json:"stream"`
	StreamOptions     *OpenAIStreamOptions `json:"stream_options,omitempty"`
	User              *string              `json:"user,omitempty"`
}

type OpenAIChoice struct {
	Index        int               `json:"index"`
	Message      OpenAIChatMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type OpenAIChatCompletionResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   *OpenAIUsage   `json:"usage,omitempty"`
}

type OpenAIDelta struct {
	Role             string           `json:"role,omitempty"`
	Content          string           `json:"content,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
}

type OpenAIStreamChoice struct {
	Index        int         `json:"index"`
	Delta        OpenAIDelta `json:"delta"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

type OpenAIChatCompletionChunk struct {
	ID      string               `json:"id"`
	Object  string               `json:"object"`
	Created int64                `json:"created"`
	Model   string               `json:"model"`
	Choices []OpenAIStreamChoice `json:"choices"`
	Usage   *OpenAIUsage         `json:"usage,omitempty"`
}

// ---- image types (unchanged) ----

type ImageGenerationRequest struct {
	Prompt         string `json:"prompt"`
	Model          string `json:"model"`
	N              int    `json:"n"`
	Size           string `json:"size"`
	ResponseFormat string `json:"response_format"`
	UserID         string `json:"user_id"`
}

type ImageData struct {
	URL     string `json:"url,omitempty"`
	B64JSON string `json:"b64_json,omitempty"`
}

type ImageGenerationResponse struct {
	Created int64       `json:"created"`
	Data    []ImageData `json:"data"`
}
