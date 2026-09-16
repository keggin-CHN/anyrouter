package client

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestUUID(t *testing.T) {
	u1 := NewUUID()
	u2 := NewUUID()
	if len(u1) != 36 || len(u2) != 36 {
		t.Fatalf("invalid uuid lengths: %s, %s", u1, u2)
	}
	if u1 == u2 {
		t.Fatalf("uuid collision: %s == %s", u1, u2)
	}
	h := RandomHex(32)
	if len(h) != 64 {
		t.Fatalf("invalid hex length: %d", len(h))
	}
}

func TestModelIdentification(t *testing.T) {
	codexModels := []string{"gpt-6-astra", "gpt-5-codex", "o1", "o3-mini", "astra-pro"}
	for _, m := range codexModels {
		if !IsCodexModel(m) {
			t.Errorf("expected %s to be recognized as codex model", m)
		}
	}

	claudeModels := []string{"claude-fable-5-1", "claude-3-7-sonnet-20250219", "claude-3-5-haiku"}
	for _, m := range claudeModels {
		if IsCodexModel(m) {
			t.Errorf("expected %s NOT to be recognized as codex model", m)
		}
	}
}

func TestCodexMasquerade(t *testing.T) {
	c, err := NewClient(ClientConfig{APIKey: "sk-test-key"})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	headers, clientMeta := c.createCodexHeaders("sess-123", "turn-456")
	if headers.Get("Originator") != "codex_exec" {
		t.Errorf("wrong originator: %s", headers.Get("Originator"))
	}
	if !strings.Contains(headers.Get("User-Agent"), "codex_exec/0.144.1") {
		t.Errorf("wrong user agent: %s", headers.Get("User-Agent"))
	}
	if headers.Get("X-Openai-Internal-Codex-Responses-Lite") != "true" {
		t.Errorf("missing lite header")
	}

	body := c.buildCodexBody("gpt-6-astra", []Message{{Role: "user", Content: "hello"}}, "sys", "sess-123", "turn-456", clientMeta, 1024, "medium")
	bBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal err: %v", err)
	}
	if !strings.Contains(string(bBytes), "developer") || !strings.Contains(string(bBytes), "sys") {
		t.Errorf("body missing developer message: %s", string(bBytes))
	}
}

func TestClaudeMasquerade(t *testing.T) {
	c, err := NewClient(ClientConfig{APIKey: "sk-test-key"})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	headers := c.createClaudeHeaders("sess-claude", 0)
	if !strings.Contains(headers.Get("User-Agent"), "claude-cli/2.1.226") {
		t.Errorf("wrong claude user agent: %s", headers.Get("User-Agent"))
	}
	if !strings.Contains(headers.Get("Anthropic-Beta"), "claude-code-20250219") {
		t.Errorf("missing anthropic beta flags")
	}

	body := c.buildClaudeBody("claude-fable-5-1", []Message{{Role: "user", Content: "ping"}}, "", "sess-claude", 64)
	bBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal err: %v", err)
	}
	if !strings.Contains(string(bBytes), "x-anthropic-billing-header") {
		t.Errorf("body missing billing header: %s", string(bBytes))
	}
}

func TestCodexSSEParser(t *testing.T) {
	sseInput := `
data: {"type":"response.reasoning_text.delta","delta":"thinking step 1"}
data: {"type":"response.output_text.delta","delta":"Hello "}
data: {"type":"response.output_text.delta","delta":"World!"}
data: {"type":"response.completed"}
data: [DONE]
`
	out := make(chan StreamEvent, 10)
	go func() {
		parseCodexSSE(bytes.NewBufferString(sseInput), out)
		close(out)
	}()

	var fullText strings.Builder
	var fullThinking strings.Builder
	for ev := range out {
		if ev.Type == "text" {
			fullText.WriteString(ev.Delta)
		} else if ev.Type == "thinking" {
			fullThinking.WriteString(ev.Delta)
		} else if ev.Type == "done" {
			if ev.FullText != "Hello World!" {
				t.Errorf("unexpected done text: %s", ev.FullText)
			}
			if ev.FullThinking != "thinking step 1" {
				t.Errorf("unexpected done thinking: %s", ev.FullThinking)
			}
		}
	}
	if fullText.String() != "Hello World!" {
		t.Errorf("unexpected assembled text: %s", fullText.String())
	}
}

func TestClaudeSSEParser(t *testing.T) {
	sseInput := `
data: {"type":"content_block_start","content_block":{"type":"thinking"}}
data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"let me think"}}
data: {"type":"content_block_start","content_block":{"type":"text"}}
data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"pong"}}
data: {"type":"message_stop"}
data: [DONE]
`
	out := make(chan StreamEvent, 10)
	go func() {
		parseClaudeSSE(bytes.NewBufferString(sseInput), out)
		close(out)
	}()

	var fullText strings.Builder
	var fullThinking strings.Builder
	for ev := range out {
		if ev.Type == "text" {
			fullText.WriteString(ev.Delta)
		} else if ev.Type == "thinking" {
			fullThinking.WriteString(ev.Delta)
		}
	}
	if fullText.String() != "pong" {
		t.Errorf("unexpected assembled text: %s", fullText.String())
	}
	if fullThinking.String() != "let me think" {
		t.Errorf("unexpected assembled thinking: %s", fullThinking.String())
	}
}
