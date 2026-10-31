package gemini

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"sync"

	"github.com/google/uuid"
)

var bufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ArrangedOutput struct {
	CleanText        string     `json:"clean_text"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	IsToolCall       bool       `json:"is_tool_call"`
	FinishReason     string     `json:"finish_reason"`
}

// FormatToolsInstruction creates a clear, structured system prompt injecting tools into Gemini.
// parallel controls whether the model is allowed to emit more than one <tool_call> block.
func FormatToolsInstruction(tools []map[string]any, parallel bool) string {
	if len(tools) == 0 {
		return ""
	}

	toolsJSON, err := json.MarshalIndent(tools, "", "  ")
	if err != nil {
		bytes, _ := json.Marshal(tools)
		toolsJSON = bytes
	}

	var sb strings.Builder
	sb.WriteString("\n[AVAILABLE TOOLS]\n")
	sb.WriteString("You have access to the following tools that you can invoke:\n")
	sb.WriteString("```json\n")
	sb.Write(toolsJSON)
	sb.WriteString("\n```\n\n")
	sb.WriteString("[TOOL CALL INSTRUCTIONS]\n")
	sb.WriteString("1. To call a tool, reply with one or more `<tool_call>` blocks in EXACTLY this format:\n")
	sb.WriteString("<tool_call>\n")
	sb.WriteString(`{"name": "<tool_name>", "arguments": {<json_arguments>}}` + "\n")
	sb.WriteString("</tool_call>\n")
	if parallel {
		sb.WriteString("2. You may invoke multiple tools by emitting multiple `<tool_call>` blocks consecutively.\n")
	} else {
		sb.WriteString("2. Invoke at most ONE tool per response. Emit a single `<tool_call>` block and stop.\n")
	}
	sb.WriteString("3. `arguments` must be a JSON object that strictly follows the tool's parameter schema (types, names, required fields).\n")
	sb.WriteString("4. Every `<tool_call>` MUST be closed with `</tool_call>`. Do NOT wrap it in a code fence. Do NOT add prose, markdown, or explanation around it.\n")
	sb.WriteString("5. If no tool is needed, or after receiving tool results, reply in natural language with NO `<tool_call>` tags.\n")
	sb.WriteString("6. Do not invent tools that are not in the list.\n\n")
	return sb.String()
}

