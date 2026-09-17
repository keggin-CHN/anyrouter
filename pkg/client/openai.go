package client

import (
	"net/http"
)

// createOpenAIHeaders builds headers for standard OpenAI protocol endpoints (/v1/chat/completions).
// Used for models like gemini-2.5-pro, gpt-4o, etc.
func (c *AnyRouterClient) createOpenAIHeaders(sessionID string) http.Header {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+c.apiKey)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "text/event-stream, application/json")
	headers.Set("User-Agent", "OpenAI/Python 1.50.0 (Windows; x64) (anyrouter-keeper; 2.6.0)")
	headers.Set("X-Client-Request-Id", sessionID)
	return headers
}

// buildOpenAIBody constructs the JSON payload for standard OpenAI chat completions.
func (c *AnyRouterClient) buildOpenAIBody(
	model string,
	messages []Message,
	systemPrompt string,
	maxTokens int,
) map[string]interface{} {
	chatMessages := make([]map[string]interface{}, 0, len(messages)+1)

	if systemPrompt != "" {
		chatMessages = append(chatMessages, map[string]interface{}{
			"role":    "system",
			"content": systemPrompt,
		})
	}

	for _, msg := range messages {
		chatMessages = append(chatMessages, map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	body := map[string]interface{}{
		"model":    model,
		"messages": chatMessages,
		"stream":   true,
	}

	if maxTokens > 0 {
		body["max_tokens"] = maxTokens
	}

	return body
}
