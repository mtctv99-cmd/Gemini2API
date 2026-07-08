package main

import (
	"encoding/json"
	"log"
	"os"
)

type Config struct {
	Port              int      `json:"port"`
	Host              string   `json:"host"`
	RetryAttempts     int      `json:"retry_attempts"`
	RetryDelaySec     int      `json:"retry_delay_sec"`
	RequestTimeoutSec int      `json:"request_timeout_sec"`
	GeminiBL          string   `json:"gemini_bl"`
	AuthUser          string   `json:"auth_user"`
	XsrfToken         string   `json:"xsrf_token"`
	DefaultModel      string   `json:"default_model"`
	LogRequests       bool     `json:"log_requests"`
	CookieFile        string   `json:"cookie_file"`
	Proxy             string   `json:"proxy"`
	ApiKeys           []string `json:"api_keys"`
}

var CONFIG = Config{
	Port:              8081,
	Host:              "0.0.0.0",
	RetryAttempts:     3,
	RetryDelaySec:     2,
	RequestTimeoutSec: 180,
	GeminiBL:          "boq_assistant-bard-web-server_20260525.09_p0",
	DefaultModel:      "gemini-2.0-flash",
	LogRequests:       true,
}

type ModelCfg struct {
	Mode     int    `json:"mode"`
	Think    int    `json:"think"`
	ImageGen bool   `json:"image_gen,omitempty"`
	Desc     string `json:"desc"`
}

var MODELS = map[string]ModelCfg{
	"gemini-2.0-flash": {
		Mode:  1,
		Think: 0,
		Desc:  "Fast general-purpose model, lowest latency",
	},
	"gemini-2.0-flash-thinking": {
		Mode:  2,
		Think: 4,
		Desc:  "Deep thinking mode, longer output (~20k chars)",
	},
	"gemini-2.5-pro": {
		Mode:  3,
		Think: 4,
		Desc:  "Most capable model, best for complex tasks",
	},
	"gemini-auto": {
		Mode:  4,
		Think: 4,
		Desc:  "Auto model selection (routes based on prompt complexity)",
	},
	"gemini-2.0-flash-thinking-lite": {
		Mode:  5,
		Think: 4,
		Desc:  "Faster thinking model, ~8k char context window",
	},
	"gemini-2.0-flash-lite": {
		Mode:  6,
		Think: 0,
		Desc:  "Fastest model, lowest latency, best for simple tasks",
	},
	"imagen-3.0": {
		Mode:     6,
		Think:    0,
		ImageGen: true,
		Desc:     "Image generation via Imagen 3.0 (prompt-based, returns image URLs)",
	},
}

func loadConfig(path string) {
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("Warning: could not read config %s: %v\n", path, err)
		return
	}
	if err := json.Unmarshal(data, &CONFIG); err != nil {
		log.Printf("Warning: invalid config JSON in %s: %v\n", path, err)
	}
}
