package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	codePattern   = regexp.MustCompile("(?s)```(?:python|javascript|text)\\?code_(?:reference|stdout)&code_event_index=\\d+\\n.*?```\\n?")
	cardPattern   = regexp.MustCompile(`http://googleusercontent\.com/card_content/\d+\n?`)
	imageURLRegex = regexp.MustCompile(`(?i)(?:https?:)?//[^\s"'<>\\]+|(?:[a-z0-9.-]+\.)?googleusercontent\.com/[^\s"'<>\\]+`)
	httpClient    *http.Client
	httpClientMu  sync.Mutex
)

func cleanText(text string) string {
	text = codePattern.ReplaceAllString(text, "")
	text = cardPattern.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

func extractTextsFromLine(line string) []string {
	if !strings.Contains(line, `"wrb.fr"`) || len(line) < 200 {
		return nil
	}
	var arr []interface{}
	if err := json.Unmarshal([]byte(line), &arr); err != nil {
		return nil
	}
	if len(arr) == 0 {
		return nil
	}
	subArr, ok := arr[0].([]interface{})
	if !ok || len(subArr) < 3 {
		return nil
	}
	innerStr, ok := subArr[2].(string)
	if !ok || len(innerStr) < 50 {
		return nil
	}
	var inner []interface{}
	if err := json.Unmarshal([]byte(innerStr), &inner); err != nil {
		return nil
	}
	if len(inner) <= 4 || inner[4] == nil {
		return nil
	}
	parts, ok := inner[4].([]interface{})
	if !ok {
		return nil
	}
	var texts []string
	for _, part := range parts {
		pList, ok := part.([]interface{})
		if !ok || len(pList) <= 1 || pList[1] == nil {
			continue
		}
		tList, ok := pList[1].([]interface{})
		if !ok {
			continue
		}
		for _, t := range tList {
			if s, ok := t.(string); ok && s != "" {
				texts = append(texts, s)
			}
		}
	}
	return texts
}

func extractResponseText(raw string) string {
	lastText := ""
	for _, line := range strings.Split(raw, "\n") {
		for _, t := range extractTextsFromLine(line) {
			if len(t) > len(lastText) {
				lastText = t
			}
		}
	}
	return cleanText(lastText)
}

func extractThinkingAndText(raw string) (string, string) {
	var startIdx = -1
	var startLen = 0

	markers := []string{"<ctrl94>thought", "<ctrl94>"}
	for _, marker := range markers {
		if idx := strings.Index(raw, marker); idx != -1 {
			startIdx = idx
			startLen = len(marker)
			break
		}
	}

	if startIdx == -1 {
		return "", raw
	}

	var endIdx = -1
	var endLen = 0
	endMarkers := []string{"<ctrl95>"}
	for _, marker := range endMarkers {
		if idx := strings.Index(raw[startIdx+startLen:], marker); idx != -1 {
			endIdx = startIdx + startLen + idx
			endLen = len(marker)
			break
		}
	}

	if endIdx == -1 {
		thinking := strings.TrimSpace(raw[startIdx+startLen:])
		textBefore := strings.TrimSpace(raw[:startIdx])
		return thinking, textBefore
	}

	thinking := strings.TrimSpace(raw[startIdx+startLen : endIdx])
	textBefore := raw[:startIdx]
	textAfter := raw[endIdx+endLen:]

	textContent := strings.TrimSpace(textBefore + textAfter)
	return thinking, textContent
}

func collectImages(text string) []string {
	var images []string
	seen := make(map[string]bool)
	matches := imageURLRegex.FindAllString(text, -1)
	for _, match := range matches {
		cleaned := strings.TrimSpace(match)
		cleaned = strings.TrimRight(cleaned, ".,);]")
		if strings.HasPrefix(cleaned, "//") {
			cleaned = "https:" + cleaned
		}
		lower := strings.ToLower(cleaned)
		if strings.Contains(lower, "googleusercontent.com") {
			if !strings.HasPrefix(lower, "http") {
				cleaned = "https://" + cleaned
			}
			if !seen[cleaned] {
				seen[cleaned] = true
				images = append(images, cleaned)
			}
		}
	}
	return images
}

func getHTTPClient() *http.Client {
	httpClientMu.Lock()
	defer httpClientMu.Unlock()
	if httpClient != nil {
		return httpClient
	}
	tr := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	}
	if CONFIG.Proxy != "" {
		proxyURL, err := url.Parse(CONFIG.Proxy)
		if err == nil {
			tr.Proxy = http.ProxyURL(proxyURL)
		}
	}
	timeout := time.Duration(CONFIG.RequestTimeoutSec) * time.Second
	httpClient = &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}
	return httpClient
}

func generateText(ctx context.Context, prompt string, modelID, thinkMode int, fileRefs []uploadedFile, imageGen bool) (string, error) {
	client := getHTTPClient()
	body := buildPayload(prompt, modelID, thinkMode, fileRefs, imageGen)
	req, err := http.NewRequestWithContext(ctx, "POST", getStreamURL(), strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header = buildHeaders()

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return extractResponseText(string(data)), nil
}
