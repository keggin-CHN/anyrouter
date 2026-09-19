package keeper

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LogEntry represents a single log line with timestamp and level.
type LogEntry struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	TaskID    string    `json:"task_id"`
	Text      string    `json:"text"`
	Level     string    `json:"level"` // info, warn, success, queue, heartbeat
}

// Matrix coordinates all Key x Model worker threads.
type Matrix struct {
	lifecycleMu sync.Mutex // Serialize start, stop and reload without blocking worker callbacks.
	mu          sync.RWMutex
	cfgPath     string
	cfg         *Config
	workers     map[string]*Worker
	states      map[string]ChannelState
	logs        []LogEntry
	maxLogs     int
	nextLogID   int64
	isRunning   bool
	ctx         context.Context
	cancelFunc  context.CancelFunc

	listenersMu sync.Mutex
	listeners   map[chan ChannelState]struct{}
	logChans    map[chan LogEntry]struct{}
}

// NewMatrix initializes the manager with configuration.
func NewMatrix(cfgPath string, cfg *Config) *Matrix {
	cfg = cfg.Clone()
	cfg.Normalize()
	return &Matrix{
		cfgPath:   cfgPath,
		cfg:       cfg,
		workers:   make(map[string]*Worker),
		states:    make(map[string]ChannelState),
		logs:      make([]LogEntry, 0, 500),
		maxLogs:   500,
		listeners: make(map[chan ChannelState]struct{}),
		logChans:  make(map[chan LogEntry]struct{}),
	}
}

// Start launches workers for all Key x Model combinations.
func (m *Matrix) Start() error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	return m.start()
}

func (m *Matrix) start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.isRunning {
		return nil
	}
	if err := m.cfg.Validate(); err != nil {
		return err
	}
	if len(m.cfg.APIKeys) == 0 || len(m.cfg.SelectedModels) == 0 {
		return fmt.Errorf("请至少配置一个 API Key 和一个模型")
	}

	m.ctx, m.cancelFunc = context.WithCancel(context.Background())
	m.isRunning = true
	m.states = make(map[string]ChannelState)

	keys := m.cfg.APIKeys
	if len(keys) == 0 && m.cfg.APIKey != "" {
		keys = []string{m.cfg.APIKey}
	}
	models := m.cfg.SelectedModels

	m.addLog("", fmt.Sprintf("🔥 启动多通道并发监控矩阵 (Keys: %d, 模型: %d, 总通道数: %d)",
		len(keys), len(models), len(keys)*len(models)), "info")

	for kIdx, key := range keys {
		keyLabel := fmt.Sprintf("Key-%d", kIdx+1)
		for _, modelID := range models {
			modelType := "Claude Code"
			for _, c := range AvailableModelCandidates {
				if c.ID == modelID {
					modelType = c.Type
					break
				}
			}

			w := NewWorker(
				key, keyLabel, modelID, modelType,
				m.cfg,
				m.onWorkerLog,
				m.onWorkerStatus,
			)

			taskID := fmt.Sprintf("%s::%s", keyLabel, modelID)
			m.workers[taskID] = w
			m.states[taskID] = w.state
			w.Start(m.ctx)
		}
	}

	return nil
}

// Stop safely shuts down all running workers.
func (m *Matrix) Stop() {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.stop()
}

func (m *Matrix) stop() {
	m.mu.Lock()

	if !m.isRunning {
		m.mu.Unlock()
		return
	}

	m.addLog("", "🛑 正在停止所有通道守护线程...", "info")
	if m.cancelFunc != nil {
		m.cancelFunc()
	}

	workers := m.workers
	m.mu.Unlock()
	// Workers publish their final state before exiting; never wait while holding mu.
	for _, w := range workers {
		w.Stop()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workers = make(map[string]*Worker)
	m.isRunning = false
	m.addLog("", "⏹ 所有通道已停止", "info")
}

// Reload persists a configuration and preserves whether the matrix was running.
func (m *Matrix) Reload(newCfg *Config) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	newCfg = newCfg.Clone()
	newCfg.Normalize()
	if err := newCfg.Validate(); err != nil {
		return err
	}
	wasRunning := m.IsRunning()
	if wasRunning && (len(newCfg.APIKeys) == 0 || len(newCfg.SelectedModels) == 0) {
		return fmt.Errorf("运行时至少需要一个 API Key 和一个模型")
	}
	if m.cfgPath != "" {
		if err := SaveConfig(m.cfgPath, newCfg); err != nil {
			return err
		}
	}
	m.stop()

	m.mu.Lock()
	m.cfg = newCfg
	m.states = make(map[string]ChannelState)
	m.mu.Unlock()

	if wasRunning {
		return m.start()
	}
	return nil
}

