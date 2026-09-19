package tests

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"anyrouter/pkg/client"
	"anyrouter/pkg/keeper"
	"anyrouter/pkg/web"
)

func testConfig(t *testing.T) *keeper.Config {
	t.Helper()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "test proxy: upstream unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(proxy.Close)
	cfg := keeper.DefaultConfig()
	cfg.APIKeys = []string{"sk-test-key"}
	cfg.Proxy = proxy.URL
	return cfg
}

func TestFullE2EFlow(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "test_keeper_config.json")

	// 1. 测试配置保存与多 Key 去重
	cfg := testConfig(t)
	cfg.APIKeys = []string{
		"sk-key1111111111111111111111111111111111111111111111111",
		"sk-key2222222222222222222222222222222222222222222222222",
		"sk-key1111111111111111111111111111111111111111111111111", // duplicate
	}
	cfg.SelectedModels = []string{"gpt-6-astra", "claude-fable-5-1"}
	cfg.TriesPerRound = 3
	cfg.IntraRoundDelaySec = 1
	cfg.RoundCooldownSec = 5

	if err := keeper.SaveConfig(cfgPath, cfg); err != nil {
		t.Fatalf("save config failed: %v", err)
	}

	loadedCfg, err := keeper.LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("load config failed: %v", err)
	}
	if len(loadedCfg.APIKeys) != 2 {
		t.Fatalf("expected 2 unique keys after deduplication, got %d", len(loadedCfg.APIKeys))
	}

	// 2. 启动 Matrix 管理池
	matrix := keeper.NewMatrix(cfgPath, loadedCfg)
	t.Cleanup(matrix.Stop)
	if matrix.IsRunning() {
		t.Fatalf("matrix should not be running yet")
	}

	// 启动保活通道 (2 keys x 2 models = 4 通道)
	if err := matrix.Start(); err != nil {
		t.Fatalf("matrix start failed: %v", err)
	}
	if !matrix.IsRunning() {
		t.Fatalf("matrix should be running")
	}

	states := matrix.GetStates()
	if len(states) != 4 {
		t.Fatalf("expected 4 channel states (2 keys * 2 models), got %d", len(states))
	}

	// 验证各通道字段完整性
	for _, s := range states {
		if s.KeyLabel == "" || s.KeyMasked == "" || s.ModelID == "" {
			t.Fatalf("channel state missing key info: %+v", s)
		}
		if s.Status != keeper.StatusSqueezing && s.Status != keeper.StatusIdle {
			t.Fatalf("unexpected initial channel status: %s", s.Status)
		}
	}

	// 3. 测试日志记录与清空
	logs := matrix.GetLogs(50)
	if len(logs) == 0 {
		t.Fatalf("expected matrix to have produced initial logs")
	}

	// Stop producers before asserting an empty log buffer.
	matrix.Stop()
	matrix.ClearLogs()
	if len(matrix.GetLogs(50)) != 0 {
		t.Fatalf("expected 0 logs after ClearLogs")
	}

	// 4. 测试代理连通性检测函数
	if !keeper.CheckProxyReachable(cfg.Proxy) {
		t.Fatal("local test proxy should be reachable")
	}

	// 5. 测试 Windows 桌面通知函数 (非阻塞调用，确保不抛 panic)
	keeper.NotifyDesktop("测试通知", "这是一个自动化功能测试通知")

	// 6. 停止 Matrix
	matrix.Stop()
	if matrix.IsRunning() {
		t.Fatalf("matrix should be stopped")
	}
}

