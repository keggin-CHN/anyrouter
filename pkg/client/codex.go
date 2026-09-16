package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var codexModelRegex = regexp.MustCompile(`(?i)(astra|codex|gpt|^o\d)`)

// IsCodexModel determines if a model identifier belongs to the Codex / Responses API family.
func IsCodexModel(modelID string) bool {
	return codexModelRegex.MatchString(strings.ToLower(modelID))
}

type codexTurnMetadata struct {
	InstallationID       string `json:"installation_id"`
	SessionID            string `json:"session_id"`
	ThreadID             string `json:"thread_id"`
	TurnID               string `json:"turn_id"`
	WindowID             string `json:"window_id"`
	RequestKind          string `json:"request_kind"`
	ThreadSource         string `json:"thread_source"`
	TurnStartedAtUnixMs  int64  `json:"turn_started_at_unix_ms"`
}

func (c *AnyRouterClient) createCodexHeaders(sessionID, turnID string) (http.Header, map[string]interface{}) {
	windowID := fmt.Sprintf("%s:0", sessionID)
	meta := codexTurnMetadata{
		InstallationID:      c.codexInstallationID,
		SessionID:           sessionID,
		ThreadID:            sessionID,
		TurnID:              turnID,
		WindowID:            windowID,
		RequestKind:         "turn",
		ThreadSource:        "user",
		TurnStartedAtUnixMs: time.Now().UnixMilli(),
	}

	metaBytes, _ := json.Marshal(meta)
	metaJSON := string(metaBytes)

	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+c.apiKey)
	headers.Set("Accept", "text/event-stream")
	headers.Set("Content-Type", "application/json")
	headers.Set("Originator", "codex_exec")
	headers.Set("User-Agent", fmt.Sprintf("codex_exec/%s (Linux; x86_64) (codex_exec; %s)", CodexVersion, CodexVersion))
	headers.Set("X-Openai-Internal-Codex-Responses-Lite", "true")
	headers.Set("X-Codex-Beta-Features", "remote_compaction_v2")
	headers.Set("X-Codex-Window-Id", windowID)
	headers.Set("X-Codex-Turn-Metadata", metaJSON)
	headers.Set("X-Client-Request-Id", sessionID)
	headers.Set("Session-Id", sessionID)
	headers.Set("Thread-Id", sessionID)

	clientMetadata := map[string]interface{}{
		"session_id":              sessionID,
		"thread_id":               sessionID,
		"turn_id":                 turnID,
		"x-codex-installation-id": c.codexInstallationID,
		"x-codex-window-id":       windowID,
		"x-codex-turn-metadata":   metaJSON,
	}

	return headers, clientMetadata
}

func (c *AnyRouterClient) buildCodexBody(
	model string,
	messages []Message,
	systemPrompt string,
	sessionID string,
	turnID string,
	clientMetadata map[string]interface{},
	maxTokens int,
	reasoningEffort string,
) map[string]interface{} {
	inputItems := make([]map[string]interface{}, 0, len(messages)+1)

	if systemPrompt != "" {
		inputItems = append(inputItems, map[string]interface{}{
			"type": "message",
			"role": "developer",
			"content": []map[string]interface{}{
				{"type": "input_text", "text": systemPrompt},
			},
		})
	}

	for _, msg := range messages {
		if msg.Role == "user" {
			inputItems = append(inputItems, map[string]interface{}{
				"type": "message",
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "input_text", "text": msg.Content},
				},
			})
		} else if msg.Role == "assistant" {
			inputItems = append(inputItems, map[string]interface{}{
				"type":   "message",
				"role":   "assistant",
				"status": "completed",
				"content": []map[string]interface{}{
					{"type": "output_text", "text": msg.Content, "annotations": []interface{}{}},
				},
			})
		}
	}

	if reasoningEffort == "" {
		reasoningEffort = "medium"
	}

	return map[string]interface{}{
		"model":               model,
		"input":               inputItems,
		"tool_choice":         "auto",
		"parallel_tool_calls": false,
		"reasoning": map[string]interface{}{
			"effort":  reasoningEffort,
			"context": "all_turns",
		},
		"store":             false,
		"stream":            true,
		"text":              map[string]interface{}{"verbosity": "low"},
		"max_output_tokens": maxTokens,
		"include":           []string{"reasoning.encrypted_content"},
		"prompt_cache_key":  sessionID,
		"client_metadata":   clientMetadata,
	}
}
