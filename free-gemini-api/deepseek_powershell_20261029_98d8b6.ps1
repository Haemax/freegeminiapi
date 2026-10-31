#Requires -Version 5.1
<#
    Free-Gemini-API — Windows (PowerShell) equivalent of apply.sh.
    DeepSeek-Harness refactor + Firefox extension port.

    Run from the repo root (the directory containing go.mod):
        powershell -ExecutionPolicy Bypass -File apply.ps1
#>
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

if (-not (Test-Path 'go.mod')) {
    Write-Host 'error: run this from the free-gemini-api repo root (go.mod not found).' -ForegroundColor Red
    exit 1
}

# ---------- helpers ----------
$Utf8NoBom = New-Object System.Text.UTF8Encoding($false)

function Get-FileEol([string]$Path) {
    if (Test-Path $Path) {
        $c = [IO.File]::ReadAllText($Path)
        if ($c.Contains("`r`n")) { return "`r`n" }
    }
    return "`n"
}

function Normalize-Text([string]$Text, [string]$Eol) {
    $t = $Text -replace "`r`n", "`n"
    return ($t -replace "`n", $Eol)
}

function Write-Utf8NoBom {
    param([string]$Path, [string]$Content, [string]$Eol = $null)
    $dir = Split-Path -Parent $Path
    if ($dir -and -not (Test-Path $dir)) {
        New-Item -ItemType Directory -Force -Path $dir | Out-Null
    }
    if ($Eol) { $Content = Normalize-Text -Text $Content -Eol $Eol }
    [IO.File]::WriteAllText($Path, $Content, $Utf8NoBom)
}

function Replace-Literal {
    param(
        [string]$Path,
        [string]$Anchor,
        [string]$Replacement,
        [string]$Label = ''
    )
    $content = [IO.File]::ReadAllText($Path)
    $eol = Get-FileEol $Path
    $anchorN = Normalize-Text -Text $Anchor -Eol $eol
    $replN   = Normalize-Text -Text $Replacement -Eol $eol
    if (-not $content.Contains($anchorN)) {
        $snippet = $anchorN
        if ($snippet.Length -gt 160) { $snippet = $snippet.Substring(0,160) + '...' }
        throw "Anchor not found in $Path ($Label):`n$snippet"
    }
    $content = $content.Replace($anchorN, $replN)
    [IO.File]::WriteAllText($Path, $content, $Utf8NoBom)
}

# ---------- backup ----------
Write-Host '==> Backing up originals to .bak\'
New-Item -ItemType Directory -Force -Path '.bak' | Out-Null
foreach ($f in @('api/types.go','api/chat_handler.go','api/queue.go',
                 'gemini/arranger.go','gemini/discovery.go','gemini/monitor.go')) {
    if (Test-Path $f) {
        $dst = '.bak\' + ($f -replace '[\\/]', '_')
        Copy-Item $f $dst -Force
    }
}

# ============================================================
# 1. api/types.go — full replacement
# ============================================================
$typesGo = @'
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
// streaming deltas (Index MUST be set — the OpenAI spec and harnesses such as
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
'@
Write-Utf8NoBom 'api/types.go' $typesGo -Eol (Get-FileEol 'api/types.go')
Write-Host '  OK  api/types.go'

# ============================================================
# 2. gemini/arranger.go — full replacement
# ============================================================
$arrangerGo = @'
package gemini

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

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

	// 2. Tool calls — manual scanner, tolerant of unclosed / concatenated tags.
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
'@
Write-Utf8NoBom 'gemini/arranger.go' $arrangerGo -Eol (Get-FileEol 'gemini/arranger.go')
Write-Host '  OK  gemini/arranger.go'

