package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var retryableStatusCodes = map[int]bool{
	408: true, 409: true, 429: true,
	500: true, 502: true, 503: true, 504: true,
	520: true, 522: true, 524: true,
}

// AnyRouterClient is the dedicated client for anyrouter.top.
type AnyRouterClient struct {
	apiKey              string
	baseURL             string
	proxy               string
	maxRetries          *int
	attemptsPerRound    int
	intraRoundDelay     time.Duration
	interRoundDelay     time.Duration
	timeout             time.Duration
	httpClient          *http.Client
	claudeDeviceID      string
	codexInstallationID string
}

// NewClient initializes a new AnyRouterClient.
func NewClient(cfg ClientConfig) (*AnyRouterClient, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		apiKey = os.Getenv("ANYROUTER_API_KEY")
	}
	if apiKey == "" {
		apiKey = "sk-BZMSVilf0BiRvEnvymd3KflwY1xr45wmXjw5Ewg2fsIkp9T2"
	}

	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = os.Getenv("ANYROUTER_BASE_URL")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	proxy := strings.TrimSpace(cfg.Proxy)
	if proxy == "" && cfg.Proxy == "" {
		// default proxy unless explicitly set to "" or disabled
		proxy = DefaultProxy
	}

	attemptsPerRound := cfg.AttemptsPerRound
	if attemptsPerRound <= 0 {
		attemptsPerRound = 5
	}

	intraRoundDelay := cfg.IntraRoundDelay
	if intraRoundDelay <= 0 {
		intraRoundDelay = 5 * time.Second
	}

	interRoundDelay := cfg.InterRoundDelay
	if interRoundDelay <= 0 {
		interRoundDelay = 30 * time.Second
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}

	transport := &http.Transport{
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
	}

	if proxy != "" && proxy != "none" && proxy != "direct" {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy URL: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	client := &AnyRouterClient{
		apiKey:              apiKey,
		baseURL:             baseURL,
		proxy:               proxy,
		maxRetries:          cfg.MaxRetries,
		attemptsPerRound:    attemptsPerRound,
		intraRoundDelay:     intraRoundDelay,
		interRoundDelay:     interRoundDelay,
		timeout:             timeout,
		httpClient:          &http.Client{Transport: transport, Timeout: 0}, // streaming timeout handled by context/read
		claudeDeviceID:      RandomHex(32),
		codexInstallationID: NewUUID(),
	}

	return client, nil
}

