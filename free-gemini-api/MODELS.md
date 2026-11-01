# Available Models

Base URL: `http://127.0.0.1:8001`  
Catalog endpoint: `GET /v1/models`  
Total models: **14** (9 free tier, 5 subscription tier)  
Default model: `gemini-3.8-flash`

All models are served by Google Gemini. Every ID below resolves to a Gemini model token in the Gemini web backend.

## Free Tier Models

No Google account required. Requests fall back to the built-in guest worker.

| Model ID | Display Name | Capabilities |
|---|---|---|
| `gemini-3.5-flash-lite` | Google Gemini 3.5 Flash-Lite | chat, stream, vision, tools, fastest |
| `gemini-3.8-flash` | Google Gemini 3.8 Flash | chat, stream, vision, video, tools |
| `gemini-3.8-flash-thinking-high` | Google Gemini 3.8 Flash (Thinking High) | chat, stream, thinking, math, code |
| `gemini-3.8-flash-thinking-low` | Google Gemini 3.8 Flash (Thinking Low) | chat, stream, thinking, math, code |
| `gemini-3.8-flash-thinking-medium` | Google Gemini 3.8 Flash (Thinking Medium) | chat, stream, thinking, math, code |
| `gemini-4-argon` | Google Gemini 4 Argon | chat, stream, vision, tools, complex_tasks, frontier |
| `gemini-4-argon-thinking-high` | Google Gemini 4 Argon (Thinking High) | chat, stream, thinking, math, code, complex_tasks |
| `gemini-4-argon-thinking-low` | Google Gemini 4 Argon (Thinking Low) | chat, stream, thinking, math, code, complex_tasks |
| `gemini-4-argon-thinking-medium` | Google Gemini 4 Argon (Thinking Medium) | chat, stream, thinking, math, code, complex_tasks |

## Subscription Tier Models

Require an authenticated Google account with an eligible plan in the cookie pool.

| Model ID | Display Name | Capabilities |
|---|---|---|
| `gemini-3.1-pro` | Google Gemini 3.1 Pro | chat, stream, vision, tools, complex_reasoning |
| `gemini-3.1-pro-thinking-high` | Google Gemini 3.1 Pro (Thinking High) | chat, stream, thinking, math, code, complex_reasoning |
| `gemini-3.1-pro-thinking-low` | Google Gemini 3.1 Pro (Thinking Low) | chat, stream, thinking, math, code, complex_reasoning |
| `gemini-3.1-pro-thinking-medium` | Google Gemini 3.1 Pro (Thinking Medium) | chat, stream, thinking, math, code, complex_reasoning |
| `gemini-3.8-thinking` | Google Gemini 3.8 Flash Thinking | chat, stream, thinking, math, code |

## Benchmark

Prompt `Reply with exactly: OK`, single non-streaming request, wall-clock milliseconds including upstream latency. Indicative only - upstream latency varies by several seconds between runs.

| Model | Latency | Result |
|---|---|---|
| `gemini-3.5-flash-lite` | 4396 ms | OK |
| `gemini-3.8-flash` | 5023 ms | OK |
| `gemini-4-argon-thinking-low` | 5034 ms | OK |
| `gemini-4-argon` | 5462 ms | OK |

## Usage

### OpenAI-compatible endpoint

```bash
curl -s -X POST http://127.0.0.1:8001/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"Hello"}],"stream":false}'
```

**Windows / PowerShell:** passing JSON inline mangles the quotes and the server replies `{"error":"Invalid request"}`. Write the body to a file and send it with `--data-binary @file`.

```powershell
Set-Content -Path body.json -Value '{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"Hello"}],"stream":false}'
curl.exe -s -X POST http://127.0.0.1:8001/v1/chat/completions -H "Content-Type: application/json" --data-binary "@body.json"
```

### Unified /chat endpoint

Multipart form data; supports image upload.

```bash
curl -s -X POST http://127.0.0.1:8001/chat -F "prompt=Say hello" -F "model=gemini-3.8-flash"
curl -s -X POST http://127.0.0.1:8001/chat -F "prompt=Describe this" -F "model=gemini-3.8-flash" -F "images=@photo.png"
```

### Images via the OpenAI endpoint

Send a base64 data URL or a local `file://` URL:

```json
{"model":"gemini-3.8-flash","messages":[{"role":"user","content":[
  {"type":"text","text":"What is this?"},
  {"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0..."}}
]}]}
```

Local file: `"url":"file://C:/path/to/image.png"`

## Streaming

Set `"stream": true` for server-sent events.

```bash
curl -N -X POST http://127.0.0.1:8001/v1/chat/completions -H "Content-Type: application/json" --data-binary "@body.json"
```

## Notes

- Aliases resolve through the model registry (e.g. `flash`, `default` map to `gemini-3.8-flash`).
- `thinking` variants return chain-of-thought in `reasoning_content` on the response message.
- `min_tier` is enforced per request against the worker pool; free tier falls back to the guest worker.
- Unknown model IDs are rejected with HTTP 400 (`{"error":"unknown model ..."}`).
- An omitted or empty `model` field resolves to the default model.
