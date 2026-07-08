package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type Message struct {
	Role       string      `json:"role"`
	Content    interface{} `json:"content"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
	Name       string      `json:"name,omitempty"`
}

type Tool struct {
	Type     string `json:"type"`
	Function *struct {
		Name        string                 `json:"name"`
		Description string                 `json:"description,omitempty"`
		Parameters  map[string]interface{} `json:"parameters,omitempty"`
	} `json:"function,omitempty"`
}

type ParsedImage struct {
	Data []byte
	Mime string
}

func messagesToPrompt(messages []Message, tools []Tool, toolChoice interface{}) (string, []ParsedImage) {
	var parts []string
	var images []ParsedImage

	if len(tools) > 0 && toolChoice != "none" {
		toolDefs := []map[string]interface{}{}
		for _, tool := range tools {
			name, desc := "", ""
			var params map[string]interface{}
			if tool.Type == "function" && tool.Function != nil {
				name = tool.Function.Name
				desc = tool.Function.Description
				params = tool.Function.Parameters
			}
			toolDefs = append(toolDefs, map[string]interface{}{
				"name":        name,
				"description": desc,
				"parameters":  params,
			})
		}
		if len(toolDefs) > 0 {
			toolDefsJSON, _ := json.MarshalIndent(toolDefs, "", "  ")
			forceMsg := ""
			if tc, ok := toolChoice.(map[string]interface{}); ok {
				if fn, ok := tc["function"].(map[string]interface{}); ok {
					if name, ok := fn["name"].(string); ok {
						forceMsg = fmt.Sprintf("\n\nYou MUST call the tool '%s' and only that tool.", name)
					}
				}
			}
			parts = append(parts, fmt.Sprintf("# Tool Use\n\nYou can call the following tools. Call format:\n```tool_call\n{\"name\": \"func_name\", \"arguments\": {...}}\n```\n\nWhen calling tools, output ONLY the tool_call block(s).\n\nAvailable tools:\n%s%s", string(toolDefsJSON), forceMsg))
		}
	}

	for _, msg := range messages {
		role := msg.Role
		contentStr := ""

		switch val := msg.Content.(type) {
		case string:
			contentStr = val
		case []interface{}:
			var textParts []string
			for _, item := range val {
				if m, ok := item.(map[string]interface{}); ok {
					t, _ := m["type"].(string)
					if t == "text" || t == "input_text" {
						txt, _ := m["text"].(string)
						textParts = append(textParts, txt)
					} else if t == "image_url" {
						if urlMap, ok := m["image_url"].(map[string]interface{}); ok {
							if urlStr, ok := urlMap["url"].(string); ok {
								if strings.HasPrefix(urlStr, "data:") {
									// Extract base64 mime and data
									// data:image/png;base64,iVBORw...
									parts := strings.SplitN(urlStr, ",", 2)
									if len(parts) > 1 {
										mime := "image/png"
										header := parts[0]
										if strings.Contains(header, ";") {
											s1 := strings.Split(header, ";")[0]
											mime = strings.TrimPrefix(s1, "data:")
										}
										decoded, err := base64.StdEncoding.DecodeString(parts[1])
										if err == nil {
											images = append(images, ParsedImage{Data: decoded, Mime: mime})
										}
									}
								} else {
									decoded, err := fetchImageBytes(urlStr)
									if err == nil {
										images = append(images, ParsedImage{
											Data: decoded,
											Mime: "image/png", // default fallback
										})
									}
								}
							}
						}
					}
				}
			}
			contentStr = strings.Join(textParts, " ")
		}

		if role == "system" {
			parts = append(parts, fmt.Sprintf("[System instruction]: %s", contentStr))
		} else if role == "assistant" {
			parts = append(parts, fmt.Sprintf("[Assistant]: %s", contentStr))
		} else if role == "tool" {
			name := msg.Name
			if name == "" {
				name = msg.ToolCallID
			}
			if name == "" {
				name = "unknown"
			}
			parts = append(parts, fmt.Sprintf("[Tool result (%s)]: %s", name, contentStr))
		} else {
			parts = append(parts, contentStr)
		}
	}

	return strings.Join(parts, "\n\n"), images
}

type ParsedToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func parseToolCalls(text string) (string, []ParsedToolCall) {
	var toolCalls []ParsedToolCall
	pattern := `(?s)\` + "`" + `\` + "`" + `\` + "`" + `tool_call\s*\n(.*?)\n\` + "`" + `\` + "`" + `\` + "`"
	re := regexp.MustCompile(pattern)
	matches := re.FindAllStringSubmatch(text, -1)

	for i, match := range matches {
		if len(match) > 1 {
			var data struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(match[1])), &data); err == nil {
				argsJSON, _ := json.Marshal(data.Arguments)
				tc := ParsedToolCall{
					ID:   fmt.Sprintf("call_%d", i),
					Type: "function",
				}
				tc.Function.Name = data.Name
				tc.Function.Arguments = string(argsJSON)
				toolCalls = append(toolCalls, tc)
			}
		}
	}

	clean := re.ReplaceAllString(text, "")
	return strings.TrimSpace(clean), toolCalls
}

func stripToolCallBlocks(text string) string {
	re := regexp.MustCompile("(?s)\x60\x60\x60tool_call.*?\n\x60\x60\x60\n?")
	return re.ReplaceAllString(text, "")
}
