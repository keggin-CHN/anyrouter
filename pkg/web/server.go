package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"anyrouter/pkg/client"
	"anyrouter/pkg/keeper"
)

//go:embed static/*
var staticFS embed.FS

// Server hosts the local web dashboard for AnyRouter Keeper.
type Server struct {
	port   int
	matrix *keeper.Matrix
	server *http.Server
	onHide func()
	onExit func()
}

// SetWindowControls registers callbacks for desktop window control.
func (s *Server) SetWindowControls(onHide, onExit func()) {
	s.onHide = onHide
	s.onExit = onExit
}

// NewServer creates a web server instance.
func NewServer(port int, matrix *keeper.Matrix) *Server {
	if port <= 0 {
		port = 28888
	}
	return &Server{
		port:   port,
		matrix: matrix,
	}
}

// Start launches the HTTP server asynchronously.
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// Static page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		var data []byte
		var err error
		candidates := []string{"web/index.html", "pkg/web/static/index.html"}
		for _, p := range candidates {
			if b, readErr := os.ReadFile(p); readErr == nil {
				data = b
				break
			}
		}
		if data == nil {
			data, err = staticFS.ReadFile("static/index.html")
			if err != nil {
				http.Error(w, "Asset not found", http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})

	// API: Status
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"is_running": s.matrix.IsRunning(),
			"states":     s.matrix.GetStates(),
			"models":     keeper.AvailableModelCandidates,
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	// API: Logs
	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		logs := s.matrix.GetLogs(100)
		_ = json.NewEncoder(w).Encode(logs)
	})

	// API: Config
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(s.matrix.GetConfig())
			return
		}
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			newCfg := keeper.DefaultConfig()
			if err := json.Unmarshal(body, newCfg); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := s.matrix.Reload(newCfg); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	})

	// API: Start
	mux.HandleFunc("/api/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_ = s.matrix.Start()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	// API: Stop
	mux.HandleFunc("/api/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.matrix.Stop()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	// API: Clear Logs
	mux.HandleFunc("/api/clear-logs", func(w http.ResponseWriter, r *http.Request) {
		s.matrix.ClearLogs()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	// API: Proxy Check
	mux.HandleFunc("/api/proxy-check", func(w http.ResponseWriter, r *http.Request) {
		proxyURL := s.matrix.GetConfig().Proxy
		ok := keeper.CheckProxyReachable(proxyURL)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"proxy": proxyURL,
			"ok":    ok,
		})
	})

	// API: Ping Probe (used by Drawer instant model test playground)
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Key    string `json:"key"`
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		cfg := s.matrix.GetConfig()
		if req.Key == "" {
			if len(cfg.APIKeys) > 0 {
				req.Key = cfg.APIKeys[0]
			} else if cfg.APIKey != "" {
				req.Key = cfg.APIKey
			}
		}
		if req.Model == "" {
			req.Model = "gpt-6-astra"
		}
		if req.Prompt == "" {
			req.Prompt = "1+1"
		}

		c, err := client.NewClient(client.ClientConfig{
			APIKey:  req.Key,
			Proxy:   cfg.Proxy,
			Timeout: 20 * time.Second,
		})
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}

		t0 := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()

		events, streamErr := c.StreamChat(ctx, req.Model, req.Prompt, nil, "", "", 128)
		if streamErr != nil {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":      false,
				"error":   streamErr.Error(),
				"time_ms": time.Since(t0).Milliseconds(),
			})
			return
		}

		var fullText strings.Builder
		var fullThinking strings.Builder
		var lastErr string
		success := false

		for ev := range events {
			if ev.Type == "text" {
				fullText.WriteString(ev.Delta)
				success = true
			} else if ev.Type == "thinking" {
				fullThinking.WriteString(ev.Delta)
				success = true
			} else if ev.Type == "stream_error" {
				lastErr = ev.Reason
			} else if ev.Type == "done" {
				if ev.FullText != "" {
					fullText.Reset()
					fullText.WriteString(ev.FullText)
					success = true
				}
			}
		}

		elapsedMs := time.Since(t0).Milliseconds()
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":       success,
			"reply":    fullText.String(),
			"thinking": fullThinking.String(),
			"error":    lastErr,
			"time_ms":  elapsedMs,
			"model":    req.Model,
		})
	})

	// API: Hide window to tray
	mux.HandleFunc("/api/hide", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.onHide != nil {
			s.onHide()
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	// API: Exit application completely
	mux.HandleFunc("/api/exit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		if s.onExit != nil {
			go s.onExit()
		}
	})

	// Favicon
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		candidates := []string{"app.ico", "../app.ico"}
		for _, c := range candidates {
			if data, err := os.ReadFile(c); err == nil {
				w.Header().Set("Content-Type", "image/x-icon")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(data)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	s.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// Log error if any
			if !strings.Contains(err.Error(), "Server closed") {
				fmt.Printf("Web server error: %v\n", err)
			}
		}
	}()

	return nil
}

// Stop gracefully closes the web server.
func (s *Server) Stop() {
	if s.server != nil {
		_ = s.server.Close()
	}
}

// Addr returns the local web address.
func (s *Server) Addr() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.port)
}