# ============================================================
# 3. api/chat_handler.go — keep everything above HandleOpenAIChatCompletions, replace the rest
# ============================================================
$chatPath = 'api/chat_handler.go'
$chatContent = [IO.File]::ReadAllText($chatPath)
if ($chatContent.Contains('func writeSSE')) {
    Write-Host '  ..  api/chat_handler.go already patched, skipping'
} else {
    $chatEol = Get-FileEol $chatPath
    $lines = $chatContent -split "`r?`n"
    $cutoff = -1
    for ($i = 0; $i -lt $lines.Length; $i++) {
        if ($lines[$i] -like '// HandleOpenAIChatCompletions handles*') { $cutoff = $i; break }
    }
    if ($cutoff -lt 0) { throw 'Could not locate HandleOpenAIChatCompletions in api/chat_handler.go' }
    $prefix = ($lines[0..($cutoff-1)] -join $chatEol) + $chatEol

    $newTail = @'
// HandleOpenAIChatCompletions handles /v1/chat/completions with Native Tool Calling & Output Arranger
func HandleOpenAIChatCompletions(c fiber.Ctx) error {
	var req OpenAIChatCompletionRequest
	if err := c.Bind().JSON(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
	}
	if len(req.Messages) == 0 {
		return c.Status(400).JSON(fiber.Map{"error": "Messages array is empty"})
	}

	modelCfg := gemini.GetModelRegistry().Resolve(req.Model)
	model := modelCfg.ID
	requiredTier := modelCfg.MinTier

	parallel := true
	if req.ParallelToolCalls != nil {
		parallel = *req.ParallelToolCalls
	}
	toolsActive := len(req.Tools) > 0 && !toolChoiceIsNone(req.ToolChoice)

	var prompt string
	hasToolResults := false
	var fullPromptBuilder strings.Builder

	for _, m := range req.Messages {
		contentStr := ""
		if m.Content != nil {
			contentStr = *m.Content
		}
		switch m.Role {
		case "user":
			prompt = contentStr
			fullPromptBuilder.WriteString("User: " + contentStr + "\n")
		case "assistant":
			if len(m.ToolCalls) > 0 {
				tcJSON, _ := json.Marshal(m.ToolCalls)
				fullPromptBuilder.WriteString("Assistant called tools: " + string(tcJSON) + "\n")
			} else if contentStr != "" {
				fullPromptBuilder.WriteString("Assistant: " + contentStr + "\n")
			}
		case "tool":
			hasToolResults = true
			fullPromptBuilder.WriteString(fmt.Sprintf("[Tool Result for %s]: %s\n", m.Name, contentStr))
		case "system":
			fullPromptBuilder.WriteString("[System]: " + contentStr + "\n")
		}
	}
	if prompt == "" && len(req.Messages) > 0 {
		if last := req.Messages[len(req.Messages)-1]; last.Content != nil {
			prompt = *last.Content
		}
	}
	if toolsActive {
		fullPromptBuilder.WriteString(gemini.FormatToolsInstruction(req.Tools, parallel))
	}

	sessionID := strings.TrimSpace(c.Get("X-Agent-ID"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(c.Get("X-Session-ID"))
	}
	if sessionID == "" {
		sessionID = strings.TrimSpace(c.Get("X-Conversation-ID"))
	}
	if sessionID == "" && req.User != nil {
		sessionID = strings.TrimSpace(*req.User)
	}
	if sessionID == "" {
		sessionID = strings.TrimSpace(c.Query("session_id"))
	}
	if sessionID == "" {
		sessionID = c.IP()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	askPrompt := prompt
	if len(req.Messages) > 1 || hasToolResults || toolsActive {
		if hasToolResults {
			fullPromptBuilder.WriteString("Assistant: ")
		}
		askPrompt = fullPromptBuilder.String()
	}

	pool := GetWorkerPool()

	if req.Stream {
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("X-Accel-Buffering", "no")

		streamIP := c.IP()
		includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage

		return c.SendStreamWriter(func(w *bufio.Writer) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("🔥 Recovered panic in stream writer: %v", r)
				}
			}()
			streamCtx, streamCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer streamCancel()

			chunkID := "chatcmpl-" + uuid.NewString()[:12]
			createdTime := time.Now().Unix()
			var usedWorker *AccountWorker
			var streamResp *gemini.GeminiResponse
			var accumulatedOutput strings.Builder
			roleSent := false

			streamErr := pool.ExecuteQueuedStreamWithTier(streamCtx, sessionID, requiredTier, func(wWorker *AccountWorker) error {
				usedWorker = wWorker
				accumulatedOutput.Reset()
				emitted := false
				sr, err := wWorker.Client.AskStreamModel(askPrompt, model, func(chunk string) {
					if toolsActive {
						accumulatedOutput.WriteString(chunk)
						return
					}
					delta := OpenAIDelta{Content: chunk}
					if !roleSent {
						delta.Role = "assistant"
						roleSent = true
					}
					emitted = true
					writeSSE(w, OpenAIChatCompletionChunk{
						ID: chunkID, Object: "chat.completion.chunk", Created: createdTime, Model: model,
						Choices: []OpenAIStreamChoice{{Index: 0, Delta: delta}},
					})
				})
				streamResp = sr
				if err != nil && emitted {
					err = &StreamStartedError{Err: err}
				}
				return err
			})

			if streamErr != nil {
				log.Printf("❌ Streaming error: %v", streamErr)
				writeSSE(w, OpenAIChatCompletionChunk{
					ID: chunkID, Object: "chat.completion.chunk", Created: createdTime, Model: model,
					Choices: []OpenAIStreamChoice{{
						Index:        0,
						Delta:        OpenAIDelta{Role: "assistant", Content: "⚠️ Gemini API error: " + streamErr.Error()},
						FinishReason: "stop",
					}},
				})
				streamWrite(w, "data: [DONE]\n\n")
				return
			}

			finishReason := "stop"
			var finalContentForLog string

			if toolsActive {
				rawOutput := accumulatedOutput.String()
				if streamResp != nil && streamResp.Text != "" && len(rawOutput) < len(streamResp.Text) {
					rawOutput = streamResp.Text
				}
				arranged := gemini.ArrangeGeminiOutput(rawOutput)
				finishReason = arranged.FinishReason
				finalContentForLog = arranged.CleanText

				if arranged.IsToolCall {
					tcs := arranged.ToolCalls
					if !parallel && len(tcs) > 1 {
						tcs = tcs[:1]
					}
					for i := range tcs {
						tc := tcs[i]
						idx := i
						// Delta 1: id/type/name, empty arguments. index REQUIRED.
						writeSSE(w, OpenAIChatCompletionChunk{
							ID: chunkID, Object: "chat.completion.chunk", Created: createdTime, Model: model,
							Choices: []OpenAIStreamChoice{{
								Index: 0,
								Delta: OpenAIDelta{
									Role: "assistant",
									ToolCalls: []OpenAIToolCall{{
										Index: &idx,
										ID:    tc.ID,
										Type:  tc.Type,
										Function: OpenAIToolCallFunction{
											Name:      tc.Function.Name,
											Arguments: "",
										},
									}},
								},
							}},
						})
						// Delta 2: arguments fragment. Same index; name/id/type omitted.
						writeSSE(w, OpenAIChatCompletionChunk{
							ID: chunkID, Object: "chat.completion.chunk", Created: createdTime, Model: model,
							Choices: []OpenAIStreamChoice{{
								Index: 0,
								Delta: OpenAIDelta{
									ToolCalls: []OpenAIToolCall{{
										Index: &idx,
										Function: OpenAIToolCallFunction{
											Arguments: tc.Function.Arguments,
										},
									}},
								},
							}},
						})
					}
				} else if arranged.CleanText != "" {
					writeSSE(w, OpenAIChatCompletionChunk{
						ID: chunkID, Object: "chat.completion.chunk", Created: createdTime, Model: model,
						Choices: []OpenAIStreamChoice{{
							Index: 0,
							Delta: OpenAIDelta{
								Role:             "assistant",
								Content:          arranged.CleanText,
								ReasoningContent: arranged.ReasoningContent,
							},
						}},
					})
				}
			} else if streamResp != nil {
				finalContentForLog = streamResp.Text
			}

			// Finish chunk: empty delta, finish_reason only.
			writeSSE(w, OpenAIChatCompletionChunk{
				ID: chunkID, Object: "chat.completion.chunk", Created: createdTime, Model: model,
				Choices: []OpenAIStreamChoice{{Index: 0, FinishReason: finishReason}},
			})

			pTokens := CountTokens(prompt)
			rTokens := CountTokens(finalContentForLog)

			if includeUsage {
				writeSSE(w, OpenAIChatCompletionChunk{
					ID: chunkID, Object: "chat.completion.chunk", Created: createdTime, Model: model,
					Choices: []OpenAIStreamChoice{},
					Usage: &OpenAIUsage{
						PromptTokens:     pTokens,
						CompletionTokens: rTokens,
						TotalTokens:      pTokens + rTokens,
					},
				})
			}
			streamWrite(w, "data: [DONE]\n\n")

			go func() {
				tier, accID := "free", ""
				if usedWorker != nil {
					tier, accID = usedWorker.Tier, usedWorker.ID
				}
				convID := ""
				if streamResp != nil {
					convID = streamResp.ConversationID
				}
				db.LogMessageWithTier(convID, sessionID, "user", prompt, model, pTokens, tier, accID)
				if finalContentForLog != "" {
					db.LogMessageWithTier(convID, sessionID, "assistant", finalContentForLog, model, rTokens, tier, accID)
				}
				db.LogRequest("/v1/chat/completions", "POST", streamIP, 200, float64(time.Now().Unix()-createdTime)*1000)
				AutoExportAnalytics()
			}()
		})
	}

	// ---- non-streaming ----
	var usedWorker *AccountWorker
	resp, err := pool.ExecuteQueuedWithTier(ctx, sessionID, requiredTier, func(w *AccountWorker) (*gemini.GeminiResponse, error) {
		usedWorker = w
		return w.Client.AskModel(askPrompt, model)
	})
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return c.Status(504).JSON(fiber.Map{"error": "Request timed out in worker queue."})
		}
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "rate_limit") || strings.Contains(err.Error(), "429") || strings.Contains(low, "cooldown") {
			return c.Status(429).JSON(fiber.Map{"error": fiber.Map{
				"message": err.Error(), "type": "rate_limit_error", "code": 429,
			}})
		}
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	arranged := gemini.ArrangeGeminiOutput(resp.Text)
	pTokens := CountTokens(prompt)
	cTokens := CountTokens(arranged.CleanText)

	var message OpenAIChatMessage
	if arranged.IsToolCall {
		tcs := arranged.ToolCalls
		if !parallel && len(tcs) > 1 {
			tcs = tcs[:1]
		}
		var toolCalls []OpenAIToolCall
		for _, tc := range tcs {
			// Non-streaming: index omitted (nil) per OpenAI spec.
			toolCalls = append(toolCalls, OpenAIToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: OpenAIToolCallFunction{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
		message = OpenAIChatMessage{Role: "assistant", Content: nil, ToolCalls: toolCalls}
	} else {
		clean := arranged.CleanText
		message = OpenAIChatMessage{Role: "assistant", Content: &clean}
	}
	if arranged.ReasoningContent != "" {
		rc := arranged.ReasoningContent
		message.ReasoningContent = &rc
	}

	openAIResp := OpenAIChatCompletionResponse{
		ID: "chatcmpl-" + uuid.NewString()[:12], Object: "chat.completion",
		Created: time.Now().Unix(), Model: model,
		Choices: []OpenAIChoice{{Index: 0, Message: message, FinishReason: arranged.FinishReason}},
		Usage:   &OpenAIUsage{PromptTokens: pTokens, CompletionTokens: cTokens, TotalTokens: pTokens + cTokens},
	}

	userIP := c.IP()
	go func() {
		tier, accID := "free", ""
		if usedWorker != nil {
			tier, accID = usedWorker.Tier, usedWorker.ID
		}
		db.LogMessageWithTier(resp.ConversationID, sessionID, "user", prompt, model, pTokens, tier, accID)
		if arranged.CleanText != "" {
			db.LogMessageWithTier(resp.ConversationID, sessionID, "assistant", arranged.CleanText, model, cTokens, tier, accID)
		}
		db.LogRequest("/v1/chat/completions", "POST", userIP, 200, resp.Elapsed*1000)
	}()

	return c.JSON(openAIResp)
}

func toolChoiceIsNone(tc any) bool {
	if tc == nil {
		return false
	}
	if s, ok := tc.(string); ok {
		return strings.EqualFold(strings.TrimSpace(s), "none")
	}
	return false
}

// writeSSE marshals v and writes one SSE frame. Returns false on write failure.
func writeSSE(w *bufio.Writer, v any) bool {
	data, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return streamWrite(w, fmt.Sprintf("data: %s\n\n", data))
}

// streamWrite writes one SSE frame and flushes. Returns false on client disconnect.
func streamWrite(w *bufio.Writer, frame string) bool {
	if _, err := fmt.Fprint(w, frame); err != nil {
		return false
	}
	return w.Flush() == nil
}
'@
    $newTailN = Normalize-Text -Text $newTail -Eol $chatEol
    [IO.File]::WriteAllText($chatPath, ($prefix + $newTailN), $Utf8NoBom)
    Write-Host '  OK  api/chat_handler.go'
}

# ============================================================
# 4. api/queue.go — two string edits
# ============================================================
$queue = 'api/queue.go'

# 4a: insert refreshWorkerTiers before // BindAffinity
$qAnchor1 = '// BindAffinity binds a conversation ID'
$qInsert1 = @'
var tierRefreshMu sync.Mutex

// refreshWorkerTiers initialises logged-in workers whose session was invalidated
// (ReloadSession leaves Client un-initialised) and copies the freshly detected
// tier onto the worker. Reports whether any worker is now "subscription".
func (p *WorkerPool) refreshWorkerTiers() bool {
	tierRefreshMu.Lock()
	defer tierRefreshMu.Unlock()

	p.mu.RLock()
	workers := append([]*AccountWorker(nil), p.workers...)
	p.mu.RUnlock()

	found := false
	for _, w := range workers {
		if w.ID == "guest" || w.Client == nil {
			continue
		}
		if !w.Client.IsInitialized {
			if err := w.Client.InitSession(); err != nil {
				log.Printf("⚠️ [WorkerPool] Tier re-detection: init failed for [%s]: %v", w.ID, err)
				continue
			}
		}
		t := w.Client.AccountTier
		w.mu.Lock()
		if t != "" && t != w.Tier {
			log.Printf("🔄 [WorkerPool] Worker [%s] tier %s -> %s", w.ID, w.Tier, t)
			w.Tier = t
		}
		if w.Tier == "subscription" {
			found = true
		}
		w.mu.Unlock()
	}
	return found
}

// BindAffinity binds a conversation ID
'@
Replace-Literal -Path $queue -Anchor $qAnchor1 -Replacement $qInsert1 -Label 'refreshWorkerTiers'

# 4b: insert refresh check before returning the subscription error
$qAnchor2 = "`t`t`tif !hasSubWorker {`n`t`t`t`tp.mu.RUnlock()`n`t`t`t`treturn nil, fmt.Errorf(`"requested model requires"
$qRepl2 = @'
			if !hasSubWorker {
				p.mu.RUnlock()
				// worker.Tier can be stale: ReloadSession() leaves the client
				// un-initialised and the tier is only re-detected by InitSession —
				// which never ran because this check rejected the request first.
				if p.refreshWorkerTiers() {
					continue
				}
				return nil, fmt.Errorf("requested model requires
'@
Replace-Literal -Path $queue -Anchor $qAnchor2 -Replacement $qRepl2 -Label 'subscription-recheck'
Write-Host '  OK  api/queue.go'

# ============================================================
# 5. gemini/discovery.go — three edits
# ============================================================
$disc = 'gemini/discovery.go'

# 5a: add "os" import
Replace-Literal -Path $disc `
    -Anchor "`t`"log`"`n`t`"path/filepath`"`n`t`"regexp`"" `
    -Replacement "`t`"log`"`n`t`"os`"`n`t`"path/filepath`"`n`t`"regexp`"" `
    -Label 'os import'

# 5b: insert forcedTierFor before DetectAccountTier comment
$dAnchor2 = '// DetectAccountTier scans HTML payload'
$dInsert2 = @'
// forcedTierFor lets the operator pin an account's tier because the HTML
// heuristics cannot reliably recognise a Pro account.
//
//	GEMINI_ACCOUNT_TIER=subscription|free   force every logged-in account
//	GEMINI_PRO_ACCOUNTS=ga000EAlFK,...      mark cookie files containing an id as Pro
func forcedTierFor(cookieFileName string) string {
	base := strings.ToLower(filepath.Base(cookieFileName))
	if base == "guest" || base == "." || base == "" {
		return ""
	}
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("GEMINI_ACCOUNT_TIER"))); v == "subscription" || v == "free" {
		return v
	}
	for _, id := range strings.Split(os.Getenv("GEMINI_PRO_ACCOUNTS"), ",") {
		id = strings.ToLower(strings.TrimSpace(id))
		if id != "" && strings.Contains(base, id) {
			return "subscription"
		}
	}
	return ""
}

// DetectAccountTier scans HTML payload
'@
Replace-Literal -Path $disc -Anchor $dAnchor2 -Replacement $dInsert2 -Label 'forcedTierFor'

# 5c: early return in DetectAccountTier
Replace-Literal -Path $disc `
    -Anchor "func DetectAccountTier(htmlContent string, cookieFileName string) string {`n`tlowerHTML := strings.ToLower(htmlContent)" `
    -Replacement "func DetectAccountTier(htmlContent string, cookieFileName string) string {`n`tif forced := forcedTierFor(cookieFileName); forced != `"`" {`n`t`treturn forced`n`t}`n`tlowerHTML := strings.ToLower(htmlContent)" `
    -Label 'early-return'