// IsRunning returns whether matrix is currently active.
func (m *Matrix) IsRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.isRunning
}

// GetStates returns a snapshot of all channel states, prioritizing available channels.
func (m *Matrix) GetStates() []ChannelState {
	m.mu.RLock()

	list := make([]ChannelState, 0, len(m.states))
	for _, s := range m.states {
		list = append(list, s)
	}
	m.mu.RUnlock()

	sort.SliceStable(list, func(i, j int) bool {
		// 1. Available channels first
		if (list[i].Status == StatusAvailable) != (list[j].Status == StatusAvailable) {
			return list[i].Status == StatusAvailable
		}
		// 2. KeyLabel order (Key-1, Key-2, ...)
		if list[i].KeyLabel != list[j].KeyLabel {
			iKey, iErr := strconv.Atoi(strings.TrimPrefix(list[i].KeyLabel, "Key-"))
			jKey, jErr := strconv.Atoi(strings.TrimPrefix(list[j].KeyLabel, "Key-"))
			if iErr == nil && jErr == nil && iKey != jKey {
				return iKey < jKey
			}
			return list[i].KeyLabel < list[j].KeyLabel
		}
		// 3. ModelID
		return list[i].ModelID < list[j].ModelID
	})

	return list
}

// ClearLogs empties the in-memory log buffer.
func (m *Matrix) ClearLogs() {
	m.mu.Lock()
	m.logs = make([]LogEntry, 0, m.maxLogs)
	m.mu.Unlock()
}

// GetLogs returns the most recent log entries.
func (m *Matrix) GetLogs(limit int) []LogEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if limit <= 0 || limit > len(m.logs) {
		limit = len(m.logs)
	}
	start := len(m.logs) - limit
	out := make([]LogEntry, limit)
	copy(out, m.logs[start:])
	return out
}

// GetConfig returns a thread-safe copy of the current configuration.
func (m *Matrix) GetConfig() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.Clone()
}

func (m *Matrix) addLog(taskID, text, level string) {
	m.nextLogID++
	entry := LogEntry{
		ID:        m.nextLogID,
		Timestamp: time.Now(),
		TaskID:    taskID,
		Text:      text,
		Level:     level,
	}

	if len(m.logs) >= m.maxLogs {
		m.logs = m.logs[1:]
	}
	m.logs = append(m.logs, entry)

	// Broadcast to log subscribers
	m.listenersMu.Lock()
	for ch := range m.logChans {
		select {
		case ch <- entry:
		default:
		}
	}
	m.listenersMu.Unlock()
}

func (m *Matrix) onWorkerLog(taskID, text, level string) {
	m.mu.Lock()
	m.addLog(taskID, text, level)
	m.mu.Unlock()
}

func (m *Matrix) onWorkerStatus(state ChannelState) {
	m.mu.Lock()
	m.states[state.TaskID] = state
	m.mu.Unlock()

	m.listenersMu.Lock()
	for ch := range m.listeners {
		select {
		case ch <- state:
		default:
		}
	}
	m.listenersMu.Unlock()
}

// SubscribeStatus registers a channel for live state updates.
func (m *Matrix) SubscribeStatus() chan ChannelState {
	ch := make(chan ChannelState, 64)
	m.listenersMu.Lock()
	m.listeners[ch] = struct{}{}
	m.listenersMu.Unlock()
	return ch
}

// UnsubscribeStatus unregisters a state update listener.
func (m *Matrix) UnsubscribeStatus(ch chan ChannelState) {
	m.listenersMu.Lock()
	defer m.listenersMu.Unlock()
	if _, ok := m.listeners[ch]; ok {
		delete(m.listeners, ch)
		close(ch)
	}
}

// SubscribeLogs registers a channel for live log entries.
func (m *Matrix) SubscribeLogs() chan LogEntry {
	ch := make(chan LogEntry, 64)
	m.listenersMu.Lock()
	m.logChans[ch] = struct{}{}
	m.listenersMu.Unlock()
	return ch
}

// UnsubscribeLogs unregisters a log listener.
func (m *Matrix) UnsubscribeLogs(ch chan LogEntry) {
	m.listenersMu.Lock()
	defer m.listenersMu.Unlock()
	if _, ok := m.logChans[ch]; ok {
		delete(m.logChans, ch)
		close(ch)
	}
}
