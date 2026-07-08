package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

func authorized(r *http.Request) bool {
	keys := CONFIG.ApiKeys
	if len(keys) == 0 {
		return true
	}
	auth := r.Header.Get("Authorization")
	key := ""
	if strings.HasPrefix(auth, "Bearer ") {
		key = strings.TrimPrefix(auth, "Bearer ")
	} else {
		key = r.Header.Get("x-api-key")
	}
	if key == "" {
		return false
	}
	keyBytes := []byte(key)
	for _, k := range keys {
		if len(k) == len(key) && subtle.ConstantTimeCompare([]byte(k), keyBytes) == 1 {
			return true
		}
	}
	return false
}

func logRequest(r *http.Request) {
	if !CONFIG.LogRequests {
		return
	}
	log.Printf("[%s] %s %s from %s\n", time.Now().Format("15:04:05"), r.Method, r.URL.Path, r.RemoteAddr)
}

var corsHeaders = map[string]string{
	"Access-Control-Allow-Origin":  "*",
	"Access-Control-Allow-Methods": "GET, POST, OPTIONS",
	"Access-Control-Allow-Headers": "*",
}

func setCORS(w http.ResponseWriter) {
	for k, v := range corsHeaders {
		w.Header().Set(k, v)
	}
}

func sendJSON(w http.ResponseWriter, data interface{}, status int) {
	setCORS(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func handleOptions(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	w.WriteHeader(http.StatusNoContent)
}

func handleModels(w http.ResponseWriter, r *http.Request) {
	logRequest(r)
	if !authorized(r) {
		sendJSON(w, map[string]interface{}{"error": map[string]string{"message": "invalid api key"}}, http.StatusUnauthorized)
		return
	}
	var data []map[string]interface{}
	for name, cfg := range MODELS {
		data = append(data, map[string]interface{}{
			"id":          name,
			"object":      "model",
			"created":     1700000000,
			"owned_by":    "google",
			"description": cfg.Desc,
		})
	}
	sendJSON(w, map[string]interface{}{"object": "list", "data": data}, http.StatusOK)
}

type ChatCompletionRequest struct {
	Model      string      `json:"model"`
	Messages   []Message   `json:"messages"`
	Tools      []Tool      `json:"tools"`
	ToolChoice interface{} `json:"tool_choice"`
	Stream     bool        `json:"stream"`
}

func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	logRequest(r)
	if r.Method == http.MethodOptions {
		handleOptions(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !authorized(r) {
		sendJSON(w, map[string]interface{}{"error": map[string]string{"message": "invalid api key"}}, http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		sendJSON(w, map[string]interface{}{"error": map[string]string{"message": "failed to read body"}}, http.StatusBadRequest)
		return
	}

	var req ChatCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		sendJSON(w, map[string]interface{}{"error": map[string]string{"message": "invalid JSON"}}, http.StatusBadRequest)
		return
	}

	modelName := req.Model
	if modelName == "" {
		modelName = CONFIG.DefaultModel
	}
	cfg, ok := MODELS[modelName]
	if !ok {
		sendJSON(w, map[string]interface{}{"error": map[string]string{"message": "unknown model"}}, http.StatusBadRequest)
		return
	}

	prompt, rawImages := messagesToPrompt(req.Messages, req.Tools, req.ToolChoice)
	var fileRefs []uploadedFile
	for i, img := range rawImages {
		name := fmt.Sprintf("input_%d%s", i+1, extensionForMimeType(img.Mime))
		ref, err := uploadImage(img.Data, name, img.Mime)
		if err == nil {
			fileRefs = append(fileRefs, ref)
		}
	}

	ctx := r.Context() // propagate cancellation from client disconnect

	if req.Stream {
		handleChatStream(ctx, w, prompt, modelName, cfg.Mode, cfg.Think, fileRefs, len(req.Tools) > 0, cfg.ImageGen)
		return
	}

	rawText, err := generateText(ctx, prompt, cfg.Mode, cfg.Think, fileRefs, cfg.ImageGen)
	if err != nil {
		sendJSON(w, map[string]interface{}{"error": map[string]string{"message": "upstream error: " + err.Error()}}, http.StatusBadGateway)
		return
	}

	thinking, text := extractThinkingAndText(rawText)
	images := collectImages(text)
	if len(images) > 0 {
		var imgMarkdowns []string
		for _, imgURL := range images {
			imgMarkdowns = append(imgMarkdowns, fmt.Sprintf("![image](%s)", imgURL))
		}
		if text != "" {
			text += "\n\n"
		}
		text += strings.Join(imgMarkdowns, "\n")
	}

	cleanMsgContent, toolCalls := parseToolCalls(text)

	totalTokens := int64((len(prompt) + len(text)) / 4)
	updateStats(totalTokens)

	msg := map[string]interface{}{
		"role":    "assistant",
		"content": cleanMsgContent,
	}
	if thinking != "" {
		msg["reasoning_content"] = thinking
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
		finishReason = "tool_calls"
	}

	sendJSON(w, map[string]interface{}{
		"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelName,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       msg,
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     len(prompt) / 4,
			"completion_tokens": len(text) / 4,
			"total_tokens":      (len(prompt) + len(text)) / 4,
		},
	}, http.StatusOK)
}

func handleChatStream(ctx context.Context, w http.ResponseWriter, prompt, modelName string, mode, think int, fileRefs []uploadedFile, hasTools bool, imageGen bool) {
	client := getHTTPClient()
	body := buildPayload(prompt, mode, think, fileRefs, imageGen)
	req, err := http.NewRequestWithContext(ctx, "POST", getStreamURL(), strings.NewReader(body))
	if err != nil {
		sendJSON(w, map[string]interface{}{"error": map[string]string{"message": err.Error()}}, http.StatusInternalServerError)
		return
	}
	req.Header = buildHeaders()

	resp, err := client.Do(req)
	if err != nil {
		sendJSON(w, map[string]interface{}{"error": map[string]string{"message": err.Error()}}, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	setCORS(w)
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Println("Streaming not supported by response writer")
		return
	}

	reader := bufio.NewReader(resp.Body)
	cid := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	prevText := ""
	var totalBytesRead int64
	done := ctx.Done()

loop:
	for {
		lineCh := make(chan string, 1)
		errCh := make(chan error, 1)
		go func() {
			l, e := reader.ReadString('\n')
			if e != nil {
				errCh <- e
				return
			}
			lineCh <- l
		}()

		select {
		case <-done:
			// client disconnected, stop upstream
			return
		case line := <-lineCh:
			totalBytesRead += int64(len(line))
			for _, t := range extractTextsFromLine(line) {
				if len(t) > len(prevText) {
					rawDelta := t[len(prevText):]
					prevText = t
					thinking, text := extractThinkingAndText(rawDelta)
					if thinking != "" {
						chunk := map[string]interface{}{
							"id":      cid,
							"object":  "chat.completion.chunk",
							"created": time.Now().Unix(),
							"model":   modelName,
							"choices": []map[string]interface{}{
								{
									"index": 0,
									"delta": map[string]string{
										"reasoning_content": thinking,
									},
									"finish_reason": nil,
								},
							},
						}
						chunkJSON, _ := json.Marshal(chunk)
						_, _ = fmt.Fprintf(w, "data: %s\n\n", chunkJSON)
						flusher.Flush()
					}

					if text != "" {
						cleanDelta := cleanText(text)
						// Strip tool_call blocks from content delta so streaming
						// clients don't see raw tool call text.
						cleanDelta = stripToolCallBlocks(cleanDelta)
						cleanDelta = strings.TrimSpace(cleanDelta)
						if cleanDelta != "" {
							chunk := map[string]interface{}{
								"id":      cid,
								"object":  "chat.completion.chunk",
								"created": time.Now().Unix(),
								"model":   modelName,
								"choices": []map[string]interface{}{
									{
										"index": 0,
										"delta": map[string]string{
											"content": cleanDelta,
										},
										"finish_reason": nil,
									},
								},
							}
							chunkJSON, _ := json.Marshal(chunk)
							_, _ = fmt.Fprintf(w, "data: %s\n\n", chunkJSON)
							flusher.Flush()
						}
					}
				}
			}
		case err := <-errCh:
			if err == io.EOF {
				break loop
			}
			return
		}
	}

	updateStats((int64(len(prompt)) + totalBytesRead/4) / 4)

	finishReason := "stop"
	if hasTools && prevText != "" {
		_, toolCalls := parseToolCalls(prevText)
		if len(toolCalls) > 0 {
			// Emit delta.tool_calls chunk first (OpenAI streaming spec)
			tcChunk := map[string]interface{}{
				"id":      cid,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   modelName,
				"choices": []map[string]interface{}{
					{
						"index": 0,
						"delta": map[string]interface{}{
							"tool_calls": toolCalls,
						},
						"finish_reason": nil,
					},
				},
			}
			tcJSON, _ := json.Marshal(tcChunk)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", tcJSON)
			flusher.Flush()
			finishReason = "tool_calls"
		}
	}

	endChunk := map[string]interface{}{
		"id":      cid,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   modelName,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"delta":         map[string]string{},
				"finish_reason": finishReason,
			},
		},
	}
	endJSON, _ := json.Marshal(endChunk)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", endJSON)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	pool.mu.RLock()
	totalAccounts := len(pool.Accounts)
	pool.mu.RUnlock()

	cookieStatus := "Chưa cấu hình"
	cookieClass := "status-badge error"
	if totalAccounts > 0 {
		cookieStatus = fmt.Sprintf("Đã cấu hình %d tài khoản", totalAccounts)
		cookieClass = "status-badge ok"
	}

	STATS.mu.RLock()
	totalReqs := STATS.TotalRequests
	totalTokens := STATS.TotalTokens
	lastReqAt := STATS.LastRequestAt
	if lastReqAt == "" {
		lastReqAt = "—"
	}
	// Format last request time relative
	lastReqDisplay := lastReqAt
	if parsed, err := time.Parse("2006-01-02 15:04:05", lastReqAt); err == nil {
		diff := time.Since(parsed)
		if diff < time.Minute {
			lastReqDisplay = "Vài giây trước"
		} else if diff < time.Hour {
			lastReqDisplay = fmt.Sprintf("%d phút trước", int(diff.Minutes()))
		} else {
			lastReqDisplay = fmt.Sprintf("%d giờ trước", int(diff.Hours()))
		}
	}
	STATS.mu.RUnlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="vi">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GeminiGo</title>
<style>
*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
html{font-size:16px}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Oxygen,Ubuntu,sans-serif;background:#0a0a0b;color:#e4e4e7;min-height:100dvh;display:flex;align-items:center;justify-content:center;padding:16px}
.container{width:100%%;max-width:720px;background:#141416;border-radius:20px;border:1px solid #222225;padding:32px;box-shadow:0 25px 50px -12px rgba(0,0,0,.5)}
h1{font-size:20px;font-weight:600;letter-spacing:-0.01em;display:flex;align-items:center;gap:10px;padding-bottom:20px;border-bottom:1px solid #222225;margin-bottom:24px}
h1 span{color:#a1a1aa;font-weight:400;font-size:12px;background:#1a1a1d;padding:2px 10px;border-radius:999px;border:1px solid #27272a}
.grid{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-bottom:24px}
.card{background:#1a1a1d;border-radius:12px;border:1px solid #27272a;padding:16px;transition:border-color .15s}
.card:hover{border-color:#3f3f46}
.card-label{font-size:11px;font-weight:500;text-transform:uppercase;letter-spacing:.08em;color:#71717a;margin-bottom:6px}
.card-value{font-size:22px;font-weight:600;letter-spacing:-0.02em;color:#fafafa}
.card-value small{font-size:14px;font-weight:400;color:#71717a}
.status-badge{display:inline-flex;align-items:center;gap:6px}
.status-badge::before{content:"";width:7px;height:7px;border-radius:50%%;display:inline-block}
.status-badge.ok::before{background:#22c55e;box-shadow:0 0 6px rgba(34,197,94,.4)}
.status-badge.error::before{background:#a3a3a3;box-shadow:none}
.card-value .status-badge.ok{color:#22c55e}
.card-value .status-badge.error{color:#a1a1aa}
.stats{margin-bottom:24px}
.stats-label{font-size:11px;font-weight:500;text-transform:uppercase;letter-spacing:.08em;color:#71717a;margin-bottom:10px}
.stat-row{display:flex;gap:12px}
.stat-item{flex:1;background:#1a1a1d;border-radius:10px;border:1px solid #27272a;padding:14px 16px}
.stat-item .num{font-size:20px;font-weight:600;color:#fafafa}
.stat-item .desc{font-size:12px;color:#71717a;margin-top:2px}
.step-box{background:#1a1a1d;border-radius:12px;border:1px solid #27272a;padding:20px;margin-bottom:20px}
.step-box h4{font-size:13px;font-weight:600;color:#d4d4d8;margin-bottom:12px}
.step-box ol{padding-left:18px;color:#a1a1aa;font-size:13px;line-height:1.7}
.step-box li{margin-bottom:6px}
.step-box li:last-child{margin-bottom:0}
.actions{display:flex;gap:10px;margin-bottom:24px}
.btn{display:inline-flex;align-items:center;justify-content:center;padding:10px 20px;font-size:14px;font-weight:500;border-radius:10px;cursor:pointer;text-decoration:none;border:none;transition:background .15s,box-shadow .15s}
.btn-primary{background:#2563eb;color:#fff}
.btn-primary:hover{background:#2563ebdd;box-shadow:0 0 20px rgba(37,99,235,.25)}
.btn-success{background:#16a34a;color:#fff}
.btn-success:hover{background:#16a34add;box-shadow:0 0 20px rgba(22,163,74,.2)}
.cookie-section{border-top:1px solid #222225;padding-top:20px;margin-top:4px}
.cookie-section label{display:block;font-size:13px;font-weight:500;color:#d4d4d8;margin-bottom:10px}
.cookie-section textarea{width:100%%;height:90px;padding:10px 12px;border:1px solid #27272a;border-radius:10px;background:#1a1a1d;color:#e4e4e7;font-family:ui-monospace,SFMono-Regular,"SF Mono",Menlo,Consolas,monospace;font-size:12px;resize:vertical;box-sizing:border-box;transition:border-color .15s}
.cookie-section textarea:focus{outline:none;border-color:#2563eb;box-shadow:0 0 0 3px rgba(37,99,235,.15)}
.cookie-section textarea::placeholder{color:#52525b}
.cookie-section button{margin-top:10px}
.footer{margin-top:24px;padding-top:16px;border-top:1px solid #222225;display:flex;justify-content:space-between;align-items:center;font-size:12px;color:#52525b}
.footer a{color:#71717a;text-decoration:none}
.footer a:hover{color:#a1a1aa}
@media(max-width:540px){.container{padding:20px;border-radius:16px}.grid{grid-template-columns:1fr}.stat-row{flex-direction:column}.actions{flex-direction:column}}
</style>
</head>
<body>
<div class="container">
<h1>GeminiGo <span>v2.0</span></h1>

<div class="grid">
<div class="card"><div class="card-label">Phiên bản</div><div class="card-value">2.0.0</div></div>
<div class="card"><div class="card-label">Cookie</div><div class="card-value"><span class="%s">%s</span></div></div>
</div>

<div class="stats">
<div class="stats-label">Thống kê</div>
<div class="stat-row">
<div class="stat-item"><div class="num">%d</div><div class="desc">Tổng request</div></div>
<div class="stat-item"><div class="num">%d</div><div class="desc">Tokens</div></div>
<div class="stat-item"><div class="num">%s</div><div class="desc">Gần nhất</div></div>
</div>
</div>

<div class="step-box">
<h4>Cách lấy Cookie tự động</h4>
<ol>
<li>Nhấn nút <strong>Tự động kết nối</strong> bên dưới.</li>
<li>Hệ thống mở tab Chrome/Edge riêng, chuyển hướng tới <strong>gemini.google.com</strong>.</li>
<li>Đăng nhập tài khoản Google trên tab đó.</li>
<li>Cookie tự động được phát hiện và lưu lại. Trình duyệt tự đóng.</li>
</ol>
</div>

<div class="actions">
<a href="/admin/auto-login" class="btn btn-success">Tự động kết nối</a>
</div>

<div class="cookie-section">
<label for="cookie">Dán thủ công cookie (khi Auto-Login không hoạt động)</label>
<form action="/admin/save-cookie" method="POST">
<textarea name="cookie" id="cookie" placeholder="SAPISID=xxx; __Secure-1PSID=xxx; ..."></textarea>
<button type="submit" class="btn btn-primary">Cập nhật</button>
</form>
</div>

<div class="footer">
<a href="/v1/models">API Models</a>
<span>gemini.google.com proxy</span>
</div>
</div>
</body>
</html>`, cookieClass, cookieStatus, totalReqs, totalTokens, lastReqDisplay)
}

func handleSaveCookie(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	rawCookie := strings.TrimSpace(r.FormValue("cookie"))

	if rawCookie != "" {
		if err := addAccountCookie(rawCookie); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintf(w, `<h2>Lưu Cookie thất bại: %v</h2><a href="/">Quay lại</a>`, err)
			return
		}
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleAutoLogin(w http.ResponseWriter, r *http.Request) {
	go startChromeAutoLogin()
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
