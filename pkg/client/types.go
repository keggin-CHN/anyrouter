package client

import (
	"time"
)

const (
	DefaultBaseURL           = "https://anyrouter.top"
	DefaultProxy             = "http://127.0.0.1:10808"
	ClaudeCodeVersion        = "2.1.226"
	ClaudeCodeVersionBuild   = "2.1.226.b94"
	StainlessPackageVersion  = "0.94.0"
	AnthropicBeta            = "claude-code-20250219,context-1m-2025-08-07,interleaved-thinking-2025-05-14,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,effort-2025-11-24"
	CodexVersion             = "0.144.1"
)

// ProtocolType identifies the backend API protocol and client masquerade to use.
type ProtocolType string

const (
	ProtocolCodex  ProtocolType = "codex"  // OpenAI Responses API (/v1/responses) with codex_exec masquerade
	ProtocolClaude ProtocolType = "claude" // Anthropic Messages API (/v1/messages?beta=true) with claude-cli masquerade
	ProtocolOpenAI ProtocolType = "openai" // OpenAI Chat Completions (/v1/chat/completions) for Gemini and standard models
)


// Message represents a chat message between user and assistant.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// StreamEvent represents an event emitted during stream_chat.
type StreamEvent struct {
	Type           string  `json:"type"`                     // status, text, thinking, done, stream_error
	Stage          string  `json:"stage,omitempty"`          // connecting, connected, retrying, waiting
	Delta          string  `json:"delta,omitempty"`          // text or thinking incremental chunk
	FullText       string  `json:"full_text,omitempty"`      // complete text on done
	FullThinking   string  `json:"full_thinking,omitempty"`  // complete thinking on done
	Message        string  `json:"message,omitempty"`        // user friendly status message
	Round          int     `json:"round,omitempty"`          // current retry round (1-based)
	AttemptInRound int     `json:"attempt_in_round,omitempty"` // attempt within round (1-based)
	MaxInRound     int     `json:"max_in_round,omitempty"`   // max attempts in this round
	Attempt        int     `json:"attempt,omitempty"`        // overall attempt counter
	StatusCode     int     `json:"status_code,omitempty"`    // HTTP response status code
	Reason         string  `json:"reason,omitempty"`         // failure or retry reason
	WaitSeconds    float64 `json:"wait_seconds,omitempty"`   // seconds to wait before next retry
	Remaining      int     `json:"remaining,omitempty"`      // countdown seconds remaining
	IsRoundEnd     bool    `json:"is_round_end,omitempty"`   // whether current round ended
	Err            error   `json:"-"`                        // underlying error if any
}

// ClientConfig configures the AnyRouterClient.
type ClientConfig struct {
	APIKey           string
	BaseURL          string
	Proxy            string
	MaxRetries       *int
	AttemptsPerRound int
	IntraRoundDelay  time.Duration
	InterRoundDelay  time.Duration
	Timeout          time.Duration
}