func TestWebAPIServerFlow(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "web_test_config.json")
	cfg := testConfig(t)
	if err := keeper.SaveConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	matrix := keeper.NewMatrix(cfgPath, cfg)
	t.Cleanup(matrix.Stop)
	server := web.NewServer(0, matrix)
	if err := server.Start(); err != nil {
		t.Fatalf("web server start error: %v", err)
	}
	defer server.Stop()

	baseURL := server.Addr()

	// 1. GET /
	resp, err := http.Get(baseURL + "/")
	if err != nil {
		t.Fatalf("GET / error: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(body, []byte("AnyRouter")) {
		t.Fatalf("unexpected index response: %d, body: %s", resp.StatusCode, string(body))
	}

	// 2. GET /api/status
	respStatus, err := http.Get(baseURL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status error: %v", err)
	}
	var statusData map[string]interface{}
	_ = json.NewDecoder(respStatus.Body).Decode(&statusData)
	respStatus.Body.Close()
	if statusData["is_running"] != false {
		t.Fatalf("expected is_running=false initially")
	}

	// 3. POST /api/start
	respStart, err := http.Post(baseURL+"/api/start", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/start error: %v", err)
	}
	respStart.Body.Close()
	if !matrix.IsRunning() {
		t.Fatalf("matrix should be running after /api/start")
	}

	// 4. GET /api/logs
	respLogs, err := http.Get(baseURL + "/api/logs")
	if err != nil {
		t.Fatalf("GET /api/logs error: %v", err)
	}
	var logs []keeper.LogEntry
	_ = json.NewDecoder(respLogs.Body).Decode(&logs)
	respLogs.Body.Close()
	if len(logs) == 0 {
		t.Fatalf("expected logs after start")
	}

	// 5. POST /api/clear-logs
	respClear, err := http.Post(baseURL+"/api/clear-logs", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/clear-logs error: %v", err)
	}
	respClear.Body.Close()

	// 6. POST /api/stop
	respStop, err := http.Post(baseURL+"/api/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/stop error: %v", err)
	}
	respStop.Body.Close()
	if matrix.IsRunning() {
		t.Fatalf("matrix should be stopped after /api/stop")
	}

	// 7. GET /api/proxy-check
	respProxy, err := http.Get(baseURL + "/api/proxy-check")
	if err != nil {
		t.Fatalf("GET /api/proxy-check error: %v", err)
	}
	var proxyData map[string]interface{}
	_ = json.NewDecoder(respProxy.Body).Decode(&proxyData)
	respProxy.Body.Close()
	if _, ok := proxyData["ok"]; !ok {
		t.Fatalf("proxy-check response missing ok field")
	}

	// 8. POST /api/hide and /api/show
	var hideTriggered, showTriggered atomic.Bool
	server.SetWindowControls(func() {
		hideTriggered.Store(true)
	}, func() {
		showTriggered.Store(true)
	}, func() {})

	respHide, err := http.Post(baseURL+"/api/hide", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/hide error: %v", err)
	}
	respHide.Body.Close()
	if !hideTriggered.Load() {
		t.Fatalf("expected hide callback to be triggered")
	}

	respShow, err := http.Post(baseURL+"/api/show", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/show error: %v", err)
	}
	respShow.Body.Close()
	if !showTriggered.Load() {
		t.Fatalf("expected show callback to be triggered")
	}
}

func TestCoreModelsExclusiveList(t *testing.T) {
	expected := []string{"gpt-6-astra", "claude-opus-4-8", "claude-fable-5-1"}
	if len(keeper.AvailableModelCandidates) != 3 {
		t.Fatalf("expected exactly 3 available models, got %d", len(keeper.AvailableModelCandidates))
	}
	for i, m := range keeper.AvailableModelCandidates {
		if m.ID != expected[i] {
			t.Fatalf("model [%d] mismatch: expected %s, got %s", i, expected[i], m.ID)
		}
	}

	cfg := keeper.DefaultConfig()
	if cfg.CloseBehavior != "silent" {
		t.Fatalf("expected default CloseBehavior='silent', got %s", cfg.CloseBehavior)
	}
	if len(cfg.SelectedModels) != 3 {
		t.Fatalf("expected 3 selected models, got %d", len(cfg.SelectedModels))
	}
}

func TestClientDeepMasqueradeVerification(t *testing.T) {
	_, err := client.NewClient(client.ClientConfig{
		APIKey: "sk-test-fake-key",
	})
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	// 验证 3 大核心模型各自的协议体系与深度伪装
	if client.DetectProtocol("gpt-6-astra") != client.ProtocolCodex {
		t.Fatalf("gpt-6-astra should be codex protocol")
	}
	if client.DetectProtocol("claude-opus-4-8") != client.ProtocolClaude {
		t.Fatalf("claude-opus-4-8 should be claude protocol")
	}
	if client.DetectProtocol("claude-fable-5-1") != client.ProtocolClaude {
		t.Fatalf("claude-fable-5-1 should be claude protocol")
	}

	// 验证 100 道备用题库存在且完备
	if len(keeper.DefaultHeartbeatPrompts) != 100 {
		t.Fatalf("expected 100 default prompts, got %d", len(keeper.DefaultHeartbeatPrompts))
	}
}