Write-Host '  OK  gemini/discovery.go'

# ============================================================
# 6. gemini/monitor.go — two edits
# ============================================================
$mon = 'gemini/monitor.go'

# 6a: insert volatileCookies var + filter inside loop
$mAnchor1 = "func cookieSetHash(cookies []CookieObject) string {`n`tpairs := make([]string, 0, len(cookies))`n`tfor _, c := range cookies {`n`t`tpairs = append(pairs, c.Name+`"=`"+c.Value)"
$mRepl1 = @'
// volatileCookies are rewritten by Google on almost every request (SIDCC
// family, NID, COMPASS). They carry no login identity: a change in only these
// must NOT count as "new cookies", otherwise any open Google tab makes the
// extension re-push every few seconds, and each push rewrites the file, wipes
// the session cache and reloads the whole worker pool.
var volatileCookies = map[string]bool{
	"SIDCC":            true,
	"__Secure-1PSIDCC": true,
	"__Secure-3PSIDCC": true,
	"NID":              true,
	"COMPASS":          true,
}

func cookieSetHash(cookies []CookieObject) string {
	pairs := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if volatileCookies[c.Name] {
			continue
		}
		pairs = append(pairs, c.Name+"="+c.Value)
'@
Replace-Literal -Path $mon -Anchor $mAnchor1 -Replacement $mRepl1 -Label 'volatile-filter'
Write-Host '  OK  gemini/monitor.go'

# ============================================================
# 7. Firefox extension
# ============================================================
New-Item -ItemType Directory -Force -Path 'firefox-extension' | Out-Null

$manifestJson = @'
{
  "manifest_version": 3,
  "name": "Gemini Cookie Sync (Firefox)",
  "version": "1.1.0",
  "description": "Pushes Google/Gemini cookies to the local free-gemini-api server. Ignores volatile cookies to avoid feedback loops.",
  "browser_specific_settings": {
    "gecko": {
      "id": "gemini-cookie-sync@local.freegeminiapi",
      "strict_min_version": "115.0"
    }
  },
  "permissions": ["cookies", "storage", "alarms"],
  "host_permissions": [
    "*://*.google.com/*",
    "*://*.googleusercontent.com/*",
    "http://127.0.0.1:8001/*",
    "http://localhost:8001/*"
  ],
  "background": { "scripts": ["background.js"] },
  "action": {
    "default_popup": "popup.html",
    "default_title": "Gemini Cookie Sync"
  }
}
'@
Write-Utf8NoBom 'firefox-extension/manifest.json' $manifestJson -Eol "`n"

