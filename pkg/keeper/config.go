package keeper

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var DefaultHeartbeatPrompts = []string{
	"1+1",
	"用一个字回答：好",
	"输出一个数字: 8",
	"2*3等于几",
	"Hi, reply 1 word.",
	"请回复'pong'",
	"3+5等于几",
	"ping",
	"100-1等于多少",
	"从1数到3",
}

var DefaultModels = []string{
	"gpt-6-astra",
	"claude-opus-4-8",
	"claude-fable-5-1",
	"gemini-2.5-pro",
}

type ModelInfo struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Desc string `json:"desc"`
}

var AvailableModelCandidates = []ModelInfo{
	{ID: "gpt-6-astra", Type: "Codex / Responses", Desc: "OpenAI Responses 协议 (核心推荐)"},
	{ID: "claude-opus-4-8", Type: "Claude Code", Desc: "Anthropic Claude Code 协议 (旗舰推荐)"},
	{ID: "claude-fable-5-1", Type: "Claude Code", Desc: "Anthropic Claude Code 协议 (极速推荐)"},
	{ID: "gemini-2.5-pro", Type: "OpenAI 标准", Desc: "Google Gemini 2.5 Pro 协议"},
}

// Config represents keeper_config.json structure.
type Config struct {
	mu                  sync.RWMutex `json:"-"`
	APIKeys             []string     `json:"api_keys"`
	APIKey              string       `json:"api_key"`
	Proxy               string       `json:"proxy"`
	CheckIntervalMin    int          `json:"check_interval_min"`
	RoundCooldownSec    int          `json:"round_cooldown_sec"`
	TriesPerRound       int          `json:"tries_per_round"`
	IntraRoundDelaySec  int          `json:"intra_round_delay_sec"`
	MaxRetries          int          `json:"max_retries"`
	RandomHeartbeat     bool         `json:"random_heartbeat"`
	HeartbeatPrompts    []string     `json:"heartbeat_prompts"`
	HeartbeatPrompt     string       `json:"heartbeat_prompt"`
	SelectedModels      []string     `json:"selected_models"`
	CloseBehavior       string       `json:"close_behavior"`
}

// DefaultConfig returns a preconfigured default configuration.
func DefaultConfig() *Config {
	return &Config{
		APIKeys: []string{
			"sk-BZMSVilf0BiRvEnvymd3KflwY1xr45wmXjw5Ewg2fsIkp9T2",
		},
		APIKey:             "sk-BZMSVilf0BiRvEnvymd3KflwY1xr45wmXjw5Ewg2fsIkp9T2",
		Proxy:              "http://127.0.0.1:10808",
		CheckIntervalMin:   30,
		RoundCooldownSec:   30,
		TriesPerRound:      5,
		IntraRoundDelaySec: 5,
		MaxRetries:         3,
		RandomHeartbeat:    true,
		HeartbeatPrompts:   append([]string(nil), DefaultHeartbeatPrompts...),
		HeartbeatPrompt:    "1+1",
		SelectedModels:     append([]string(nil), DefaultModels...),
		CloseBehavior:      "silent",
	}
}

// MaskKey formats a key for UI display (e.g. sk-BZMS...p9T2).
func MaskKey(key string) string {
	k := strings.TrimSpace(key)
	if len(k) <= 12 {
		return k
	}
	return fmt.Sprintf("%s...%s", k[:7], k[len(k)-4:])
}

// LoadConfig reads configuration from file, initializing defaults if missing.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		cfg := DefaultConfig()
		_ = SaveConfig(path, cfg)
		return cfg, nil
	} else if err != nil {
		return nil, fmt.Errorf("read config error: %w", err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config error: %w", err)
	}

	// Normalize
	if len(cfg.APIKeys) == 0 && cfg.APIKey != "" {
		cfg.APIKeys = []string{cfg.APIKey}
	} else if len(cfg.APIKeys) > 0 && cfg.APIKey == "" {
		cfg.APIKey = cfg.APIKeys[0]
	}

	cleanKeys := make([]string, 0, len(cfg.APIKeys))
	seen := make(map[string]bool)
	for _, k := range cfg.APIKeys {
		trimmed := strings.TrimSpace(k)
		if trimmed != "" && !seen[trimmed] {
			seen[trimmed] = true
			cleanKeys = append(cleanKeys, trimmed)
		}
	}
	cfg.APIKeys = cleanKeys

	if len(cfg.HeartbeatPrompts) == 0 {
		cfg.HeartbeatPrompts = append([]string(nil), DefaultHeartbeatPrompts...)
	}
	if len(cfg.SelectedModels) == 0 {
		cfg.SelectedModels = append([]string(nil), DefaultModels...)
	}

	return cfg, nil
}

// SaveConfig writes the configuration to the specified JSON path.
func SaveConfig(path string, cfg *Config) error {
	cfg.mu.RLock()
	defer cfg.mu.RUnlock()

	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config error: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}

// CheckProxyReachable checks if the local proxy port is reachable.
func CheckProxyReachable(proxyURL string) bool {
	proxy := strings.TrimSpace(proxyURL)
	if proxy == "" || proxy == "none" || proxy == "direct" {
		return true
	}
	parts := strings.Split(proxy, "//")
	addr := parts[len(parts)-1]
	if strings.Contains(addr, "/") {
		addr = strings.Split(addr, "/")[0]
	}
	conn, err := net.DialTimeout("tcp", addr, 1500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

