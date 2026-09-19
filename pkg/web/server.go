package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"anyrouter/pkg/client"
	"anyrouter/pkg/keeper"
)

//go:embed static/*
var staticFS embed.FS

// Server hosts the local web dashboard for AnyRouter Keeper.
type Server struct {
	mu         sync.Mutex
	controlsMu sync.RWMutex
	port       int
	matrix     *keeper.Matrix
	server     *http.Server
	onHide     func()
	onShow     func()
	onExit     func()
}

// SetWindowControls registers callbacks for desktop window control.
func (s *Server) SetWindowControls(onHide, onShow, onExit func()) {
	s.controlsMu.Lock()
	defer s.controlsMu.Unlock()
	s.onHide = onHide
	s.onShow = onShow
	s.onExit = onExit
}

// NewServer creates a web server instance.
func NewServer(port int, matrix *keeper.Matrix) *Server {
	if port < 0 {
		port = 28888
	}
	return &Server{
		port:   port,
		matrix: matrix,
	}
}

// Handler exposes the same routes used by the desktop server and HTTP tests.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Static page
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"is_running": s.matrix.IsRunning(),
			"states":     s.matrix.GetStates(),
			"models":     keeper.AvailableModelCandidates,
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	// API: Logs
	mux.HandleFunc("GET /api/logs", func(w http.ResponseWriter, r *http.Request) {
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
			newCfg := keeper.DefaultConfig()
			if !decodeJSON(w, r, newCfg) {
				return
			}
			newCfg.Normalize()
			if err := newCfg.Validate(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if s.matrix.IsRunning() && (len(newCfg.APIKeys) == 0 || len(newCfg.SelectedModels) == 0) {
				http.Error(w, "运行时至少需要一个 API Key 和一个模型", http.StatusBadRequest)
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
		if err := s.matrix.Start(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
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
	mux.HandleFunc("POST /api/clear-logs", func(w http.ResponseWriter, r *http.Request) {
		s.matrix.ClearLogs()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	// API: Proxy Check
	mux.HandleFunc("GET /api/proxy-check", func(w http.ResponseWriter, r *http.Request) {
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
		if !decodeJSON(w, r, &req) {
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
		defer c.CloseIdleConnections()

		t0 := time.Now()
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
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
	mux.HandleFunc("POST /api/hide", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if callback := s.windowControl("hide"); callback != nil {
			callback()
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	// API: Show/restore window from tray or external trigger
	mux.HandleFunc("POST /api/show", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if callback := s.windowControl("show"); callback != nil {
			callback()
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	// API: Exit application completely
	mux.HandleFunc("POST /api/exit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		if callback := s.windowControl("exit"); callback != nil {
			go callback()
		}
	})

	// Favicon
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
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

	protected := http.NewCrossOriginProtection().Handler(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if name, _, err := net.SplitHostPort(host); err == nil {
			host = name
		}
		if host != "localhost" && !net.ParseIP(host).IsLoopback() {
			http.Error(w, "Local requests only", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		protected.ServeHTTP(w, r)
	})
}

func (s *Server) windowControl(action string) func() {
	s.controlsMu.RLock()
	defer s.controlsMu.RUnlock()
	switch action {
	case "hide":
		return s.onHide
	case "show":
		return s.onShow
	default:
		return s.onExit
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
	if err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, err.Error(), status)
		return false
	}
	if bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		http.Error(w, "Expected a JSON object", http.StatusBadRequest)
		return false
	}
	if err := json.Unmarshal(body, target); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// Start binds the port before returning, so bind errors are reported to the caller.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		return fmt.Errorf("web server already started")
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if err != nil {
		return err
	}
	s.port = listener.Addr().(*net.TCPAddr).Port
	srv := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	s.server = srv

	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			// Log error if any
			if !strings.Contains(err.Error(), "Server closed") {
				fmt.Printf("Web server error: %v\n", err)
			}
		}
	}()

	return nil
}

// Stop closes the web server and cancels in-flight probe requests.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		_ = s.server.Close()
		s.server = nil
	}
}

// Addr returns the local web address.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("http://127.0.0.1:%d", s.port)
}