$backgroundJs = @'
/* Firefox MV3 event page. Uses the promise-based `browser` API when present
 * and falls back to callback-based `chrome` (so it also runs in Chrome/Edge). */

const api = (typeof browser !== 'undefined') ? browser : chrome;
const hasBrowser = (typeof browser !== 'undefined');

const DEFAULT_ENDPOINT = 'http://127.0.0.1:8001/api/cookies';

const ALLOWED_COOKIE_NAMES = new Set([
  'SID', 'HSID', 'SSID', 'APISID', 'SAPISID',
  '__Secure-1PSID', '__Secure-3PSID',
  '__Secure-1PAPISID', '__Secure-3PAPISID',
  '__Secure-1PSIDTS', '__Secure-3PSIDTS',
  '__Secure-OSID', 'OSID', 'LSID',
  'NID', 'AEC', 'CONSENT', 'SOCS', 'SEARCH_SAMESITE',
  '__Host-GAPS', 'GAPS',
  'SIDCC', '__Secure-1PSIDCC', '__Secure-3PSIDCC', 'COMPASS'
]);

// Rotate on nearly every Google request; they must NOT trigger a sync on their own.
const VOLATILE_COOKIE_NAMES = new Set(['SIDCC', '__Secure-1PSIDCC', '__Secure-3PSIDCC', 'NID', 'COMPASS']);
const MIN_AUTO_SYNC_MS = 30000;