var (
	thoughtRegex  = regexp.MustCompile(`(?s)<(?:thought|thinking)>([\s\S]*?)</(?:thought|thinking)>`)
	jsonCodeBlock = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{[\\s\\S]*?\\})\\s*```")
	trailingComma = regexp.MustCompile(`,\s*([}\]])`)
)

// ArrangeGeminiOutput analyzes, cleans, parses, and structures raw Gemini output.
func ArrangeGeminiOutput(raw string) *ArrangedOutput {
	res := &ArrangedOutput{FinishReason: "stop"}
	text := raw

	// 1. Reasoning blocks
	if matches := thoughtRegex.FindAllStringSubmatch(text, -1); len(matches) > 0 {
		var thoughts []string
		for _, m := range matches {
			if len(m) > 1 {
				if t := strings.TrimSpace(m[1]); t != "" {
					thoughts = append(thoughts, t)
				}
			}
		}
		if len(thoughts) > 0 {
			res.ReasoningContent = strings.Join(thoughts, "\n\n")
		}
		text = thoughtRegex.ReplaceAllString(text, "")
	}

	text = strings.TrimSpace(text)

	// 2. Tool calls â€” manual scanner, tolerant of unclosed / concatenated tags.
	if strings.Contains(text, "<tool_call>") {
		calls, cleaned := extractToolCalls(text)
		if len(calls) > 0 {
			res.ToolCalls = calls
			res.IsToolCall = true
			res.FinishReason = "tool_calls"
			res.CleanText = strings.TrimSpace(sanitizeGeminiContent(cleaned))
			return res
		}
		// Tags present but none parsed: strip them so raw tags never leak.
		text = strings.ReplaceAll(text, "</tool_call>", "")
		text = strings.ReplaceAll(text, "<tool_call>", "")
		text = strings.TrimSpace(text)
	}

	// 3. Fallback: JSON tool call inside a code block or bare JSON.
	if strings.Contains(text, `"name"`) && (strings.Contains(text, `"arguments"`) || strings.Contains(text, `"parameters"`)) {
		if cb := jsonCodeBlock.FindAllStringSubmatch(text, -1); len(cb) > 0 {
			for _, m := range cb {
				if len(m) > 1 {
					if tc, ok := parseSingleToolCall(m[1]); ok {
						res.ToolCalls = append(res.ToolCalls, tc)
					}
				}
			}
		}
		if len(res.ToolCalls) == 0 {
			if tc, ok := parseSingleToolCall(text); ok {
				res.ToolCalls = append(res.ToolCalls, tc)
			}
		}
		if len(res.ToolCalls) > 0 {
			res.IsToolCall = true
			res.FinishReason = "tool_calls"
			res.CleanText = ""
			return res
		}
	}

	// 4. Clean up
	res.CleanText = sanitizeGeminiContent(text)
	return res
}

// extractToolCalls walks the text, pairing each <tool_call> with its nearest
// </tool_call>. An unclosed tag consumes until the next <tool_call> or EOF.
func extractToolCalls(text string) ([]ToolCall, string) {
	const openTag, closeTag = "<tool_call>", "</tool_call>"
	var calls []ToolCall
	var cleaned strings.Builder
	pos := 0
	for {
		start := strings.Index(text[pos:], openTag)
		if start < 0 {
			cleaned.WriteString(text[pos:])
			break
		}
		start += pos
		cleaned.WriteString(text[pos:start])
		bodyStart := start + len(openTag)

		end := strings.Index(text[bodyStart:], closeTag)
		var body string
		if end >= 0 {
			body = text[bodyStart : bodyStart+end]
			pos = bodyStart + end + len(closeTag)
		} else if next := strings.Index(text[bodyStart:], openTag); next >= 0 {
			body = text[bodyStart : bodyStart+next]
			pos = bodyStart + next
		} else {
			body = text[bodyStart:]
			pos = len(text)
		}
		if tc, ok := parseSingleToolCall(body); ok {
			calls = append(calls, tc)
		}
	}
	return calls, cleaned.String()
}

func parseSingleToolCall(payload string) (ToolCall, bool) {
	payload = strings.TrimSpace(payload)
	payload = strings.TrimPrefix(payload, "```json")
	payload = strings.TrimPrefix(payload, "```")
	payload = strings.TrimSuffix(payload, "```")
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return ToolCall{}, false
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		if fixed := trailingComma.ReplaceAllString(payload, "$1"); fixed != payload {
			payload = fixed
		}
		if first := firstJSONObject(payload); first != "" && first != payload {
			payload = first
		}
		obj = nil
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			return ToolCall{}, false
		}
	}

	name := ""
	for _, key := range []string{"name", "tool", "function"} {
		if v, ok := obj[key].(string); ok && v != "" {
			name = v
			break
		}
	}
	if name == "" {
		return ToolCall{}, false
	}

	argsStr := "{}"
	for _, key := range []string{"arguments", "parameters"} {
		if v, exists := obj[key]; exists {
			if s, ok := v.(string); ok {
				argsStr = s
			} else if b, err := json.Marshal(v); err == nil {
				argsStr = string(b)
			}
			break
		}
	}
	var dummy any
	if err := json.Unmarshal([]byte(argsStr), &dummy); err != nil {
		argsStr = "{}"
	}

	return ToolCall{
		ID:   "call_" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""),
		Type: "function",
		Function: ToolCallFunction{
			Name:      name,
			Arguments: argsStr,
		},
	}, true
}

// firstJSONObject returns the first balanced {...} substring, or "".
func firstJSONObject(s string) string {
	depth, start := 0, -1
	inStr, esc := false, false
	for i, r := range s {
		if inStr {
			if esc {
				esc = false
			} else if r == '\\' {
				esc = true
			} else if r == '"' {
				inStr = false
			}
			continue
		}
		switch r {
		case '"':
			inStr = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					return s[start : i+1]
				}
			}
		}
	}
	return ""
}

var (
	codeFenceWithQueryRegex = regexp.MustCompile(`(?m)^` + "```" + `([a-zA-Z0-9_-]+)\?[^\n]*$`)
	multiNewlineRegex       = regexp.MustCompile(`\n{3,}`)
)

func sanitizeGeminiContent(text string) string {
	cleaned := strings.ReplaceAll(text, "\x00", "")
	cleaned = codeFenceWithQueryRegex.ReplaceAllString(cleaned, "```$1")
	cleaned = multiNewlineRegex.ReplaceAllString(cleaned, "\n\n")
	return strings.TrimSpace(cleaned)
}
