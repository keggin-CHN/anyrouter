package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

func (c *AnyRouterClient) createClaudeHeaders(sessionID string, retryCount int) http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/json")
	headers.Set("Authorization", "Bearer "+c.apiKey)
	headers.Set("Anthropic-Version", "2023-06-01")
	headers.Set("Anthropic-Dangerous-Direct-Browser-Access", "true")
	headers.Set("Anthropic-Beta", AnthropicBeta)
	headers.Set("User-Agent", fmt.Sprintf("claude-cli/%s (external, sdk-cli)", ClaudeCodeVersion))
	headers.Set("X-App", "cli")
	headers.Set("X-Claude-Code-Session-Id", sessionID)
	headers.Set("X-Stainless-Retry-Count", strconv.Itoa(retryCount))
	headers.Set("X-Stainless-Timeout", "600")
	headers.Set("X-Stainless-Lang", "js")
	headers.Set("X-Stainless-Package-Version", StainlessPackageVersion)
	headers.Set("X-Stainless-Os", "Linux")
	headers.Set("X-Stainless-Arch", "x64")
	headers.Set("X-Stainless-Runtime", "node")
	headers.Set("X-Stainless-Runtime-Version", "v26.3.0")
	return headers
}

func (c *AnyRouterClient) buildClaudeBody(
	model string,
	messages []Message,
	systemPrompt string,
	sessionID string,
	maxTokens int,
) map[string]interface{} {
	systemBlocks := []map[string]interface{}{
		{
			"type": "text",
			"text": fmt.Sprintf("x-anthropic-billing-header: cc_version=%s; cc_entrypoint=sdk-cli;", ClaudeCodeVersionBuild),
		},
		{
			"type": "text",
			"text": "You are a Claude agent, built on Anthropic's Claude Agent SDK.",
			"cache_control": map[string]interface{}{
				"type": "ephemeral",
			},
		},
	}

	if systemPrompt != "" {
		systemBlocks = append(systemBlocks, map[string]interface{}{
			"type": "text",
			"text": systemPrompt,
			"cache_control": map[string]interface{}{
				"type": "ephemeral",
			},
		})
	}

	formattedMessages := make([]map[string]interface{}, 0, len(messages))
	for _, msg := range messages {
		formattedMessages = append(formattedMessages, map[string]interface{}{
			"role": msg.Role,
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": msg.Content,
				},
			},
		})
	}

	// 标记最后一条用户消息开启临时缓存
	if len(formattedMessages) > 0 {
		lastIdx := len(formattedMessages) - 1
		if formattedMessages[lastIdx]["role"] == "user" {
			contents, ok := formattedMessages[lastIdx]["content"].([]map[string]interface{})
			if ok && len(contents) > 0 {
				contents[len(contents)-1]["cache_control"] = map[string]interface{}{
					"type": "ephemeral",
				}
			}
		}
	}

	userIDMeta, _ := json.Marshal(map[string]string{
		"device_id":    c.claudeDeviceID,
		"account_uuid": "",
		"session_id":   sessionID,
	})

	return map[string]interface{}{
		"model":      model,
		"max_tokens": maxTokens,
		"stream":     true,
		"system":     systemBlocks,
		"messages":   formattedMessages,
		"metadata": map[string]interface{}{
			"user_id": string(userIDMeta),
		},
	}
}