const TARGET_DOMAIN_SUFFIXES = [
  '.google.com', 'google.com', 'gemini.google.com',
  '.googleusercontent.com', 'googleusercontent.com'
];

let syncDebounceTimeout = null;

/* ---- promise wrappers ---- */
function storageGet(keys) {
  if (hasBrowser) return browser.storage.local.get(keys);
  return new Promise((res) => chrome.storage.local.get(keys, res));
}
function storageSet(obj) {
  if (hasBrowser) return browser.storage.local.set(obj);
  return new Promise((res) => chrome.storage.local.set(obj, res));
}
function getAllCookies(details) {
  if (hasBrowser) return browser.cookies.getAll(details);
  return new Promise((res, rej) => {
    chrome.cookies.getAll(details, (cookies) => {
      const e = chrome.runtime.lastError;
      if (e) rej(new Error(e.message)); else res(cookies);
    });
  });
}

function setBadge(text, color) {
  const target = api.action || api.browserAction;
  if (!target) return;
  try {
    target.setBadgeText({ text });
    target.setBadgeBackgroundColor({ color });
  } catch (_) { /* ignore */ }
}

/* ---- cookie collection ---- */
function inTargetDomain(domain) {
  return TARGET_DOMAIN_SUFFIXES.some((s) => domain === s || domain.endsWith(s));
}

