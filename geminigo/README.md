# GeminiGo 🚀

**OpenAI-compatible API proxy** cho **Gemini Web** (gemini.google.com).

Reverse-engineered từ internal protocol của Gemini Web — biến tài khoản Google/Gemini miễn phí thành API OpenAI-compatible server. Hỗ trợ streaming, tool calling, image generation, multi-account load balancing.

---

## Tính năng chính

| Tính năng | Trạng thái |
|-----------|-----------|
| ✅ OpenAI-compatible API (`/v1/models`, `/v1/chat/completions`) | Ổn định |
| ✅ Streaming SSE (Server-Sent Events) | Ổn định |
| ✅ Tool/Function calling | Ổn định |
| ✅ Image generation (Imagen 3.0) | Ổn định |
| ✅ Multi-account cookie pool + round-robin | Ổn định |
| ✅ Auto-login qua Chrome DevTools Protocol | Ổn định |
| ✅ API Key authentication (constant-time compare) | Ổn định |
| ✅ Graceful shutdown | Ổn định |
| ✅ Proxy support | Ổn định |
| ✅ Request logging | Ổn định |

---

## Models

| ID | Mode | Think | Mô tả |
|----|:----:|:-----:|-------|
| `gemini-2.0-flash` | 1 | 0 | Nhanh, đa dụng, latency thấp nhất |
| `gemini-2.0-flash-thinking` | 2 | 4 | Deep thinking, output dài (~20k ký tự) |
| `gemini-2.5-pro` | 3 | 4 | Mạnh nhất, xử lý tác vụ phức tạp |
| `gemini-auto` | 4 | 4 | Tự động chọn model theo prompt |
| `gemini-2.0-flash-thinking-lite` | 5 | 4 | Thinking nhanh hơn, context ~8k |
| `gemini-2.0-flash-lite` | 6 | 0 | Nhanh nhất, tác vụ đơn giản |
| `imagen-3.0` | 6 | 0 | Tạo ảnh (prompt-based, trả về URL) |

> **Lưu ý**: `Mode` là internal enum ID gửi lên Gemini Web, không phải model ID chính thức của Google.

---

## Cài đặt & Chạy

