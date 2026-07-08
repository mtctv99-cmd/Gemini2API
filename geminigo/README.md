# GeminiGo

OpenAI-compatible API proxy for **Gemini Web** (gemini.google.com).
Reverse-engineered internal protocol — provides standard OpenAI /v1 endpoints backed by Gemini Web sessions.

## Features

- OpenAI-compatible API: `/v1/models`, `/v1/chat/completions` (streaming + non-streaming)
- Multi-account cookie pool with round-robin
- Auto-login via Chrome DevTools Protocol
- Image generation (Imagen 3.0 via prompt-based auto-detect)
- Tool/function calling (prompt-engineered on Gemini Web)
- API key authentication with constant-time comparison
- Graceful shutdown

## Models

| ID | Description |
|---|---|
| `gemini-2.0-flash` | Fast general-purpose, lowest latency |
| `gemini-2.0-flash-thinking` | Deep thinking, ~20k chars |
| `gemini-2.5-pro` | Most capable, complex tasks |
| `gemini-auto` | Auto-selects based on prompt |
| `gemini-2.0-flash-thinking-lite` | Faster thinking, ~8k context |
| `gemini-2.0-flash-lite` | Fastest, simple tasks |
| `imagen-3.0` | Image generation (prompt-based) |

## Quick Start

1. Set up cookie (auto-login or paste from browser)
2. `go build .`
3. `./geminigo`
4. API at `http://localhost:8081/v1`

## Cookie Setup

**Auto-login:** Open `http://localhost:8081` → click "Auto-login" → sign in to Google in the opened browser → cookie auto-saved.

**Manual:** Open `http://localhost:8081` → paste full cookie string from browser dev tools.

## API Usage

```bash
curl http://localhost:8081/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini-2.0-flash","messages":[{"role":"user","content":"hello"}]}'
```

Streaming:
```bash
curl -N http://localhost:8081/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini-2.0-flash","stream":true,"messages":[{"role":"user","content":"hello"}]}'
```

Tool calling:
```bash
curl http://localhost:8081/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini-2.0-flash","messages":[{"role":"user","content":"weather in Hanoi?"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"...","parameters":{...}}}],"tool_choice":"auto"}'
```

## Config

Edit `config.json` or pass via flags:
- `--port` (default 8081)
- `--config` path to JSON config
- `--cookie-file` path to cookie storage