async function getFormattedCookies() {
  const all = await getAllCookies({});
  const seen = new Map();
  for (const c of all) {
    if (!c || !inTargetDomain(c.domain || '')) continue;
    if (!ALLOWED_COOKIE_NAMES.has(c.name)) continue;
    seen.set(`${c.name}|${c.domain}|${c.path}`, {
      name: c.name,
      value: c.value,
      domain: c.domain,
      path: c.path,
      secure: c.secure,
      httpOnly: c.httpOnly,
      hostOnly: c.hostOnly,
      sameSite: c.sameSite,
      session: c.session,
      expirationDate: c.expirationDate
    });
  }
  return [...seen.values()];
}

/* ---- sync ---- */
async function performSync(reason) {
  try {
    const { endpoint } = await storageGet(['endpoint']);
    const url = (endpoint && endpoint.trim()) || DEFAULT_ENDPOINT;
    const cookies = await getFormattedCookies();
    if (!cookies.length) {
      setBadge('!', '#f59e0b');
      await storageSet({ lastSync: Date.now(), lastSyncStatus: 'no cookies found' });
      return { ok: false, error: 'no cookies found' };
    }

    // The server binds []CookieObject, so POST a bare array.
    const resp = await fetch(url, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Sync-Reason': reason || 'auto',
        'X-Sync-Timestamp': String(Date.now())
      },
      body: JSON.stringify(cookies)
    });
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);

    setBadge('✓', '#16a34a');
    await storageSet({ lastSync: Date.now(), lastSyncStatus: `ok (${cookies.length} cookies)` });
    return { ok: true };
  } catch (e) {
    console.error('[Gemini Cookie Sync] sync failed:', e);
    setBadge('✗', '#dc2626');
    await storageSet({ lastSync: Date.now(), lastSyncStatus: 'error: ' + e.message });
    return { ok: false, error: e.message };
  }
}

