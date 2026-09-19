package keeper

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaskKey(t *testing.T) {
	k := "sk-test-abcdefghijklmnopqrstuvwxyz1234"
	masked := MaskKey(k)
	if masked != "sk-test...1234" {
		t.Errorf("unexpected masked key: %s", masked)
	}

	short := "short"
	if MaskKey(short) != "short" {
		t.Errorf("short key should remain unchanged")
	}
}

func TestConfigLoadSave(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "test_config.json")

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("load default config failed: %v", err)
	}
	if len(cfg.APIKeys) != 0 || cfg.APIKey != "" {
		t.Fatalf("default config must not contain API credentials")
	}
	if len(cfg.HeartbeatPrompts) != 100 {
		t.Fatalf("expected 100 default prompts, got %d", len(cfg.HeartbeatPrompts))
	}

	cfg.CheckIntervalMin = 45
	if err := SaveConfig(cfgPath, cfg); err != nil {
		t.Fatalf("save config failed: %v", err)
	}

	cfg2, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("reload config failed: %v", err)
	}
	if cfg2.CheckIntervalMin != 45 {
		t.Fatalf("expected check interval 45, got %d", cfg2.CheckIntervalMin)
	}
}

func TestMatrixInit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.APIKeys = []string{"sk-test-key-1", "sk-test-key-2"}
	cfg.SelectedModels = []string{"gpt-6-astra"}

	m := NewMatrix("", cfg)
	if m.IsRunning() {
		t.Errorf("matrix should not be running initially")
	}

	logs := m.GetLogs(10)
	if len(logs) != 0 {
		t.Errorf("expected 0 logs initially")
	}
}

func TestExistingConfigCompatibility(t *testing.T) {
	// Read current workspace keeper_config.json
	realPath := filepath.Join("..", "..", "keeper_config.json")
	if _, err := os.Stat(realPath); err == nil {
		cfg, err := LoadConfig(realPath)
		if err != nil {
			t.Fatalf("failed to load real keeper_config.json: %v", err)
		}
		if len(cfg.APIKeys) == 0 {
			t.Errorf("no keys loaded from real config")
		}
		if len(cfg.SelectedModels) == 0 {
			t.Errorf("no models loaded from real config")
		}
	}
}

func TestGetStatesOrder(t *testing.T) {
	cfg := DefaultConfig()
	m := NewMatrix("", cfg)
	m.states["key2::modelA"] = ChannelState{TaskID: "key2::modelA", KeyLabel: "Key-2", ModelID: "modelA", Status: StatusSqueezing}
	m.states["key1::modelB"] = ChannelState{TaskID: "key1::modelB", KeyLabel: "Key-1", ModelID: "modelB", Status: StatusSqueezing}
	m.states["key2::modelB"] = ChannelState{TaskID: "key2::modelB", KeyLabel: "Key-2", ModelID: "modelB", Status: StatusAvailable}

	states := m.GetStates()
	if len(states) != 3 {
		t.Fatalf("expected 3 states, got %d", len(states))
	}
	// Available channel must be first
	if states[0].Status != StatusAvailable || states[0].TaskID != "key2::modelB" {
		t.Errorf("expected available channel to be first, got %+v", states[0])
	}
	// Key-1 should precede Key-2 for remaining squeezing channels
	if states[1].KeyLabel != "Key-1" {
		t.Errorf("expected Key-1 next, got %+v", states[1])
	}
}
