package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"goapi/db"
	"goapi/gemini"
	"io"
	"log"
	"mime/multipart"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

// EstimateTokens calculates an accurate token count using word and character heuristics
func EstimateTokens(text string) int {
	clean := strings.TrimSpace(text)
	if clean == "" {
		return 0
	}
	words := 0
	runes := 0
	inWord := false
	for _, r := range clean {
		runes++
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			inWord = false
		} else if !inWord {
			inWord = true
			words++
		}
	}
	wTokens := int(float64(words) * 1.3)
	cTokens := (runes + 3) / 4
	tokens := wTokens
	if cTokens > tokens {
		tokens = cTokens
	}
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

func HandleUnifiedChat(c fiber.Ctx) error {
	var prompt, userID, reqModel string
	var newChat, stream bool
	var images []gemini.ImageInput

	contentType := string(c.Request().Header.ContentType())

	if strings.Contains(contentType, "multipart/form-data") {
		prompt = c.FormValue("prompt")
		userID = c.FormValue("user_id")
		newChat = c.FormValue("new_chat") == "true"
		stream = c.FormValue("stream") == "true"
		reqModel = c.FormValue("model")

		form, err := c.MultipartForm()
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid multipart form"})
		}

		var fileHeaders []*multipart.FileHeader
		if fhs, ok := form.File["images"]; ok {
			fileHeaders = append(fileHeaders, fhs...)
		}
		if fhs, ok := form.File["image"]; ok {
			fileHeaders = append(fileHeaders, fhs...)
		}

		for _, fh := range fileHeaders {
			file, err := fh.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(file)
			file.Close()
			if err != nil {
				continue
			}

			mime := fh.Header.Get("Content-Type")
			if mime == "" {
				ext := strings.ToLower(fh.Filename)
				switch {
				case strings.HasSuffix(ext, ".png"):
					mime = "image/png"
				case strings.HasSuffix(ext, ".webp"):
					mime = "image/webp"
				case strings.HasSuffix(ext, ".gif"):
					mime = "image/gif"
				case strings.HasSuffix(ext, ".mp4"):
					mime = "video/mp4"
				default:
					mime = "image/jpeg"
				}
			}
			images = append(images, gemini.ImageInput{Data: data, Filename: fh.Filename, MimeType: mime})
		}
	} else {
		var req gemini.ChatRequest
		if err := c.Bind().JSON(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		prompt = req.Prompt
		if prompt == "" {
			prompt = req.Message
		}
		userID = req.UserID
		newChat = req.NewChat
		stream = req.Stream
		reqModel = req.Model
	}

	sessionID := userID
	if sessionID == "" {
		sessionID = c.IP()
	}

	if newChat {
		UserSessions.Delete(sessionID)
	}

	modelCfg := gemini.GetModelRegistry().Resolve(reqModel)
	model := modelCfg.ID
	requiredTier := modelCfg.MinTier
	pool := GetWorkerPool()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if stream && len(images) == 0 {
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("X-Accel-Buffering", "no")

		// fiber.Ctx is recycled once the handler returns, so read everything the stream
		// writer needs from it NOW (calling c.IP()/c.Host() inside the writer panics).
		streamIP := c.IP()
		streamHost := c.Host()

		return c.SendStreamWriter(func(w *bufio.Writer) {
			// A panic in a stream writer runs on a bare goroutine and would take down the whole server.
			defer func() {
				if r := recover(); r != nil {
					log.Printf("🔥 Recovered panic in stream writer: %v", r)
				}
			}()
			// The writer runs after the handler returns, so the handler's ctx is already cancelled.
			streamCtx, streamCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer streamCancel()
			var usedWorker *AccountWorker
			var streamResp *gemini.GeminiResponse
			startTime := time.Now()

			stopKeepAlive := make(chan struct{})
			defer close(stopKeepAlive)
			go func() {
				ticker := time.NewTicker(15 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						streamWrite(w, ": ping\n\n")
					case <-stopKeepAlive:
						return
					}
				}
			}()
			streamErr := pool.ExecuteQueuedStreamWithTier(streamCtx, sessionID, requiredTier, func(worker *AccountWorker) error {
				usedWorker = worker
				emitted := false
				sr, err := worker.Client.AskStreamModel(prompt, model, func(chunk string) {
					emitted = true
					data, _ := json.Marshal(fiber.Map{"text": chunk})
					fmt.Fprintf(w, "data: %s\n\n", data)
					w.Flush()
				})
				streamResp = sr
				if err != nil && emitted {
					err = &StreamStartedError{Err: err}
				}
				return err
			})

			if streamErr != nil {
				data, _ := json.Marshal(fiber.Map{"error": streamErr.Error()})
				fmt.Fprintf(w, "data: %s\n\n", data)
				w.Flush()
				return
			}

			if streamResp != nil && usedWorker != nil {
				if len(streamResp.Images) > 0 {
					for i, imgURL := range streamResp.Images {
						filename := fmt.Sprintf("img_%s_%d.png", streamResp.ResponseID, i)
						if streamResp.ResponseID == "" {
							filename = fmt.Sprintf("img_%d_%d.png", time.Now().Unix(), i)
						}
						if err := DownloadAndClean(usedWorker.Client, imgURL, filename, "image"); err == nil {
							streamResp.Images[i] = fmt.Sprintf("http://%s/output/%s", streamHost, filename)
						}
					}
				}

				if len(streamResp.Videos) > 0 {
					vidURL := streamResp.Videos[0]
					if vidURL != "" && len(vidURL) > 50 {
						filename := fmt.Sprintf("vid_%s_0.mp4", streamResp.ResponseID)
						if streamResp.ResponseID == "" {
							filename = fmt.Sprintf("vid_%d_0.mp4", time.Now().Unix())
						}
						if err := DownloadAndClean(usedWorker.Client, vidURL, filename, "video"); err == nil {
							streamResp.Videos[0] = fmt.Sprintf("http://%s/output/%s", streamHost, filename)
						}
					}
				}
			}

			final, _ := json.Marshal(streamResp)
			fmt.Fprintf(w, "data: %s\n\n", final)
			fmt.Fprintf(w, "data: [DONE]\n\n")
			w.Flush()

			userIP := streamIP
			elapsed := time.Since(startTime).Seconds()
			go func() {
				tier := "free"
				accID := ""
				if usedWorker != nil {
					tier = usedWorker.Tier
					accID = usedWorker.ID
				}
				pTokens := CountTokens(prompt)
				rTokens := 0
				convID := ""
				if streamResp != nil {
					rTokens = CountTokens(streamResp.Text)
					convID = streamResp.ConversationID
				}
				db.LogMessageWithTier(convID, sessionID, "user", prompt, model, pTokens, tier, accID)
				if streamResp != nil && streamResp.Text != "" {
					db.LogMessageWithTier(convID, sessionID, "assistant", streamResp.Text, model, rTokens, tier, accID)
				}
				db.LogRequest("/chat", "POST", userIP, 200, elapsed*1000)
				AutoExportAnalytics()
			}()
		})
	}

	if len(images) > 0 {
		log.Printf("📸 Image/Media request: %d items, prompt: %s (model: %s, tier: %s)", len(images), prompt, model, requiredTier)
	}

	var usedWorker *AccountWorker
	resp, err := pool.ExecuteQueuedWithTier(ctx, sessionID, requiredTier, func(w *AccountWorker) (*gemini.GeminiResponse, error) {
		usedWorker = w
		if len(images) > 0 {
			return w.Client.AskWithImagesModel(prompt, images, model)
		}
		return w.Client.AskModel(prompt, model)
	})

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			UserSessions.Delete(sessionID)
			return c.Status(504).JSON(fiber.Map{"error": "Request timed out. Session reset."})
		}
		if strings.Contains(strings.ToLower(err.Error()), "rate_limit") || strings.Contains(err.Error(), "429") || strings.Contains(strings.ToLower(err.Error()), "cooldown") {
			return c.Status(429).JSON(fiber.Map{
				"error": fiber.Map{
					"message": err.Error(),
					"type":    "rate_limit_error",
					"code":    429,
				},
			})
		}
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	if len(resp.Images) > 0 && usedWorker != nil {
		for i, imgURL := range resp.Images {
			filename := fmt.Sprintf("img_%s_%d.png", resp.ResponseID, i)
			if resp.ResponseID == "" {
				filename = fmt.Sprintf("img_%d_%d.png", time.Now().Unix(), i)
			}
			if err := DownloadAndClean(usedWorker.Client, imgURL, filename, "image"); err == nil {
				resp.Images[i] = fmt.Sprintf("http://%s/output/%s", c.Host(), filename)
			}
		}
	}

	if len(resp.Videos) > 0 && usedWorker != nil {
		vidURL := resp.Videos[0]
		if vidURL != "" && len(vidURL) > 50 {
			filename := fmt.Sprintf("vid_%s_0.mp4", resp.ResponseID)
			if resp.ResponseID == "" {
				filename = fmt.Sprintf("vid_%d_0.mp4", time.Now().Unix())
			}
			if err := DownloadAndClean(usedWorker.Client, vidURL, filename, "video"); err == nil {
				resp.Videos[0] = fmt.Sprintf("http://%s/output/%s", c.Host(), filename)
			}
		}
	}

	// Save to SQLite database asynchronously & auto-export Excel
	userIP := c.IP()
	hostStr := c.Host()
	go func() {
		tier := "free"
		accID := ""
		if usedWorker != nil {
			tier = usedWorker.Tier
			accID = usedWorker.ID
		}
		pTokens := CountTokens(prompt)
		rTokens := CountTokens(resp.Text)
		db.LogMessageWithTier(resp.ConversationID, sessionID, "user", prompt, model, pTokens, tier, accID)
		if resp.Text != "" {
			db.LogMessageWithTier(resp.ConversationID, sessionID, "assistant", resp.Text, model, rTokens, tier, accID)
		}
		for _, imgURL := range resp.Images {
			fName := filepath.Base(imgURL)
			db.LogMediaGeneration("image", prompt, fName, "./output/"+fName, fmt.Sprintf("http://%s/output/%s", hostStr, fName), "16:9", resp.ResponseID)
		}
		for _, vidURL := range resp.Videos {
			fName := filepath.Base(vidURL)
			db.LogMediaGeneration("video", prompt, fName, "./output/"+fName, fmt.Sprintf("http://%s/output/%s", hostStr, fName), "16:9", resp.ResponseID)
		}
		db.LogRequest("/chat", "POST", userIP, 200, resp.Elapsed*1000)
	}()

	return c.JSON(resp)
}

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
					log.Printf("ðŸ”¥ Recovered panic in stream writer: %v", r)
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

			stopKeepAliveOpenAI := make(chan struct{})
			defer close(stopKeepAliveOpenAI)
			go func() {
				ticker := time.NewTicker(15 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						streamWrite(w, ": ping\n\n")
					case <-stopKeepAliveOpenAI:
						return
					}
				}
			}()
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
				log.Printf("âŒ Streaming error: %v", streamErr)
				writeSSE(w, OpenAIChatCompletionChunk{
					ID: chunkID, Object: "chat.completion.chunk", Created: createdTime, Model: model,
					Choices: []OpenAIStreamChoice{{
						Index:        0,
						Delta:        OpenAIDelta{Role: "assistant", Content: "âš ï¸ Gemini API error: " + streamErr.Error()},
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