async function scheduleAutoSync(reason) {
  const now = Date.now();
  const { lastAutoSync = 0 } = await storageGet(['lastAutoSync']);
  const wait = Math.max(1500, lastAutoSync + MIN_AUTO_SYNC_MS - now);
  if (syncDebounceTimeout) clearTimeout(syncDebounceTimeout);
  syncDebounceTimeout = setTimeout(async () => {
    await storageSet({ lastAutoSync: Date.now() });
    performSync(reason);
  }, wait);
}

/* ---- event wiring ---- */
api.cookies.onChanged.addListener((info) => {
  if (info.removed) return;
  const c = info.cookie;
  if (!c) return;
  if (!inTargetDomain(c.domain || '')) return;
  if (!ALLOWED_COOKIE_NAMES.has(c.name)) return;
  if (VOLATILE_COOKIE_NAMES.has(c.name)) return; // rotation must not cause a loop
  scheduleAutoSync('cookie-change');
});

api.alarms.create('periodic-sync', { periodInMinutes: 5 });
api.alarms.onAlarm.addListener((a) => {
  if (a.name === 'periodic-sync') performSync('periodic');
});

api.runtime.onInstalled.addListener(() => {
  setTimeout(() => performSync('installed'), 2000);
});
api.runtime.onStartup.addListener(() => {
  setTimeout(() => performSync('startup'), 2000);
});

api.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg && msg.type === 'sync-now') {
    performSync('manual').then(sendResponse);
    return true; // keep channel open for async response
  }
  if (msg && msg.type === 'get-status') {
    storageGet(['lastSync', 'lastSyncStatus']).then(sendResponse);
    return true;
  }
  return false;
});
'@
Write-Utf8NoBom 'firefox-extension/background.js' $backgroundJs -Eol "`n"

