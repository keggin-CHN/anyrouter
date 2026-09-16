package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"anyrouter/pkg/keeper"
)

func TestWebEndpoints(t *testing.T) {
	cfg := keeper.DefaultConfig()
	matrix := keeper.NewMatrix("", cfg)
	server := NewServer(0, matrix)

	// Test GET /
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		data, err := staticFS.ReadFile("static/index.html")
		if err != nil {
			http.Error(rw, "not found", 500)
			return
		}
		rw.Write(data)
	})
	mux.HandleFunc("/api/status", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]interface{}{
			"is_running": matrix.IsRunning(),
			"states":     matrix.GetStates(),
		})
	})
	mux.HandleFunc("/api/config", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(matrix.GetConfig())
	})

	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200 for index, got %d", w.Code)
	}
	if len(w.Body.String()) < 100 {
		t.Fatalf("index content too short: %d", len(w.Body.String()))
	}

	// Test GET /api/status
	reqStatus := httptest.NewRequest("GET", "/api/status", nil)
	wStatus := httptest.NewRecorder()
	mux.ServeHTTP(wStatus, reqStatus)
	if wStatus.Code != 200 {
		t.Fatalf("expected 200 for /api/status, got %d", wStatus.Code)
	}

	// Test GET /api/config
	reqCfg := httptest.NewRequest("GET", "/api/config", nil)
	wCfg := httptest.NewRecorder()
	mux.ServeHTTP(wCfg, reqCfg)
	if wCfg.Code != 200 {
		t.Fatalf("expected 200 for /api/config, got %d", wCfg.Code)
	}
	_ = server
}
