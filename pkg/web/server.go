package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

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
