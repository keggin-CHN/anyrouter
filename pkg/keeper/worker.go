package keeper

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"anyrouter/pkg/client"
)

const (
	StatusIdle      = "idle"
	StatusSqueezing = "squeezing" // 阶段一：用不了 (排队挤入中)
	StatusAvailable = "available" // 阶段二：能用 (测活巡航中)
	StatusStopped   = "stopped"   // 已停止
)

// ChannelState holds the live runtime snapshot for a single Key x Model channel.
type ChannelState struct {
	TaskID      string    `json:"task_id"`
	Key         string    `json:"key"`
	KeyMasked   string    `json:"key_masked"`
	KeyLabel    string    `json:"key_label"`
	ModelID     string    `json:"model_id"`
	ModelType   string    `json:"model_type"`
	Status      string    `json:"status"`
	StatusText  string    `json:"status_text"`
	LastPrompt  string    `json:"last_prompt"`
	LastReply   string    `json:"last_reply"`
	CheckCount  int       `json:"check_count"`
	Round       int       `json:"round"`
	Attempt     int       `json:"attempt"`
	MaxAttempts int       `json:"max_attempts"`
	CooldownSec int       `json:"cooldown_sec"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// LogCallback is a function that receives structured log lines.
type LogCallback func(taskID, text, level string)

// StatusCallback is called whenever a channel's status changes.
type StatusCallback func(state ChannelState)

// Worker runs the two-phase keep-alive state machine for a specific (Key, Model) pair.
type Worker struct {
	key                string
	keyLabel           string
	modelID            string
	modelType          string
	proxy              string
	checkIntervalSec   int
	roundCooldownSec   int
	triesPerRound      int
	intraRoundDelaySec int
	maxRetries         int
	heartbeatPrompts   []string
	randomHeartbeat    bool
	logCb              LogCallback
	statusCb           StatusCallback

	mu         sync.RWMutex
	state      ChannelState
	cancelFunc context.CancelFunc
}

// NewWorker initializes a channel worker.
func NewWorker(
	key, keyLabel, modelID, modelType string,
	cfg *Config,
	logCb LogCallback,
	statusCb StatusCallback,
) *Worker {
	checkSec := cfg.CheckIntervalMin * 60
	if checkSec < 60 {
		checkSec = 60
	}
	cooldownSec := cfg.RoundCooldownSec
	if cooldownSec < 5 {
		cooldownSec = 5
	}
	tries := cfg.TriesPerRound
	if tries < 1 {
		tries = 5
	}
	delaySec := cfg.IntraRoundDelaySec
	if delaySec < 1 {
		delaySec = 5
	}
	retries := cfg.MaxRetries
	if retries < 1 {
		retries = 3
	}

	taskID := fmt.Sprintf("%s::%s", keyLabel, modelID)

	w := &Worker{
		key:                key,
		keyLabel:           keyLabel,
		modelID:            modelID,
		modelType:          modelType,
		proxy:              cfg.Proxy,
		checkIntervalSec:   checkSec,
		roundCooldownSec:   cooldownSec,
		triesPerRound:      tries,
		intraRoundDelaySec: delaySec,
		maxRetries:         retries,
		heartbeatPrompts:   cfg.HeartbeatPrompts,
		randomHeartbeat:    cfg.RandomHeartbeat,
		logCb:              logCb,
		statusCb:           statusCb,
		state: ChannelState{
			TaskID:      taskID,
			Key:         key,
			KeyMasked:   MaskKey(key),
			KeyLabel:    keyLabel,
			ModelID:     modelID,
			ModelType:   modelType,
			Status:      StatusIdle,
			StatusText:  "⚪ 就绪待命",
			MaxAttempts: tries,
			UpdatedAt:   time.Now(),
		},
	}
	return w
}

func (w *Worker) log(text, level string) {
	if w.logCb != nil {
		w.logCb(w.state.TaskID, fmt.Sprintf("[%s | %s] %s", w.keyLabel, w.modelID, text), level)
	}
}

func (w *Worker) updateStatus(status, statusText, prompt, reply string, round, attempt, cooldown int) {
	w.mu.Lock()
	w.state.Status = status
	w.state.StatusText = statusText
	if prompt != "" {
		w.state.LastPrompt = prompt
	}
	if reply != "" {
		w.state.LastReply = reply
	}
	w.state.Round = round
	w.state.Attempt = attempt
	w.state.CooldownSec = cooldown
	w.state.UpdatedAt = time.Now()
	snapshot := w.state
	w.mu.Unlock()

	if w.statusCb != nil {
		w.statusCb(snapshot)
	}
}

func (w *Worker) pickPrompt() string {
	if len(w.heartbeatPrompts) == 0 {
		return "1+1"
	}
	if w.randomHeartbeat {
		idx := rand.IntN(len(w.heartbeatPrompts))
		return w.heartbeatPrompts[idx]
	}
	return w.heartbeatPrompts[0]
}

func (w *Worker) makeClient(maxRetries int) (*client.AnyRouterClient, error) {
	return client.NewClient(client.ClientConfig{
		APIKey:     w.key,
		Proxy:      w.proxy,
		MaxRetries: &maxRetries,
		Timeout:    60 * time.Second,
	})
}

// Start runs the worker asynchronously.
func (w *Worker) Start(parentCtx context.Context) {
	ctx, cancel := context.WithCancel(parentCtx)
	w.cancelFunc = cancel

	go func() {
		w.log(
			fmt.Sprintf("🚀 启动守护 | 阶段一(用不了: %ds一轮/%d次) -> 阶段二(能用: %d分钟测活) | 题库量: %d",
				w.roundCooldownSec, w.triesPerRound, w.checkIntervalSec/60, len(w.heartbeatPrompts)),
			"info",
		)

		for {
			select {
			case <-ctx.Done():
				w.updateStatus(StatusStopped, "⚪ 已安全停止", "", "", 0, 0, 0)
				w.log("⏹ 任务已安全停止", "info")
				return
			default:
			}

			// =======================================================
			// 阶段一：【用不了】 循环排队挤入
			// =======================================================
			squeezed := w.squeezeIn(ctx)
			if !squeezed {
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
					continue
				}
			}

			// =======================================================
			// 挤入成功！触发右下角弹窗通知
			// =======================================================
			w.mu.Lock()
			lastPrompt := w.state.LastPrompt
			lastReply := w.state.LastReply
			w.mu.Unlock()

			title := fmt.Sprintf("[%s] %s 可用！", w.keyLabel, w.modelID)
			msg := fmt.Sprintf("🎉 挤入提问: '%s' -> 回复: '%s'\n已进入【能用】状态，开启 %d 分钟定期测活。",
				lastPrompt, truncateStr(lastReply, 30), w.checkIntervalSec/60)
			NotifyDesktop(title, msg)
			w.log(fmt.Sprintf("★★★ 挤入成功！提问: '%s' -> 响应: '%s' -> 通道已进入能用巡航状态 ★★★", lastPrompt, lastReply), "success")

			// =======================================================
			// 阶段二：【能用】 测活巡航（能用就不管）
			// =======================================================
			cruiseBroken := false
			for !cruiseBroken {
				select {
				case <-ctx.Done():
					return
				default:
				}

				// 倒计时循环（逐秒跳秒显示）
				slept := 0
				for slept < w.checkIntervalSec {
					select {
					case <-ctx.Done():
						return
					default:
					}

					remaining := w.checkIntervalSec - slept
					remMin := remaining / 60
					remSec := remaining % 60
					timeStr := fmt.Sprintf("%d分%02d秒", remMin, remSec)
					if remMin == 0 {
						timeStr = fmt.Sprintf("%d秒", remSec)
					}

					w.mu.RLock()
					count := w.state.CheckCount
					w.mu.RUnlock()

					w.updateStatus(
						StatusAvailable,
						fmt.Sprintf("🟢 能用 (距测活: %s | 已保活 %d 次)", timeStr, count),
						"", "", 0, 0, remaining,
					)

					select {
					case <-ctx.Done():
						return
					case <-time.After(1 * time.Second):
						slept++
					}
				}

				// 到期执行测活 Ping
				testPrompt := w.pickPrompt()
				w.log(fmt.Sprintf("🔍 触发 %d 分钟定时测活 (随机提问: '%s')...", w.checkIntervalSec/60, testPrompt), "heartbeat")
				w.updateStatus(StatusAvailable, "🟢 正在执行测活 Ping...", testPrompt, "", 0, 0, 0)

				pingOK, reply := w.sendHeartbeat(ctx, testPrompt)
				if pingOK {
					w.mu.Lock()
					w.state.CheckCount++
					w.mu.Unlock()
					w.log(fmt.Sprintf("✅ 测活通过 (提问: '%s' -> 响应: '%s'，能用就不管，重置倒计时)", testPrompt, reply), "success")
				} else {
					w.log("⚠️ 测活失败或通道失效！模型不可用，立即重新进入阶段一排队挤入...", "warn")
					cruiseBroken = true
					break
				}
			}
		}
	}()
}

// Stop terminates the worker goroutine.
func (w *Worker) Stop() {
	if w.cancelFunc != nil {
		w.cancelFunc()
	}
}

func (w *Worker) squeezeIn(ctx context.Context) bool {
	attempt := 0

	for {
		select {
		case <-ctx.Done():
			return false
		default:
		}

		attempt++
		roundNum := ((attempt - 1) / w.triesPerRound) + 1
		attemptInRound := ((attempt - 1) % w.triesPerRound) + 1
		isRoundEnd := (attemptInRound == w.triesPerRound)

		prompt := w.pickPrompt()
		w.updateStatus(
			StatusSqueezing,
			fmt.Sprintf("🟡 用不了 (第 %d 轮 #%d/%d)", roundNum, attemptInRound, w.triesPerRound),
			prompt, "", roundNum, attemptInRound, 0,
		)
		w.log(fmt.Sprintf("[第 %d 轮 #%d/%d] 正在尝试挤入 (随机提问: '%s')...", roundNum, attemptInRound, w.triesPerRound, prompt), "queue")

		hasContent := false
		var fullReply strings.Builder
		failureReason := ""

		c, err := w.makeClient(1) // 单次尝试，由外层精确控制轮次与题库
		if err != nil {
			failureReason = err.Error()
		} else {
			events, streamErr := c.StreamChat(ctx, w.modelID, prompt, nil, "", "", 64)
			if streamErr != nil {
				failureReason = streamErr.Error()
			} else {
				for event := range events {
					select {
					case <-ctx.Done():
						return false
					default:
					}

					if event.Type == "text" || event.Type == "thinking" {
						if event.Delta != "" {
							hasContent = true
							if event.Type == "text" {
								fullReply.WriteString(event.Delta)
							}
						}
					} else if event.Type == "status" && event.Stage == "retrying" {
						failureReason = event.Reason
					} else if event.Type == "stream_error" {
						failureReason = event.Reason
					}
				}
			}
		}

		if hasContent {
			replyText := strings.TrimSpace(strings.ReplaceAll(fullReply.String(), "\n", " "))
			w.updateStatus(StatusAvailable, "🟢 刚刚挤入成功", prompt, replyText, roundNum, attemptInRound, 0)
			return true
		}

		if failureReason != "" {
			cleanReason := failureReason
			if idx := strings.Index(cleanReason, "最后报错: "); idx != -1 {
				cleanReason = cleanReason[idx+len("最后报错: "):]
			}
			w.log(fmt.Sprintf("[第 %d 轮 #%d/%d] 排队未挤上: %s", roundNum, attemptInRound, w.triesPerRound, truncateStr(cleanReason, 120)), "warn")
		}

		// 轮次冷却与间隔等待
		if isRoundEnd {
			w.log(fmt.Sprintf("第 %d 轮 (%d/%d) 结束未挤上，等待冷却 %ds 进入第 %d 轮...",
				roundNum, w.triesPerRound, w.triesPerRound, w.roundCooldownSec, roundNum+1), "queue")

			slept := 0
			for slept < w.roundCooldownSec {
				select {
				case <-ctx.Done():
					return false
				default:
				}

				rem := w.roundCooldownSec - slept
				w.updateStatus(
					StatusSqueezing,
					fmt.Sprintf("🟡 轮末冷却中 (%ds 后开启第 %d 轮)", rem, roundNum+1),
					prompt, "", roundNum, attemptInRound, rem,
				)

				select {
				case <-ctx.Done():
					return false
				case <-time.After(1 * time.Second):
					slept++
				}
			}
		} else {
			slept := 0
			for slept < w.intraRoundDelaySec {
				select {
				case <-ctx.Done():
					return false
				default:
				}
				select {
				case <-ctx.Done():
					return false
				case <-time.After(1 * time.Second):
					slept++
				}
			}
		}
	}
}

func (w *Worker) sendHeartbeat(ctx context.Context, prompt string) (bool, string) {
	c, err := w.makeClient(w.maxRetries)
	if err != nil {
		w.log(fmt.Sprintf("❌ 测活初始化客户端报错: %v", err), "warn")
		return false, ""
	}

	start := time.Now()
	var reply strings.Builder
	hasContent := false

	events, streamErr := c.StreamChat(ctx, w.modelID, prompt, nil, "", "", 32)
	if streamErr != nil {
		w.log(fmt.Sprintf("❌ 测活请求报错: %v", streamErr), "warn")
		return false, ""
	}

	for event := range events {
		select {
		case <-ctx.Done():
			return false, ""
		default:
		}

		if event.Type == "text" && event.Delta != "" {
			hasContent = true
			reply.WriteString(event.Delta)
		} else if event.Type == "status" && event.Stage == "retrying" {
			w.log(fmt.Sprintf("测活等待中: %s", truncateStr(event.Reason, 35)), "queue")
		}
	}

	costMs := time.Since(start).Milliseconds()
	replyStr := strings.TrimSpace(strings.ReplaceAll(reply.String(), "\n", " "))
	if hasContent {
		w.updateStatus(StatusAvailable, "🟢 刚刚测活成功", prompt, replyStr, 0, 0, 0)
		w.log(fmt.Sprintf("✅ 测活成功 (%dms) -> 提问: '%s' | 回复: %s", costMs, prompt, replyStr), "success")
		return true, replyStr
	}

	w.log("❌ 测活建立连接但未收到有效回复", "warn")
	return false, ""
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
