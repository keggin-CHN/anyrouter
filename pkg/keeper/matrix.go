package keeper

import (
	"context"
	"fmt"
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
	mu         sync.RWMutex
	cfgPath    string
	cfg        *Config
	workers    map[string]*Worker
	states     map[string]ChannelState
	logs       []LogEntry
	maxLogs    int
	nextLogID  int64
	isRunning  bool
	ctx        context.Context
	cancelFunc context.CancelFunc

	listenersMu sync.Mutex
	listeners   map[chan ChannelState]struct{}
	logChans    map[chan LogEntry]struct{}
}

// NewMatrix initializes the manager with configuration.
func NewMatrix(cfgPath string, cfg *Config) *Matrix {
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
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.isRunning {
		return nil
	}

	m.ctx, m.cancelFunc = context.WithCancel(context.Background())
	m.isRunning = true

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
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.isRunning {
		return
	}

	m.addLog("", "🛑 正在停止所有通道守护线程...", "info")
	if m.cancelFunc != nil {
		m.cancelFunc()
	}

	for _, w := range m.workers {
		w.Stop()
	}
	m.workers = make(map[string]*Worker)
	m.isRunning = false
	m.addLog("", "⏹ 所有通道已停止", "info")
}

// Reload applies a new configuration and restarts workers.
func (m *Matrix) Reload(newCfg *Config) error {
	m.Stop()

	m.mu.Lock()
	m.cfg = newCfg
	m.mu.Unlock()

	_ = SaveConfig(m.cfgPath, newCfg)
	return m.Start()
}

// IsRunning returns whether matrix is currently active.
func (m *Matrix) IsRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.isRunning
}

// GetStates returns a snapshot of all channel states.
func (m *Matrix) GetStates() []ChannelState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]ChannelState, 0, len(m.states))
	for _, s := range m.states {
		list = append(list, s)
	}
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
	return m.cfg
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
	delete(m.listeners, ch)
	m.listenersMu.Unlock()
	close(ch)
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
	delete(m.logChans, ch)
	m.listenersMu.Unlock()
	close(ch)
}