$popupHtml = @'
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <style>
    body { font: 13px/1.4 system-ui, sans-serif; width: 340px; padding: 12px; margin: 0; }
    h3 { margin: 0 0 8px; font-size: 14px; }
    label { display: block; margin: 8px 0 4px; color: #444; }
    input { width: 100%; box-sizing: border-box; padding: 6px; font: inherit; }
    .row { display: flex; gap: 8px; margin-top: 10px; }
    button { flex: 1; padding: 6px 10px; font: inherit; cursor: pointer; }
    #status { margin-top: 10px; color: #555; word-break: break-word; }
  </style>
</head>
<body>
  <h3>Gemini Cookie Sync</h3>
  <label for="endpoint">Server endpoint</label>
  <input id="endpoint" type="url" placeholder="http://127.0.0.1:8001/api/cookies">
  <div class="row">
    <button id="save">Save</button>
    <button id="sync">Sync now</button>
  </div>
  <p id="status">Loading…</p>
  <script src="popup.js"></script>
</body>
</html>
'@
Write-Utf8NoBom 'firefox-extension/popup.html' $popupHtml -Eol "`n"

$popupJs = @'
const api = (typeof browser !== 'undefined') ? browser : chrome;
const hasBrowser = (typeof browser !== 'undefined');
const DEFAULT_ENDPOINT = 'http://127.0.0.1:8001/api/cookies';

const $ = (id) => document.getElementById(id);

function storageGet(keys) {
  if (hasBrowser) return browser.storage.local.get(keys);
  return new Promise((res) => chrome.storage.local.get(keys, res));
}
function storageSet(obj) {
  if (hasBrowser) return browser.storage.local.set(obj);
  return new Promise((res) => chrome.storage.local.set(obj, res));
}
function sendMessage(msg) {
  if (hasBrowser) return browser.runtime.sendMessage(msg);
  return new Promise((res, rej) => {
    chrome.runtime.sendMessage(msg, (r) => {
      const e = chrome.runtime.lastError;
      if (e) rej(new Error(e.message)); else res(r);
    });
  });
}

async function refreshStatus() {
  const s = await storageGet(['lastSync', 'lastSyncStatus']);
  if (!s.lastSync) { $('status').textContent = 'No sync yet.'; return; }
  $('status').textContent =
    `Last sync: ${new Date(s.lastSync).toLocaleString()} — ${s.lastSyncStatus || 'unknown'}`;
}

async function load() {
  const cfg = await storageGet(['endpoint']);
  $('endpoint').value = cfg.endpoint || DEFAULT_ENDPOINT;
  refreshStatus();
}

$('save').addEventListener('click', async () => {
  await storageSet({ endpoint: $('endpoint').value.trim() });
  $('status').textContent = 'Endpoint saved.';
});

$('sync').addEventListener('click', async () => {
  $('status').textContent = 'Syncing…';
  try {
    const resp = await sendMessage({ type: 'sync-now' });
    if (resp && resp.ok) {
      await refreshStatus();
    } else {
      $('status').textContent = 'Sync failed: ' + ((resp && resp.error) || 'unknown');
    }
  } catch (e) {
    $('status').textContent = 'Sync failed: ' + e.message;
  }
});

load();
'@
Write-Utf8NoBom 'firefox-extension/popup.js' $popupJs -Eol "`n"
Write-Host '  OK  firefox-extension\{manifest.json,background.js,popup.html,popup.js}'

# ============================================================
# 8. gofmt
# ============================================================
if (Get-Command gofmt -ErrorAction SilentlyContinue) {
    Write-Host '==> gofmt'
    & gofmt -w api/types.go api/chat_handler.go gemini/arranger.go api/queue.go gemini/discovery.go gemini/monitor.go
}

Write-Host ''
Write-Host 'Done. Originals backed up in .bak\ (remove with: Remove-Item -Recurse -Force .bak)' -ForegroundColor Green
Write-Host ''
Write-Host 'Next steps:'
Write-Host '  go build ./...'
Write-Host '  go test ./gemini/...'
Write-Host ''
Write-Host 'Firefox: about:debugging#/runtime/this-firefox -> Load Temporary Add-on...'
Write-Host '         -> select firefox-extension\manifest.json'