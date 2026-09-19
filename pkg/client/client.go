package client

import (
	"bytes"
	"context"
	"crypto/tls"
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
		apiKey = strings.TrimSpace(os.Getenv("ANYROUTER_API_KEY"))
	}
	if apiKey == "" {
		return nil, fmt.Errorf("请提供 API Key 或设置 ANYROUTER_API_KEY")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = os.Getenv("ANYROUTER_BASE_URL")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")

	proxy := strings.TrimSpace(cfg.Proxy)
	if proxy == "" && cfg.Proxy == "" {
		// Use "direct" or "none" to disable the default local proxy.
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
		MaxIdleConns:          100,
		IdleConnTimeout:       15 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ForceAttemptHTTP2:     false,
		TLSNextProto:          make(map[string]func(authority string, c *tls.Conn) http.RoundTripper), // Disable HTTP/2 over proxy to prevent deadlock
		TLSClientConfig: &tls.Config{
			Renegotiation: tls.RenegotiateFreelyAsClient, // Support Cloudflare/ESA SSL renegotiation
		},
		DisableKeepAlives: proxy != "none" && proxy != "direct", // Preserve compatibility with local proxies.
	}

	if proxy != "" && proxy != "none" && proxy != "direct" {
		proxyURL, err := url.Parse(proxy)
		if err != nil || proxyURL.Hostname() == "" {
			return nil, fmt.Errorf("invalid proxy URL")
		}
		switch proxyURL.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return nil, fmt.Errorf("unsupported proxy scheme")
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	var maxRetries *int
	if cfg.MaxRetries != nil {
		if *cfg.MaxRetries < 1 {
			return nil, fmt.Errorf("MaxRetries must be at least 1")
		}
		attempts := *cfg.MaxRetries
		maxRetries = &attempts
	}
	client := &AnyRouterClient{
		apiKey:              apiKey,
		baseURL:             baseURL,
		proxy:               proxy,
		maxRetries:          maxRetries,
		attemptsPerRound:    attemptsPerRound,
		intraRoundDelay:     intraRoundDelay,
		interRoundDelay:     interRoundDelay,
		timeout:             timeout,
		httpClient:          &http.Client{Transport: transport}, // Each attempt has its own context deadline.
		claudeDeviceID:      RandomHex(32),
		codexInstallationID: NewUUID(),
	}

	return client, nil
}

// CloseIdleConnections releases pooled connections when a client is no longer used.
func (c *AnyRouterClient) CloseIdleConnections() {
	c.httpClient.CloseIdleConnections()
}

// StreamChat sends a prompt or conversation history and yields streaming events over a channel.
// Callers that stop reading early must cancel ctx to release the request.
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
	emit := func(event StreamEvent) bool { return sendEvent(ctx, out, event) }

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
				emit(StreamEvent{Type: "stream_error", Err: err, Reason: err.Error()})
				return
			}

			if !emit(StreamEvent{
				Type:           "status",
				Stage:          "connecting",
				Round:          roundNum,
				AttemptInRound: attemptInRound,
				MaxInRound:     c.attemptsPerRound,
				Attempt:        attempt,
				Message:        fmt.Sprintf("正在发起连接 (第 %d 轮 #%d/%d, 模型: %s)...", roundNum, attemptInRound, c.attemptsPerRound, model),
			}) {
				return
			}

			result := c.streamAttempt(ctx, protocol, endpoint, headers, bodyBytes, out, StreamEvent{
				Type: "status", Stage: "connected", Round: roundNum,
				AttemptInRound: attemptInRound, Attempt: attempt, Message: "成功挤入通道！",
			})
			if ctx.Err() != nil || (result.hasContent && result.err == nil) {
				return
			}
			// Never replay a partially delivered response: the caller would see duplicates.
			if !result.retryable || result.hasContent || (c.maxRetries != nil && attempt >= *c.maxRetries) {
				emit(StreamEvent{
					Type:       "stream_error",
					Reason:     result.err.Error(),
					Err:        result.err,
					StatusCode: result.statusCode,
					Attempt:    attempt,
				})
				return
			}

			// 计算等待冷却时长
			var waitDuration time.Duration
			if result.retryAfter > 0 {
				waitDuration = result.retryAfter
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

			if !emit(StreamEvent{
				Type:           "status",
				Stage:          "retrying",
				Round:          roundNum,
				AttemptInRound: attemptInRound,
				MaxInRound:     c.attemptsPerRound,
				Attempt:        attempt,
				StatusCode:     result.statusCode,
				Reason:         result.err.Error(),
				WaitSeconds:    waitSec,
				IsRoundEnd:     isRoundEnd,
				Message:        statusMsg,
			}) {
				return
			}

			// 逐秒倒计时
			startSleep := time.Now()
			for {
				elapsed := time.Since(startSleep)
				if elapsed >= waitDuration {
					break
				}
				remaining := int(math.Max(0, math.Round((waitDuration - elapsed).Seconds())))
				if !emit(StreamEvent{
					Type:           "status",
					Stage:          "waiting",
					Round:          roundNum,
					AttemptInRound: attemptInRound,
					MaxInRound:     c.attemptsPerRound,
					Remaining:      remaining,
					WaitSeconds:    waitSec,
					IsRoundEnd:     isRoundEnd,
				}) {
					return
				}

				timer := time.NewTimer(min(time.Second, waitDuration-elapsed))
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}
	}()

	return out, nil
}

