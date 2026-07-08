package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	port := flag.Int("port", 8081, "Port to listen on")
	configPath := flag.String("config", "", "Path to config.json")
	cookieFile := flag.String("cookie-file", "", "Path to cookie file")
	flag.Parse()

	if *configPath != "" {
		loadConfig(*configPath)
	}
	if *port != 8081 {
		CONFIG.Port = *port
	}
	if *cookieFile != "" {
		CONFIG.CookieFile = *cookieFile
	}

	loadPool()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			handleRoot(w, r)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/admin/save-cookie", handleSaveCookie)
	mux.HandleFunc("/admin/auto-login", handleAutoLogin)
	mux.HandleFunc("/v1/models", handleModels)
	mux.HandleFunc("/v1/chat/completions", handleChatCompletions)

	addr := fmt.Sprintf("%s:%d", CONFIG.Host, CONFIG.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("geminigo listening on http://%s\n", addr)
		log.Printf("Base URL: http://localhost:%d/v1\n", CONFIG.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v\n", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutting down gracefully...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Force shutdown: %v\n", err)
	}
}
