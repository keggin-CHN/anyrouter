package keeper

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaskKey(t *testing.T) {
	k := "sk-BZMSVilf0BiRvEnvymd3KflwY1xr45wmXjw5Ewg2fsIkp9T2"
	masked := MaskKey(k)
	if masked != "sk-BZMS...p9T2" {
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
	if len(cfg.APIKeys) == 0 {
		t.Fatalf("expected api keys in default config")
	}
	if len(cfg.HeartbeatPrompts) != 10 {
		t.Fatalf("expected 10 default prompts, got %d", len(cfg.HeartbeatPrompts))
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
