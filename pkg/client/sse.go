package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// parseCodexSSE parses an SSE stream produced by OpenAI Responses Lite.
func parseCodexSSE(r io.Reader, out chan<- StreamEvent) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024) // up to 1MB per line

	var fullText strings.Builder
	var fullThinking strings.Builder

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		dataStr := strings.TrimSpace(line[5:])
		if dataStr == "" || dataStr == "[DONE]" {
			break
		}

		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(dataStr), &payload); err != nil {
			continue
		}

		eventType, _ := payload["type"].(string)

		if eventType == "error" {
			errMap, _ := payload["error"].(map[string]interface{})
			msg, _ := errMap["message"].(string)
			if msg == "" {
				msg = fmt.Sprintf("%v", errMap)
			}
			out <- StreamEvent{
				Type:   "stream_error",
				Reason: msg,
			}
			return
		}

		if eventType == "response.failed" {
			respMap, _ := payload["response"].(map[string]interface{})
			errMap, _ := respMap["error"].(map[string]interface{})
			msg, _ := errMap["message"].(string)
			if msg == "" {
				msg = fmt.Sprintf("%v", errMap)
			}
			out <- StreamEvent{
				Type:   "stream_error",
				Reason: msg,
			}
			return
		}

		if eventType == "response.output_text.delta" {
			delta, _ := payload["delta"].(string)
			fullText.WriteString(delta)
			out <- StreamEvent{
				Type:  "text",
				Delta: delta,
			}
		} else if eventType == "response.reasoning_text.delta" || eventType == "response.reasoning_summary_text.delta" {
			delta, _ := payload["delta"].(string)
			fullThinking.WriteString(delta)
			out <- StreamEvent{
				Type:  "thinking",
				Delta: delta,
			}
		} else if eventType == "response.completed" {
			break
		}
	}

	out <- StreamEvent{
		Type:         "done",
		FullText:     fullText.String(),
		FullThinking: fullThinking.String(),
	}
}

// parseClaudeSSE parses an SSE stream produced by Anthropic Claude Code Messages.
func parseClaudeSSE(r io.Reader, out chan<- StreamEvent) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var fullText strings.Builder
	var fullThinking strings.Builder

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		dataStr := strings.TrimSpace(line[5:])
		if dataStr == "" || dataStr == "[DONE]" {
			break
		}

		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(dataStr), &payload); err != nil {
			continue
		}

		eventType, _ := payload["type"].(string)

		if eventType == "error" {
			errMap, _ := payload["error"].(map[string]interface{})
			msg, _ := errMap["message"].(string)
			if msg == "" {
				msg = fmt.Sprintf("%v", errMap)
			}
			out <- StreamEvent{
				Type:   "stream_error",
				Reason: msg,
			}
			return
		}

		if eventType == "content_block_delta" {
			deltaObj, ok := payload["delta"].(map[string]interface{})
			if ok {
				deltaType, _ := deltaObj["type"].(string)
				if deltaType == "text_delta" {
					text, _ := deltaObj["text"].(string)
					fullText.WriteString(text)
					out <- StreamEvent{
						Type:  "text",
						Delta: text,
					}
				} else if deltaType == "thinking_delta" {
					thinking, _ := deltaObj["thinking"].(string)
					fullThinking.WriteString(thinking)
					out <- StreamEvent{
						Type:  "thinking",
						Delta: thinking,
					}
				}
			}
		} else if eventType == "message_stop" {
			break
		}
	}

	out <- StreamEvent{
		Type:         "done",
		FullText:     fullText.String(),
		FullThinking: fullThinking.String(),
	}
}
