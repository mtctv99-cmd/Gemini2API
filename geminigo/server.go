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

	cookieStatus := "Chưa cấu hình (Chạy ẩn danh)"
	if totalAccounts > 0 {
		cookieStatus = fmt.Sprintf("Đã cấu hình %d tài khoản", totalAccounts)
	}

	STATS.mu.RLock()
	totalReqs := STATS.TotalRequests
	totalTokens := STATS.TotalTokens
	lastReqAt := STATS.LastRequestAt
	if lastReqAt == "" {
		lastReqAt = "Chưa có request"
	}
	STATS.mu.RUnlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <title>GeminiGo Dashboard</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #f0f2f5; margin: 0; padding: 20px; color: #1e293b; }
        .container { max-width: 850px; margin: 50px auto; background: white; padding: 40px; border-radius: 16px; box-shadow: 0 10px 15px -3px rgba(0, 0, 0, 0.1), 0 4px 6px -2px rgba(0, 0, 0, 0.05); border: 1px solid #e2e8f0; }
        h1 { color: #0f172a; border-bottom: 2px solid #f1f5f9; padding-bottom: 20px; margin-top: 0; display: flex; align-items: center; gap: 10px; font-size: 28px; }
        .badge { background: #3b82f6; color: white; font-size: 12px; padding: 4px 10px; border-radius: 9999px; font-weight: normal; }
        .status-box { display: grid; grid-template-columns: repeat(2, 1fr); gap: 20px; margin: 25px 0; }
        .stats-box { display: grid; grid-template-columns: repeat(3, 1fr); gap: 20px; margin: 25px 0; border-top: 1px solid #f1f5f9; padding-top: 25px; }
        .status-card { padding: 20px; background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 12px; transition: transform 0.2s; }
        .status-card:hover { transform: translateY(-2px); }
        .status-card h3 { margin: 0 0 8px 0; font-size: 13px; color: #64748b; text-transform: uppercase; letter-spacing: 0.05em; }
        .status-card p { margin: 0; font-size: 22px; font-weight: 700; color: #3b82f6; }
        .status-card.active p { color: #10b981; }
        .status-card.err p { color: #f59e0b; }
        .btn-group { display: flex; gap: 15px; margin-top: 25px; }
        form { margin-top: 35px; border-top: 1px dashed #e2e8f0; padding-top: 25px; }
        label { display: block; font-weight: 600; margin-bottom: 10px; color: #334155; }
        textarea { width: 100%%; height: 110px; padding: 12px; border: 1px solid #cbd5e1; border-radius: 8px; font-family: monospace; box-sizing: border-box; resize: vertical; background: #fafafa; font-size: 13px; }
        textarea:focus { outline: none; border-color: #3b82f6; box-shadow: 0 0 0 3px rgba(59, 130, 246, 0.15); }
        button, .btn { background: #2563eb; color: white; border: none; padding: 14px 28px; font-size: 15px; font-weight: 600; border-radius: 8px; cursor: pointer; text-decoration: none; display: inline-block; text-align: center; transition: background 0.2s, box-shadow 0.2s; box-shadow: 0 4px 6px -1px rgba(37, 99, 235, 0.2); }
        button:hover, .btn:hover { background: #1d4ed8; box-shadow: 0 10px 15px -3px rgba(37, 99, 235, 0.3); }
        .btn-secondary { background: #10b981; box-shadow: 0 4px 6px -1px rgba(16, 185, 129, 0.2); }
        .btn-secondary:hover { background: #059669; box-shadow: 0 10px 15px -3px rgba(16, 185, 129, 0.3); }
        .step-box { margin-top: 30px; padding: 25px; background: #f0f9ff; border-left: 5px solid #0284c7; border-radius: 8px; }
        .step-box h4 { margin: 0 0 12px 0; color: #0369a1; font-size: 16px; font-weight: 700; }
        .step-box ol { margin: 0; padding-left: 20px; color: #075985; line-height: 1.7; font-size: 14.5px; }
        .step-box li { margin-bottom: 8px; }
        .step-box li:last-child { margin-bottom: 0; }
    </style>
</head>
<body>
    <div class="container">
        <h1>GeminiGo Control Center <span class="badge">PRO v2.0</span></h1>

        <div class="status-box">
            <div class="status-card">
                <h3>Phiên bản</h3>
                <p>2.0.0-geminigo</p>
            </div>
            <div class="status-card %s">
                <h3>Trạng thái Cookie</h3>
                <p>%s</p>
            </div>
        </div>

        <div class="stats-box">
            <div class="status-card">
                <h3>Tổng số Request</h3>
                <p>%d</p>
            </div>
            <div class="status-card">
                <h3>Sản lượng Tokens</h3>
                <p>%d</p>
            </div>
            <div class="status-card">
                <h3>Request cuối cùng</h3>
                <p style="font-size: 16px; color: #475569; padding-top: 6px;">%s</p>
            </div>
        </div>

        <div class="step-box">
            <h4>Quy trình Auto-Login & Trích xuất Cookie tự động:</h4>
            <ol>
                <li>Nhấn nút <strong>Tự động kết nối Trình duyệt</strong> để kích hoạt.</li>
                <li>Hệ thống tự động tìm và khởi chạy một tab trình duyệt Chrome/Edge độc lập hướng thẳng tới Google Gemini.</li>
                <li>Bạn tiến hành Đăng nhập tài khoản Google của bạn trên tab trình duyệt đó.</li>
                <li>Khi đăng nhập thành công, Server sẽ tự động phát hiện cookie bảo mật, lưu trữ cấu hình và <strong>tự động đóng trình duyệt đó lập tức</strong>. Trạng thái Dashboard sẽ chuyển sang kích hoạt thành công.</li>
            </ol>
        </div>

        <div class="btn-group">
            <a href="/admin/auto-login" class="btn btn-secondary">Tự động kết nối Trình duyệt</a>
        </div>

        <form action="/admin/save-cookie" method="POST">
            <label for="cookie">Dán thủ công Cụm Cookie (Chỉ dùng khi tính năng Auto-Login không khởi động được trình duyệt):</label>
            <textarea name="cookie" id="cookie" placeholder="SAPISID=xxx; __Secure-1PSID=xxx; ..."></textarea>
            <button type="submit">Cập nhật thủ công</button>
        </form>
    </div>
</body>
</html>`, func() string {
		if totalAccounts == 0 {
			return "err"
		}
		return "active"
	}(), cookieStatus, totalReqs, totalTokens, lastReqAt)
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
