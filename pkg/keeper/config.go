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
	// 极简算术与数字运算 (20)
	"1+1",
	"2*3等于几",
	"3+5等于几",
	"100-1等于多少",
	"从1数到3",
	"输出一个数字: 8",
	"99+1等于几",
	"50除以2是多少",
	"7*8等于几",
	"10的平方是多少",
	"计算：12+34",
	"15减7等于多少",
	"4乘以6等于多少",
	"81的算术平方根是多少",
	"倒数：3, 2, 1",
	"偶数2后面的下一个偶数是几",
	"10进制的15转16进制是多少",
	"0加任何数等于什么",
	"2的3次方是多少",
	"7加8等于多少",

	// 单字与极短文本指令 (20)
	"用一个字回答：好",
	"Hi, reply 1 word.",
	"请回复'pong'",
	"ping",
	"请只回复数字1",
	"回复一个大写字母A",
	"回复一个英文标点句号",
	"Say 'hello'",
	"Reply with exactly 'OK'",
	"用一个汉字回答：在",
	"请输出：收到",
	"输出两个字：正常",
	"请说：ready",
	"Reply with 'YES'",
	"请回答单个汉字：行",
	"输出：alive",
	"Reply 'ack'",
	"只输出感叹号！",
	"回复：done",
	"请说一个字：通",

	// 基础常识与事实判断 (20)
	"太阳从哪边升起",
	"水的化学分子式是什么",
	"一周有几天",
	"一年有几个季度",
	"中国的首都是哪里",
	"地球是圆的吗？回答是或否",
	"雪是什么颜色的",
	"白天的天空通常是什么颜色",
	"一分钟有多少秒",
	"冰融化后变成什么",
	"人类有几只眼睛",
	"大熊猫的主食是什么",
	"猫属于犬科还是猫科",
	"月亮是行星还是卫星",
	"彩虹有几种常见颜色",
	"氧气的化学符号是什么",
	"三角形有几个内角",
	"光速快还是声速快",
	"一小时等于多少分钟",
	"树叶在秋天通常会变成什么颜色",

	// 极短语言转换与词汇 (15)
	"把'apple'翻译成中文",
	"把'你好'翻译成英文",
	"'cat'是什么动物",
	"'sun'对应的中文是什么",
	"Translate 'water' to Chinese.",
	"'thank you'用中文怎么说",
	"将'red'翻译成中文",
	"'book'的中文释义是什么",
	"Translate 'dog' into Chinese.",
	"'moon'翻译成中文",
	"把'早上好'翻译成英语",
	"'star'在中文里是什么意思",
	"'green'代表哪种颜色",
	"Translate 'time' to Chinese.",
	"'tree'的中文是什么",

	// 编程、符号与极客交互 (15)
	"print('hello')在Python中会输出什么",
	"布尔值true的反面是什么",
	"JSON格式中表示数组用什么括号",
	"HTTP状态码200代表什么含义",
	"写出HTML中表示段落的标签",
	"二进制101转换成十进制是多少",
	"SQL中用于查询数据的关键词是什么",
	"echo 'test' 输出什么",
	"Git中提交更改的命令是什么",
	"Linux中查看当前目录的命令是什么",
	"Python中用于定义函数的关键字是什么",
	"CSS中设置文字颜色的属性是什么",
	"404状态码通常表示什么",
	"Markdown中表示一级标题使用什么符号",
	"写出一个空JSON对象的符号",

	// 逻辑与短语推理 (10)
	"如果A大于B，B大于C，A和C谁大",
	"昨天是星期三，今天星期几",
	"桌上有3个苹果，拿走2个，还剩几个",
	"黑的反义词是什么",
	"高和矮哪一个是高的反义词",
	"大象和蚂蚁谁体型大",
	"明天之后的那一天叫什么",
	"火是热的还是冷的",
	"石头能浮在普通水面上吗",
	"用三个字评价今天的天气",
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

