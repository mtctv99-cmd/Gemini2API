package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var browserPaths = []string{
	// Windows
	`C:\Program Files\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
	`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
	// macOS
	`/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`,
	`/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge`,
	// Linux
	`/usr/bin/google-chrome`,
	`/usr/bin/microsoft-edge`,
	`/usr/bin/chromium`,
}

func findBrowser() string {
	for _, path := range browserPaths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	// Fallback to exec.LookPath
	for _, name := range []string{"google-chrome", "chrome", "msedge", "edge", "chromium"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

// Minimal WebSocket client implementation (RFC 6455)
func wsConnect(wsURL string) (net.Conn, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := net.Dial("tcp", host)
	if err != nil {
		return nil, err
	}

	keyBytes := make([]byte, 16)
	_, _ = rand.Read(keyBytes)
	secKey := base64.StdEncoding.EncodeToString(keyBytes)

	req := fmt.Sprintf("GET %s HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n", u.RequestURI(), u.Host, secKey)

	_, err = conn.Write([]byte(req))
	if err != nil {
		conn.Close()
		return nil, err
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != 101 {
		conn.Close()
		return nil, fmt.Errorf("handshake failed: %d", resp.StatusCode)
	}
	return conn, nil
}

func wsWriteText(conn net.Conn, payload string) error {
	data := []byte(payload)
	length := len(data)
	var header []byte
	header = append(header, 0x81) // Fin + Text frame
	mask := []byte{0x01, 0x02, 0x03, 0x04}

	if length <= 125 {
		header = append(header, byte(length|0x80))
	} else if length <= 65535 {
		header = append(header, 126|0x80)
		header = append(header, byte(length>>8), byte(length&0xFF))
	} else {
		return fmt.Errorf("payload too large")
	}

	header = append(header, mask...)
	masked := make([]byte, length)
	for i := 0; i < length; i++ {
		masked[i] = data[i] ^ mask[i%4]
	}
	_, err := conn.Write(append(header, masked...))
	return err
}

func wsReadText(conn net.Conn) (string, error) {
	// ponytail: handles text, close, and ping frames. Pong auto-reply.
	// add when: fragmented frames, compression extensions.
	for {
		buf := make([]byte, 2)
		_, err := io.ReadFull(conn, buf)
		if err != nil {
			return "", err
		}
		opcode := buf[0] & 0x0F
		if opcode == 9 { // ping
			_ = wsWritePong(conn)
			continue
		}
		if opcode != 8 && opcode != 1 {
			return "", fmt.Errorf("unhandled opcode %d", opcode)
		}
		if opcode == 8 {
			return "", io.EOF
		}
		// opcode 1 = text
		masked := (buf[1] & 0x80) != 0
		length := int(buf[1] & 0x7F)

		if length == 126 {
			lenBuf := make([]byte, 2)
			_, _ = io.ReadFull(conn, lenBuf)
			length = int(lenBuf[0])<<8 | int(lenBuf[1])
		} else if length == 127 {
			lenBuf := make([]byte, 8)
			_, _ = io.ReadFull(conn, lenBuf)
			length = int(lenBuf[4])<<24 | int(lenBuf[5])<<16 | int(lenBuf[6])<<8 | int(lenBuf[7])
		}

		var mask []byte
		if masked {
			mask = make([]byte, 4)
			_, _ = io.ReadFull(conn, mask)
		}

		payload := make([]byte, length)
		_, err = io.ReadFull(conn, payload)
		if err != nil {
			return "", err
		}

		if masked {
			for i := 0; i < length; i++ {
				payload[i] = payload[i] ^ mask[i%4]
			}
		}
		return string(payload), nil
	}
}

func wsWritePong(conn net.Conn) error {
	_, err := conn.Write([]byte{0x8A, 0x00}) // Fin + Pong, length 0
	return err
}

type TabInfo struct {
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	URL                  string `json:"url"`
}

func startChromeAutoLogin() {
	browserPath := findBrowser()
	if browserPath == "" {
		log.Println("[AutoLogin] Không tìm thấy Chrome/Edge trên máy.")
		return
	}
	log.Printf("[AutoLogin] Đang khởi chạy trình duyệt: %s\n", browserPath)

	profileDir, _ := filepath.Abs("data/browser_profile")
	_ = os.MkdirAll(profileDir, 0755)

	cmd := exec.Command(browserPath,
		"--remote-debugging-port=9222",
		"--user-data-dir="+profileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"https://gemini.google.com/app",
	)

	err := cmd.Start()
	if err != nil {
		log.Printf("[AutoLogin] Lỗi khởi chạy trình duyệt: %v\n", err)
		return
	}

	go func() {
		defer func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}()

		client := &http.Client{Timeout: 2 * time.Second}
		var wsDebuggerURL string

		for i := 0; i < 30; i++ {
			time.Sleep(1 * time.Second)
			resp, err := client.Get("http://127.0.0.1:9222/json/list")
			if err != nil {
				continue
			}
			var tabs []TabInfo
			_ = json.NewDecoder(resp.Body).Decode(&tabs)
			resp.Body.Close()

			for _, tab := range tabs {
				if strings.Contains(tab.URL, "gemini.google.com") && tab.WebSocketDebuggerURL != "" {
					wsDebuggerURL = tab.WebSocketDebuggerURL
					break
				}
			}
			if wsDebuggerURL != "" {
				break
			}
		}

		if wsDebuggerURL == "" {
			log.Println("[AutoLogin] Không tìm thấy tab Gemini Debugger qua cổng 9222.")
			return
		}
		log.Printf("[AutoLogin] Đã kết nối Debugger: %s\n", wsDebuggerURL)

		ws, err := wsConnect(wsDebuggerURL)
		if err != nil {
			log.Printf("[AutoLogin] Lỗi kết nối WebSocket Debugger: %v\n", err)
			return
		}
		defer ws.Close()

		// Do NOT enable Network events to avoid message flooding.
		// _ = wsWriteText(ws, `{"id": 1, "method": "Network.enable"}`)

		log.Println("[AutoLogin] Đang chờ bạn đăng nhập tài khoản Google trên trình duyệt...")

		for {
			time.Sleep(2 * time.Second)
			// Send request to get cookies
			reqID := 2
			_ = wsWriteText(ws, fmt.Sprintf(`{"id": %d, "method": "Network.getCookies", "params": {"urls": ["https://gemini.google.com"]}}`, reqID))

			// Read messages until we get the response for our request ID
			for {
				raw, err := wsReadText(ws)
				if err != nil {
					log.Printf("[AutoLogin] Mất kết nối đọc WebSocket: %v\n", err)
					return
				}

				var responseObj struct {
					ID     int `json:"id"`
					Result struct {
						Cookies []struct {
							Name  string `json:"name"`
							Value string `json:"value"`
						} `json:"cookies"`
					} `json:"result"`
				}

				if err := json.Unmarshal([]byte(raw), &responseObj); err == nil {
					if responseObj.ID == reqID {
						cookieParts := []string{}
						sapisid := ""
						secure1PSID := ""

						for _, cookie := range responseObj.Result.Cookies {
							cookieParts = append(cookieParts, fmt.Sprintf("%s=%s", cookie.Name, cookie.Value))
							if cookie.Name == "SAPISID" {
								sapisid = cookie.Value
							}
							if cookie.Name == "__Secure-1PSID" {
								secure1PSID = cookie.Value
							}
						}

						if sapisid != "" && secure1PSID != "" {
							cookieStr := strings.Join(cookieParts, "; ")
							if err := addAccountCookie(cookieStr); err != nil {
								log.Printf("[AutoLogin] Lỗi lưu cookie: %v\n", err)
							} else {
								log.Println("[AutoLogin] THÀNH CÔNG! Đã tự động bóc tách và lưu Cookie tài khoản Google.")
							}

							_ = wsWriteText(ws, `{"id": 3, "method": "Browser.close"}`)
							time.Sleep(500 * time.Millisecond)
							return
						}
						// We got our cookie response, but not logged in yet. Break inner loop to sleep and check again.
						break
					}
				}
			}
		}
	}()
}
