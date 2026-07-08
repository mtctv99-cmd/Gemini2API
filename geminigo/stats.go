package main

import (
	"sync"
	"time"
)

type TrafficStats struct {
	mu            sync.RWMutex
	TotalRequests int64
	TotalTokens   int64
	LastRequestAt string
}

var STATS TrafficStats

func updateStats(tokens int64) {
	STATS.mu.Lock()
	defer STATS.mu.Unlock()
	STATS.TotalRequests++
	STATS.TotalTokens += tokens
	STATS.LastRequestAt = time.Now().Format("2006-01-02 15:04:05")
}
