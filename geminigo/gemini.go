package main

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type AccountCookie struct {
	ID      string `json:"id"`
	Cookie  string `json:"cookie"`
	Sapisid string `json:"sapisid"`
}

type CookiePool struct {
	mu       sync.RWMutex
	Accounts []AccountCookie `json:"accounts"`
	index    uint64
}

var (
	pool      CookiePool
	reqIDAtom uint64
)

func loadPool() {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	cookieFile := CONFIG.CookieFile
	if cookieFile == "" {
		cookieFile = "data/cookies.json"
	}

	data, err := os.ReadFile(cookieFile)
	if err != nil {
		pool.Accounts = nil
		return
	}

	var accs []AccountCookie
	if err := json.Unmarshal(data, &accs); err == nil {
		pool.Accounts = accs
		return
	}

	// Legacy single cookie fallback
	content := strings.TrimSpace(string(data))
	if content != "" {
		var legacy struct {
			Cookie  string `json:"cookie"`
			Sapisid string `json:"sapisid"`
		}
		cookieStr := content
		sapisid := ""
		if err := json.Unmarshal(data, &legacy); err == nil {
			cookieStr = legacy.Cookie
			sapisid = legacy.Sapisid
		}
		if sapisid == "" {
			for _, part := range strings.Split(cookieStr, "; ") {
				if strings.HasPrefix(part, "SAPISID=") {
					sapisid = strings.TrimPrefix(part, "SAPISID=")
					break
				}
			}
		}
		pool.Accounts = []AccountCookie{
			{ID: "Account-1", Cookie: cookieStr, Sapisid: sapisid},
		}
	}
}

func savePool() error {
	pool.mu.RLock()
	data, err := json.MarshalIndent(pool.Accounts, "", "  ")
	pool.mu.RUnlock()

	if err != nil {
		return err
	}

	cookieFile := CONFIG.CookieFile
	if cookieFile == "" {
		cookieFile = "data/cookies.json"
	}
	if dir := filepath.Dir(cookieFile); dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}
	return os.WriteFile(cookieFile, data, 0644)
}

func addAccountCookie(rawCookie string) error {
	sapisid := ""
	for _, part := range strings.Split(rawCookie, "; ") {
		if strings.HasPrefix(part, "SAPISID=") {
			sapisid = strings.TrimPrefix(part, "SAPISID=")
			break
		}
	}

	pool.mu.Lock()
	id := fmt.Sprintf("Account-%d", len(pool.Accounts)+1)
	pool.Accounts = append(pool.Accounts, AccountCookie{
		ID:      id,
		Cookie:  rawCookie,
		Sapisid: sapisid,
	})
	pool.mu.Unlock()

	return savePool()
}

func getNextCookie() (string, string) {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	if len(pool.Accounts) == 0 {
		return "", ""
	}

	idx := atomic.AddUint64(&pool.index, 1) - 1
	acc := pool.Accounts[idx%uint64(len(pool.Accounts))]
	return acc.Cookie, acc.Sapisid
}

func makeSapisidHash(sapisid string) string {
	ts := time.Now().Unix()
	h := sha1.Sum([]byte(fmt.Sprintf("%d %s https://gemini.google.com", ts, sapisid)))
	return fmt.Sprintf("SAPISIDHASH %d_%x", ts, h)
}

func accountPrefix() string {
	if CONFIG.AuthUser == "" {
		return ""
	}
	return "/u/" + CONFIG.AuthUser
}

func buildHeaders() http.Header {
	prefix := accountPrefix()
	h := make(http.Header)
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	h.Set("Origin", "https://gemini.google.com")
	h.Set("Referer", fmt.Sprintf("https://gemini.google.com%s/app", prefix))
	h.Set("X-Same-Domain", "1")
	h.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	if prefix != "" {
		h.Set("X-Goog-AuthUser", CONFIG.AuthUser)
	}

	cStr, sapisid := getNextCookie()
	if cStr != "" {
		h.Set("Cookie", cStr)
	}
	if sapisid != "" {
		h.Set("Authorization", makeSapisidHash(sapisid))
	}
	return h
}

func getStreamURL() string {
	reqID := atomic.AddUint64(&reqIDAtom, 1) % 1000000
	prefix := accountPrefix()
	return fmt.Sprintf("https://gemini.google.com%s/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate?bl=%s&hl=en&_reqid=%d&rt=c", prefix, url.QueryEscape(CONFIG.GeminiBL), reqID)
}

func buildPayload(prompt string, modelID, thinkMode int, fileRefs []uploadedFile, imageGen bool) string {
	inner := make([]interface{}, 102)
	if len(fileRefs) > 0 {
		refs := make([][]interface{}, len(fileRefs))
		for i, ref := range fileRefs {
			refs[i] = []interface{}{[]interface{}{ref.ID}, ref.Name}
		}
		inner[0] = []interface{}{prompt, 0, nil, refs, nil, nil, 0}
	} else {
		inner[0] = []interface{}{prompt, 0, nil, nil, nil, nil, 0}
	}
	inner[1] = []string{"en"}
	inner[2] = []interface{}{"", "", "", nil, nil, nil, nil, nil, nil, ""}
	inner[6] = []int{0}
	inner[7] = 1
	inner[10] = 1
	inner[11] = 0
	inner[17] = [][]int{{thinkMode}}
	inner[18] = 0
	inner[27] = 1
	inner[30] = []int{4}
	inner[41] = []int{2}
	inner[53] = 0
	inner[59] = fmt.Sprintf("%d", time.Now().UnixNano())
	inner[61] = []interface{}{}
	inner[68] = 1
	inner[79] = modelID

	if imageGen {
		inner[15] = []interface{}{nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, []interface{}{1, 1}}
		inner[42] = 1
		inner[43] = 1
	}

	innerJSON, _ := json.Marshal(inner)
	outer := []interface{}{nil, string(innerJSON)}
	outerJSON, _ := json.Marshal(outer)

	val := url.Values{}
	val.Set("f.req", string(outerJSON))
	if CONFIG.XsrfToken != "" {
		val.Set("at", CONFIG.XsrfToken)
	}
	return val.Encode()
}