### Yêu cầu
- Go 1.25+
- Tài khoản Google có quyền truy cập [gemini.google.com](https://gemini.google.com)

### 1. Build
```bash
cd geminigo
go build -o geminigo.exe .
```

### 2. Chạy
```bash
./geminigo
# hoặc với port tùy chỉnh
./geminigo --port 3000
```

Server sẽ listen tại `http://localhost:8081` (mặc định).

### 3. Thiết lập Cookie

#### Cách A — Tự động (khuyên dùng)
Mở `http://localhost:8081` trong trình duyệt → click **"Tự động kết nối Trình duyệt"** → đăng nhập Google trong tab hiện ra → cookie tự động được lưu, trình duyệt tự đóng.

#### Cách B — Thủ công
1. Vào `gemini.google.com`, đăng nhập
2. Mở DevTools (F12) → tab Application/Storage → Cookies
3. Copy toàn bộ cookie string
4. Vào `http://localhost:8081`, dán vào ô "Cụm Cookie", nhấn "Cập nhật"

---

## API Reference

### `GET /v1/models`

Danh sách models có sẵn.

```bash
curl http://localhost:8081/v1/models
```

Response:
```json
{
  "object": "list",
  "data": [
    { "id": "gemini-2.0-flash", "object": "model", "owned_by": "google", "description": "Fast general-purpose model, lowest latency" },
    { "id": "gemini-2.0-flash-thinking", "object": "model", "owned_by": "google", "description": "Deep thinking mode, longer output (~20k chars)" },
    ...
  ]
}
```

### `POST /v1/chat/completions`

Chat completions (non-streaming).

```bash
curl http://localhost:8081/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-api-key" \
  -d '{
    "model": "gemini-2.0-flash",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Tell me about Hanoi"}
    ]
  }'
```

#### Streaming
```bash
curl -N http://localhost:8081/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-2.0-flash",
    "stream": true,
    "messages": [
      {"role": "user", "content": "Count to 5"}
    ]
  }'
```

Response chunks (SSE):
```
data: {"choices":[{"delta":{"content":"1"},"finish_reason":null,"index":0}],...}
data: {"choices":[{"delta":{"content":" 2"},"finish_reason":null,"index":0}],...}
data: {"choices":[{"delta":{"content":" 3"},"finish_reason":null,"index":0}],...}
data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],...}
data: [DONE]
```

#### Tool / Function Calling

```bash
curl http://localhost:8081/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-2.0-flash",
    "messages": [
      {"role": "user", "content": "What is the weather in Hanoi?"}
    ],
    "tools": [
      {
        "type": "function",
        "function": {
          "name": "get_weather",
          "description": "Get current weather for a city",
          "parameters": {
            "type": "object",
            "properties": {
              "city": {"type": "string", "description": "City name"}
            },
            "required": ["city"]
          }
        }
      }
    ],
    "tool_choice": "auto"
  }'
```

Response:
```json
{
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "",
      "tool_calls": [
        {
          "id": "call_0",
          "type": "function",
          "function": {
            "name": "get_weather",
            "arguments": "{\"city\":\"Hanoi\"}"
          }
        }
      ]
    },
    "finish_reason": "tool_calls"
  }]
}
```

Streaming tool calls (SSE):
```
data: {"choices":[{"delta":{"tool_calls":[{"id":"call_0","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Hanoi\"}"}}]},"finish_reason":null,...}],...}
data: {"choices":[{"delta":{},"finish_reason":"tool_calls",...}],...}
data: [DONE]
```

> **Cách hoạt động**: Gemini Web không có native function calling. Tool calls được mô phỏng qua prompt engineering — model được instruction để output `\`\`\`tool_call\n{...}\n\`\`\`` blocks, sau đó proxy parse và chuyển thành OpenAI format.

#### Image Generation (Imagen 3.0)

```bash
curl http://localhost:8081/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "imagen-3.0",
    "messages": [
      {"role": "user", "content": "Draw a red cat sitting on a mat"}
    ]
  }'
```

Response (ảnh tự động detect từ response text và append dưới dạng markdown):

```json
{
  "choices": [{
    "message": {
      "content": "Here is your image...\n\n![image](https://googleusercontent.com/...)",
      "role": "assistant"
    }
  }]
}
```

> **Lưu ý**: Imagen cần tài khoản Google có quyền truy cập Gemini Advanced hoặc đã active image generation trên Gemini Web.

---

## Authentication (API Keys)

Mặc định, API không yêu cầu auth. Để bật:

```json
// config.json
{
  "api_keys": ["sk-abc123", "sk-xyz789"]
}
```

Khi đã set `api_keys`, client phải gửi header:
```
Authorization: Bearer sk-abc123
```
hoặc
```
x-api-key: sk-abc123
```

So sánh key dùng `crypto/subtle.ConstantTimeCompare` chống timing attack.

---

## Configuration

### CLI Flags

| Flag | Default | Mô tả |
|------|---------|-------|
| `--port` | `8081` | Port listen |
| `--config` | `""` | Đường dẫn file config JSON |
| `--cookie-file` | `"data/cookies.json"` | Đường dẫn file lưu cookie |

### Config File (`config.json`)

```json
{
  "port": 8081,
  "host": "0.0.0.0",
  "retry_attempts": 3,
  "retry_delay_sec": 2,
  "request_timeout_sec": 180,
  "gemini_bl": "boq_assistant-bard-web-server_20260525.09_p0",
  "auth_user": "",
  "xsrf_token": "",
  "default_model": "gemini-2.0-flash",
  "log_requests": true,
  "cookie_file": "data/cookies.json",
  "proxy": "",
  "api_keys": []
}
```

| Field | Mô tả |
|-------|-------|
| `port` | Port server |
| `host` | Bind address |
| `retry_attempts` | *(reserved)* Số lần retry upstream |
| `retry_delay_sec` | *(reserved)* Delay giữa các lần retry |
| `request_timeout_sec` | Timeout HTTP client (giây) |
| `gemini_bl` | Build label Gemini Web (ảnh hưởng model availability) |
| `auth_user` | Google AuthUser ID (`/u/{id}`) |
| `xsrf_token` | XSRF token (nếu cần) |
| `default_model` | Model mặc định khi client không gửi |
| `log_requests` | Bật/tắt request logging |
| `cookie_file` | Đường dẫn file cookie |
| `proxy` | Proxy URL (VD: `http://127.0.0.1:8080`) |
| `api_keys` | Danh sách API keys cho auth |

---

## Dashboard

Mở `http://localhost:8081` trong trình duyệt:

- **Status**: trạng thái cookie (đã cấu hình / chưa)
- **Stats**: tổng request, token usage, request cuối
- **Auto-login**: kích hoạt tự động tìm cookie
- **Manual cookie**: paste cookie thủ công

---

## Cấu trúc mã nguồn

```
D:\API_gemini\
├── .gitignore
└── geminigo\
    ├── main.go          # Entry point, graceful shutdown
    ├── config.go        # Config struct, MODELS map, ModelCfg
    ├── server.go        # HTTP handlers (models, chat completions, streaming)
    ├── gemini.go        # Cookie pool, buildPayload, auth headers
    ├── parser.go        # Response parsing, HTTP client
    ├── tools.go         # Tool call injection & parsing
    ├── browser.go       # Auto-login via Chrome DevTools Protocol
    ├── multimodal.go    # Image upload, Imagen support
    ├── stats.go         # Request/token statistics
    ├── go.mod           # Go module
    └── README.md        # Tài liệu (file này)
```

---

## Lưu ý kỹ thuật

### Gemini Web Protocol

- Sử dụng `SAPISIDHASH` authentication, không phải API key Google
- Endpoint: `/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate`
- Payload là sparse array 102 phần tử với magic indices (reverse-engineered từ web client)
- Build label (`bl` param) xác định server-side handler version

### Hạn chế

- **Tool calling**: là prompt engineering, không phải native — model có thể không tuân theo format
- **GeminiBL**: hardcoded, cần cập nhật thủ công khi Google rotate build label
- **Token counting**: `len(text)/4` ước lượng, không chính xác như tokenizer thật
- **Rate limiting**: phụ thuộc vào rate limit của Gemini Web, không có client-side rate limiter

---

## License

MIT