// StreamChat sends a prompt or conversation history and yields streaming events over a channel.
func (c *AnyRouterClient) StreamChat(
	ctx context.Context,
	model string,
	prompt string,
	history []Message,
	systemPrompt string,
	sessionID string,
	maxTokens int,
) (<-chan StreamEvent, error) {
	if sessionID == "" {
		sessionID = NewUUID()
	}
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	messages := make([]Message, len(history))
	copy(messages, history)
	if prompt != "" {
		messages = append(messages, Message{Role: "user", Content: prompt})
	}

	protocol := DetectProtocol(model)
	var endpoint string
	switch protocol {
	case ProtocolCodex:
		endpoint = fmt.Sprintf("%s/v1/responses", c.baseURL)
	case ProtocolClaude:
		endpoint = fmt.Sprintf("%s/v1/messages?beta=true", c.baseURL)
	case ProtocolOpenAI:
		endpoint = fmt.Sprintf("%s/v1/chat/completions", c.baseURL)
	}

	out := make(chan StreamEvent, 64)

	go func() {
		defer close(out)
		attempt := 0

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			attempt++
			roundNum := ((attempt - 1) / c.attemptsPerRound) + 1
			attemptInRound := ((attempt - 1) % c.attemptsPerRound) + 1
			isRoundEnd := (attemptInRound == c.attemptsPerRound)
			turnID := NewUUID()

			var headers http.Header
			var bodyMap map[string]interface{}

			switch protocol {
			case ProtocolCodex:
				h, clientMeta := c.createCodexHeaders(sessionID, turnID)
				headers = h
				bodyMap = c.buildCodexBody(model, messages, systemPrompt, sessionID, turnID, clientMeta, maxTokens, "medium")
			case ProtocolClaude:
				headers = c.createClaudeHeaders(sessionID, 0)
				bodyMap = c.buildClaudeBody(model, messages, systemPrompt, sessionID, maxTokens)
			case ProtocolOpenAI:
				headers = c.createOpenAIHeaders(sessionID)
				bodyMap = c.buildOpenAIBody(model, messages, systemPrompt, maxTokens)
			}

			bodyBytes, err := json.Marshal(bodyMap)
			if err != nil {
				out <- StreamEvent{Type: "stream_error", Err: err, Reason: err.Error()}
				return
			}

			out <- StreamEvent{
				Type:           "status",
				Stage:          "connecting",
				Round:          roundNum,
				AttemptInRound: attemptInRound,
				MaxInRound:     c.attemptsPerRound,
				Attempt:        attempt,
				Message:        fmt.Sprintf("正在发起连接 (第 %d 轮 #%d/%d, 模型: %s)...", roundNum, attemptInRound, c.attemptsPerRound, model),
			}

			retryReason := ""
			statusCode := 0
			var retryAfterSec float64 = 0

			req, reqErr := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(bodyBytes))
			if reqErr != nil {
				out <- StreamEvent{Type: "stream_error", Err: reqErr, Reason: reqErr.Error()}
				return
			}
			req.Header = headers

			resp, err := c.httpClient.Do(req)
			if err != nil {
				retryReason = fmt.Sprintf("网络连接波动: %v", err)
			} else {
				statusCode = resp.StatusCode
				if ra := resp.Header.Get("Retry-After"); ra != "" {
					if val, parseErr := strconv.ParseFloat(ra, 64); parseErr == nil {
						retryAfterSec = val
					}
				}

				if statusCode == 200 {
					sseChan := make(chan StreamEvent, 32)
					go func() {
						defer resp.Body.Close()
						switch protocol {
						case ProtocolCodex:
							parseCodexSSE(resp.Body, sseChan)
						case ProtocolClaude:
							parseClaudeSSE(resp.Body, sseChan)
						case ProtocolOpenAI:
							parseOpenAISSE(resp.Body, sseChan)
						}
						close(sseChan)
					}()

					hasContent := false
					for event := range sseChan {
						if event.Type == "stream_error" {
							retryReason = fmt.Sprintf("流内排队拦截: %s", event.Reason)
							break
						}

						if event.Type == "text" || event.Type == "thinking" {
							if !hasContent {
								hasContent = true
								out <- StreamEvent{
									Type:           "status",
									Stage:          "connected",
									Round:          roundNum,
									AttemptInRound: attemptInRound,
									Attempt:        attempt,
									Message:        "成功挤入通道！",
								}
							}
							out <- event
						} else if event.Type == "done" {
							if hasContent {
								out <- event
								return
							}
							retryReason = "流连接建立但未返回有效内容"
						}
					}

					if hasContent {
						return
					}
				} else if retryableStatusCodes[statusCode] {
					b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
					resp.Body.Close()
					msg := strings.TrimSpace(string(b))
					if msg == "" {
						msg = "服务排队中/暂时过载"
					}
					retryReason = fmt.Sprintf("HTTP %d: %s", statusCode, msg)
				} else {
					b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
					resp.Body.Close()
					errMsg := fmt.Sprintf("不可重试的平台报错 (HTTP %d): %s", statusCode, string(b))
					out <- StreamEvent{
						Type:       "stream_error",
						StatusCode: statusCode,
						Reason:     errMsg,
						Err:        fmt.Errorf("%s", errMsg),
					}
					return
				}
			}

			// 检查是否超出最大重试
			if c.maxRetries != nil && attempt >= *c.maxRetries {
				out <- StreamEvent{
					Type:    "stream_error",
					Reason:  fmt.Sprintf("已达到最大重试次数 (%d)，最后报错: %s", *c.maxRetries, retryReason),
					Err:     fmt.Errorf("max retries exceeded"),
					Attempt: attempt,
				}
				return
			}

			// 计算等待冷却时长
			var waitDuration time.Duration
			if retryAfterSec > 0 {
				waitDuration = time.Duration(math.Min(retryAfterSec, 60.0)) * time.Second
			} else if isRoundEnd {
				waitDuration = c.interRoundDelay
			} else {
				waitDuration = c.intraRoundDelay
			}

			waitSec := waitDuration.Seconds()
			statusMsg := fmt.Sprintf("第 %d 轮 (%d/%d) 未挤上，等待 %ds 尝试下一次...", roundNum, attemptInRound, c.attemptsPerRound, int(waitSec))
			if isRoundEnd {
				statusMsg = fmt.Sprintf("第 %d 轮完成 (%d/%d)，等待冷却 %ds 开启下一轮...", roundNum, c.attemptsPerRound, c.attemptsPerRound, int(waitSec))
			}

			out <- StreamEvent{
				Type:           "status",
				Stage:          "retrying",
				Round:          roundNum,
				AttemptInRound: attemptInRound,
				MaxInRound:     c.attemptsPerRound,
				Attempt:        attempt,
				StatusCode:     statusCode,
				Reason:         retryReason,
				WaitSeconds:    waitSec,
				IsRoundEnd:     isRoundEnd,
				Message:        statusMsg,
			}

			// 逐秒倒计时
			startSleep := time.Now()
			for {
				elapsed := time.Since(startSleep)
				if elapsed >= waitDuration {
					break
				}
				remaining := int(math.Max(0, math.Round((waitDuration - elapsed).Seconds())))
				out <- StreamEvent{
					Type:           "status",
					Stage:          "waiting",
					Round:          roundNum,
					AttemptInRound: attemptInRound,
					MaxInRound:     c.attemptsPerRound,
					Remaining:      remaining,
					WaitSeconds:    waitSec,
					IsRoundEnd:     isRoundEnd,
				}

				select {
				case <-ctx.Done():
					return
				case <-time.After(1 * time.Second):
				}
			}
		}
	}()

	return out, nil
}

// Chat performs a synchronous non-streaming chat request and returns the full response text.
func (c *AnyRouterClient) Chat(ctx context.Context, model, prompt string) (string, error) {
	events, err := c.StreamChat(ctx, model, prompt, nil, "", "", 4096)
	if err != nil {
		return "", err
	}

	var fullText string
	for event := range events {
		if event.Type == "stream_error" {
			return "", fmt.Errorf("%s", event.Reason)
		}
		if event.Type == "done" {
			fullText = event.FullText
		}
	}
	return fullText, nil
}
