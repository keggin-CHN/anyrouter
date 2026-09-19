package web

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"anyrouter/pkg/keeper"
)

func TestWebEndpoints(t *testing.T) {
	matrix := keeper.NewMatrix("", keeper.DefaultConfig())
	handler := NewServer(0, matrix).Handler()
	for _, path := range []string{"/", "/api/status", "/api/config", "/api/logs"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if path == "/" {
				if !strings.Contains(w.Body.String(), "AnyRouter") {
					t.Fatal("dashboard is missing")
				}
			} else {
				if !json.Valid(w.Body.Bytes()) {
					t.Fatal("response is not JSON")
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("sensitive API responses must not be cached")
				}
			}
		})
	}
}

func TestConfigEndpointValidatesAndPreservesStoppedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	matrix := keeper.NewMatrix(path, keeper.DefaultConfig())
	handler := NewServer(0, matrix).Handler()
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"malformed", "{", http.StatusBadRequest},
		{"null", "null", http.StatusBadRequest},
		{"negative interval", `{"check_interval_min":-1}`, http.StatusBadRequest},
		{"invalid proxy", `{"proxy":"not-a-proxy"}`, http.StatusBadRequest},
		{"oversize", strings.Repeat(" ", 64*1024) + "{}", http.StatusRequestEntityTooLarge},
		{"custom configuration", `{"api_keys":[" test-key ","test-key"],"tries_per_round":50}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/config", strings.NewReader(tc.body)))
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
			if matrix.IsRunning() {
				t.Fatal("save unexpectedly started workers")
			}
		})
	}
	loaded, err := keeper.LoadConfig(path)
	if err != nil || loaded.APIKey != "test-key" || len(loaded.APIKeys) != 1 || loaded.TriesPerRound != 50 {
		t.Fatalf("configuration was not normalized and saved: %v", err)
	}
}

func TestConfigEndpointReportsSaveFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	matrix := keeper.NewMatrix(filepath.Join(blocker, "config.json"), keeper.DefaultConfig())
	w := httptest.NewRecorder()
	NewServer(0, matrix).Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1/api/config", strings.NewReader(`{"tries_per_round":50}`)))
	if w.Code != http.StatusInternalServerError || matrix.GetConfig().TriesPerRound != 5 {
		t.Fatalf("save failure was hidden: status=%d", w.Code)
	}
}

func TestLocalAPIMethodAndOriginProtection(t *testing.T) {
	matrix := keeper.NewMatrix("", keeper.DefaultConfig())
	server := NewServer(0, matrix)
	called := false
	server.SetWindowControls(func() { called = true }, func() { called = true }, func() { called = true })
	handler := server.Handler()
	for _, path := range []string{"/api/start", "/api/stop", "/api/clear-logs", "/api/hide", "/api/show", "/api/exit", "/api/ping"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: %d", path, w.Code)
		}
	}
	for _, tc := range []struct{ target, origin string }{
		{"http://127.0.0.1/api/hide", "https://untrusted.example"},
		{"http://untrusted.example/api/config", ""},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, tc.target, strings.NewReader("{}"))
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("unsafe request status=%d", w.Code)
		}
	}
	if called {
		t.Fatal("unsafe requests invoked a desktop action")
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/show", nil)
	req.Header.Set("Origin", "http://127.0.0.1")
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !called {
		t.Fatal("same-origin desktop request should be allowed")
	}
}

func TestStartWithoutKeysFails(t *testing.T) {
	matrix := keeper.NewMatrix("", keeper.DefaultConfig())
	w := httptest.NewRecorder()
	NewServer(0, matrix).Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/start", nil))
	if w.Code != http.StatusBadRequest || matrix.IsRunning() {
		t.Fatal("empty key pool started")
	}
}

func TestServerReportsPortConflictAndSupportsEphemeralPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := NewServer(listener.Addr().(*net.TCPAddr).Port, keeper.NewMatrix("", keeper.DefaultConfig()))
	if err := server.Start(); err == nil {
		server.Stop()
		t.Fatal("port conflict must be returned by Start")
	}
	server = NewServer(0, keeper.NewMatrix("", keeper.DefaultConfig()))
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	resp, err := http.Get(server.Addr() + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("server is not ready: %d", resp.StatusCode)
	}
}

func TestEmbeddedDashboardMatchesSource(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "web", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n")), bytes.ReplaceAll(embedded, []byte("\r\n"), []byte("\n"))) {
		t.Fatal("standalone executable dashboard differs from the editable source")
	}
}
