package keeper

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func localMatrix(t *testing.T, path string) *Matrix {
	t.Helper()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "offline test", http.StatusServiceUnavailable)
	}))
	t.Cleanup(proxy.Close)
	cfg := DefaultConfig()
	cfg.APIKeys = []string{"test-key-1", "test-key-2"}
	cfg.Proxy = proxy.URL
	m := NewMatrix(path, cfg)
	t.Cleanup(m.Stop)
	return m
}

func TestConfigSnapshotsAreIndependent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.APIKeys = []string{"test-key"}
	m := NewMatrix("", cfg)
	cfg.APIKeys[0] = "changed-input"
	snapshot := m.GetConfig()
	snapshot.APIKeys[0] = "changed-snapshot"
	snapshot.HeartbeatPrompts[0] = "changed-prompt"
	snapshot.SelectedModels[0] = "changed-model"
	actual := m.GetConfig()
	if actual.APIKeys[0] != "test-key" || actual.HeartbeatPrompts[0] != "1+1" || actual.SelectedModels[0] != DefaultModels[0] {
		t.Fatal("configuration escaped through a shared mutable slice")
	}
}

func TestReloadPreservesStoppedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m := localMatrix(t, path)
	cfg := m.GetConfig()
	cfg.TriesPerRound = 50 // Existing hand-edited configurations may exceed old UI limits.
	if err := m.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	if m.IsRunning() || len(m.GetStates()) != 0 {
		t.Fatal("saving a stopped matrix started workers")
	}
	loaded, err := LoadConfig(path)
	if err != nil || loaded.TriesPerRound != 50 {
		t.Fatalf("configuration was not persisted: %v", err)
	}
}

func TestReloadFailurePreservesRunningConfiguration(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	m := localMatrix(t, filepath.Join(blocker, "config.json"))
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	before := m.GetConfig()
	next := before.Clone()
	next.APIKeys = []string{"replacement"}
	if err := m.Reload(next); err == nil {
		t.Fatal("expected persistence failure")
	}
	if !m.IsRunning() || !reflect.DeepEqual(m.GetConfig(), before) {
		t.Fatal("failed save changed or stopped the running configuration")
	}
}

func TestStopWaitsAndReloadRemovesOldChannels(t *testing.T) {
	m := localMatrix(t, "")
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	m.Stop()
	for _, state := range m.GetStates() {
		if state.Status != StatusStopped {
			t.Fatalf("stop returned before workers exited: %s", state.Status)
		}
	}
	logs := m.GetLogs(0)
	if logs[len(logs)-1].Text != "⏹ 所有通道已停止" {
		t.Fatal("worker logs arrived after matrix shutdown")
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	next := m.GetConfig()
	next.APIKeys = []string{"new-key"}
	next.SelectedModels = []string{"gpt-6-astra"}
	if err := m.Reload(next); err != nil {
		t.Fatal(err)
	}
	states := m.GetStates()
	if !m.IsRunning() || len(states) != 1 || states[0].Key != "new-key" {
		t.Fatal("reload retained removed channels")
	}
}

func TestConcurrentLifecycleOperations(t *testing.T) {
	m := localMatrix(t, "")
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				if err := m.Start(); err != nil {
					t.Error(err)
					return
				}
				_ = m.GetStates()
				if err := m.Reload(m.GetConfig()); err != nil {
					t.Error(err)
					return
				}
				m.Stop()
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent lifecycle operations deadlocked")
	}
	m.Stop()
	for _, state := range m.GetStates() {
		if state.Status != StatusStopped {
			t.Fatalf("stale state after stop: %s", state.Status)
		}
	}
}

func TestLegacyConfigNormalization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(path, []byte(`{"api_key":" legacy-key ","tries_per_round":50}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil || cfg.APIKey != "legacy-key" || !reflect.DeepEqual(cfg.APIKeys, []string{"legacy-key"}) {
		t.Fatalf("legacy key configuration failed: %v", err)
	}
	cfg.APIKeys = []string{" new-key ", "new-key", ""}
	cfg.Normalize()
	if cfg.APIKey != "new-key" || len(cfg.APIKeys) != 1 {
		t.Fatal("key pool was not normalized")
	}
}

func TestAtomicSaveAndLoadFailures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := DefaultConfig()
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.CheckIntervalMin = 45
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || loaded.CheckIntervalMin != 45 {
		t.Fatalf("replacement failed: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary config files were not cleaned up")
	}
	if _, err := LoadConfig(filepath.Join(path, "missing.json")); err == nil {
		t.Fatal("initial save error must be reported")
	}
}

func TestUnsubscribeIsIdempotent(t *testing.T) {
	m := NewMatrix("", DefaultConfig())
	status, logs := m.SubscribeStatus(), m.SubscribeLogs()
	m.UnsubscribeStatus(status)
	m.UnsubscribeStatus(status)
	m.UnsubscribeLogs(logs)
	m.UnsubscribeLogs(logs)
}

func TestNumericKeyOrdering(t *testing.T) {
	m := NewMatrix("", DefaultConfig())
	for _, label := range []string{"Key-10", "Key-2", "Key-1"} {
		m.states[label] = ChannelState{KeyLabel: label, Status: StatusSqueezing}
	}
	states := m.GetStates()
	if states[0].KeyLabel != "Key-1" || states[1].KeyLabel != "Key-2" || states[2].KeyLabel != "Key-10" {
		t.Fatal("key labels are sorted lexicographically")
	}
}
