package main

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	pushIDPattern = regexp.MustCompile(`"qKIAYe":"([^"]+)"`)
	pctxPattern   = regexp.MustCompile(`"Ylro7b":"([^"]+)"`)
)

type TokenCache struct {
	mu     sync.Mutex
	PushID string
	Pctx   string
	Ts     time.Time
}

var tokenCache TokenCache

func getPageTokens() (string, string) {
	tokenCache.mu.Lock()
	defer tokenCache.mu.Unlock()

	if time.Since(tokenCache.Ts) < 10*time.Minute && tokenCache.PushID != "" {
		return tokenCache.PushID, tokenCache.Pctx
	}

	client := getHTTPClient()
	req, err := http.NewRequest("GET", "https://gemini.google.com/app", nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	cStr, _ := getNextCookie()
	if cStr != "" {
		req.Header.Set("Cookie", cStr)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	pushID := ""
	pctx := ""

	if m := pushIDPattern.FindStringSubmatch(body); len(m) > 1 {
		pushID = m[1]
	}
	if m := pctxPattern.FindStringSubmatch(body); len(m) > 1 {
		pctx = m[1]
	}

	if pushID == "" || pctx == "" {
		return "", ""
	}

	tokenCache.PushID = pushID
	tokenCache.Pctx = pctx
	tokenCache.Ts = time.Now()

	return pushID, pctx
}

type uploadedFile struct {
	ID   string
	Name string
}

func uploadImage(imageBytes []byte, filename, mimeType string) (uploadedFile, error) {
	pushID, _ := getPageTokens()
	if pushID == "" {
		return uploadedFile{}, fmt.Errorf("could not obtain upload tokens (no logged-in session?)")
	}
	cStr, _ := getNextCookie()
	client := getHTTPClient()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, strings.ReplaceAll(filename, `"`, `\"`)))
	header.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return uploadedFile{}, err
	}
	if _, err := part.Write(imageBytes); err != nil {
		return uploadedFile{}, err
	}
	_ = writer.Close()

	uploadReq, err := http.NewRequest("POST", "https://content-push.googleapis.com/upload", &body)
	if err != nil {
		return uploadedFile{}, err
	}

	h := uploadReq.Header
	h.Set("Content-Type", writer.FormDataContentType())
	h.Set("Origin", "https://gemini.google.com")
	h.Set("Referer", "https://gemini.google.com/")
	h.Set("X-Tenant-Id", "bard-storage")
	h.Set("Push-ID", pushID)
	h.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	if cStr != "" {
		h.Set("Cookie", cStr)
	}

	resp, err := client.Do(uploadReq)
	if err != nil {
		return uploadedFile{}, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return uploadedFile{}, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return uploadedFile{}, fmt.Errorf("upload failed status %d: %s", resp.StatusCode, string(respBytes))
	}

	ref := strings.TrimSpace(string(respBytes))
	if ref == "" || !strings.HasPrefix(ref, "/") {
		return uploadedFile{}, fmt.Errorf("invalid file reference from google: %s", ref)
	}

	return uploadedFile{ID: ref, Name: filename}, nil
}

func fetchImageBytes(url string) ([]byte, error) {
	client := getHTTPClient()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func extensionForMimeType(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".bin"
	}
}

func mimeByExtension(name string) string {
	ext := filepath.Ext(name)
	m := mime.TypeByExtension(ext)
	if m == "" {
		return "application/octet-stream"
	}
	return m
}