func sendEvent(ctx context.Context, out chan<- StreamEvent, event StreamEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- event:
		return true
	}
}

type attemptResult struct {
	err        error
	statusCode int
	retryAfter time.Duration
	retryable  bool
	hasContent bool
}

func (c *AnyRouterClient) streamAttempt(parent context.Context, protocol ProtocolType, endpoint string,
	headers http.Header, body []byte, out chan<- StreamEvent, connected StreamEvent) attemptResult {
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	result := attemptResult{retryable: true}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		result.err, result.retryable = err, false
		return result
	}
	req.Header = headers
	resp, err := c.httpClient.Do(req)
	if err != nil {
		result.err = fmt.Errorf("网络连接波动: %w", err)
		return result
	}
	defer resp.Body.Close()
	result.statusCode = resp.StatusCode
	if raw := resp.Header.Get("Retry-After"); raw != "" {
		if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds > 0 {
			result.retryAfter = time.Duration(math.Min(seconds, 60) * float64(time.Second))
		} else if until, err := http.ParseTime(raw); err == nil {
			result.retryAfter = min(max(time.Until(until), 0), time.Minute)
		}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		result.err = fmt.Errorf("HTTP %d: %s", resp.StatusCode, parseErrorMessage(body, resp.StatusCode))
		result.retryable = retryableStatusCodes[resp.StatusCode]
		return result
	}

	// Parse on this goroutine so early exits cannot strand a producer on a full channel.
	emit := func(event StreamEvent) bool {
		switch event.Type {
		case "stream_error":
			result.err = event.Err
			if result.err == nil {
				result.err = fmt.Errorf("%s", event.Reason)
			}
			return false
		case "text", "thinking":
			if event.Delta == "" {
				return true
			}
			if !result.hasContent {
				result.hasContent = true
				if !sendEvent(ctx, out, connected) {
					return false
				}
			}
		case "done":
			if !result.hasContent {
				return false
			}
		}
		return sendEvent(ctx, out, event)
	}
	switch protocol {
	case ProtocolCodex:
		parseCodexSSE(resp.Body, emit)
	case ProtocolClaude:
		parseClaudeSSE(resp.Body, emit)
	case ProtocolOpenAI:
		parseOpenAISSE(resp.Body, emit)
	}
	if ctx.Err() != nil {
		result.err = ctx.Err()
	} else if !result.hasContent && result.err == nil {
		result.err = fmt.Errorf("流连接建立但未返回有效内容")
	}
	return result
}

// Chat performs a synchronous non-streaming chat request and returns the full response text.
func (c *AnyRouterClient) Chat(ctx context.Context, model, prompt string) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, err := c.StreamChat(ctx, model, prompt, nil, "", "", 4096)
	if err != nil {
		return "", err
	}

	var fullText strings.Builder
	for event := range events {
		if event.Type == "stream_error" {
			if event.Err != nil {
				return "", event.Err
			}
			return "", fmt.Errorf("%s", event.Reason)
		}
		if event.Type == "text" {
			fullText.WriteString(event.Delta)
		}
		if event.Type == "done" {
			fullText.Reset()
			fullText.WriteString(event.FullText)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return fullText.String(), nil
}

func parseErrorMessage(raw []byte, statusCode int) string {
	var oneApiErr struct {
		Error interface{} `json:"error"`
	}
	if err := json.Unmarshal(raw, &oneApiErr); err == nil && oneApiErr.Error != nil {
		switch v := oneApiErr.Error.(type) {
		case string:
			if v != "" {
				return v
			}
		case map[string]interface{}:
			if m, ok := v["message"].(string); ok && m != "" {
				if idx := strings.Index(m, " (request id:"); idx != -1 {
					m = m[:idx]
				}
				return m
			}
		}
	}
	msg := strings.TrimSpace(string(raw))
	if msg == "" {
		switch statusCode {
		case 500:
			return "平台模型通道繁忙/负载达上限"
		case 502, 503, 504:
			return "平台服务暂时过载 (Service Unavailable)"
		case 429:
			return "平台触发速率限制 (Rate Limited)"
		case 404:
			return "模型不存在或未开放"
		default:
			return fmt.Sprintf("HTTP %d 状态异常", statusCode)
		}
	}
	if runes := []rune(msg); len(runes) > 120 {
		return string(runes[:120]) + "..."
	}
	return msg
}
