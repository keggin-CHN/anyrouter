package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func localClient(t *testing.T, handler http.HandlerFunc, attempts int, timeout time.Duration) *AnyRouterClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := NewClient(ClientConfig{
		APIKey: "sk-test-only", BaseURL: server.URL, Proxy: "direct",
		MaxRetries: &attempts, Timeout: timeout, IntraRoundDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func TestStreamTimeoutAfterHeaders(t *testing.T) {
	c := localClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, 1, 50*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.Chat(ctx, "gpt-4o", "ping")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected stream timeout, got %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("client timeout was ignored; only the caller deadline stopped the request")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type trackedBody struct {
	io.Reader
	closed chan struct{}
}

func (b *trackedBody) Close() error { close(b.closed); return nil }

func TestStreamCancellationReleasesBlockedProducer(t *testing.T) {
	one := 1
	c, err := NewClient(ClientConfig{APIKey: "test", Proxy: "direct", MaxRetries: &one})
	if err != nil {
		t.Fatal(err)
	}
	body := &trackedBody{
		Reader: strings.NewReader(strings.Repeat("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n", 1000)),
		closed: make(chan struct{}),
	}
	c.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := c.StreamChat(ctx, "gpt-4o", "ping", nil, "", "", 32)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for len(events) < cap(events) {
		select {
		case <-deadline.C:
			t.Fatal("stream did not fill the output buffer")
		case <-ticker.C:
		}
	}
	cancel()
	// Deliberately do not drain events: cancellation must release the producer itself.
	select {
	case <-body.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("response body leaked when the consumer stopped reading")
	}
}

func TestStreamRetriesEmptyReplyAndTransientHTTPError(t *testing.T) {
	var requests atomic.Int32
	c := localClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch requests.Add(1) {
		case 1:
			http.Error(w, "busy", http.StatusServiceUnavailable)
		case 2:
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"\"}}]}\n\ndata: [DONE]\n\n")
		default:
			fmt.Fprint(w, "data:\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\ndata: [DONE]\n\n")
		}
	}, 3, time.Second)
	reply, err := c.Chat(context.Background(), "gpt-4o", "ping")
	if err != nil || reply != "pong" || requests.Load() != 3 {
		t.Fatalf("reply=%q err=%v requests=%d", reply, err, requests.Load())
	}
}

func TestStreamDoesNotReplayPartialResponse(t *testing.T) {
	var requests atomic.Int32
	c := localClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\ndata: {\"error\":{\"message\":\"upstream failed\"}}\n\n")
	}, 3, time.Second)
	_, err := c.Chat(context.Background(), "gpt-4o", "ping")
	if err == nil || !strings.Contains(err.Error(), "upstream failed") || requests.Load() != 1 {
		t.Fatalf("partial response must fail without replay: err=%v requests=%d", err, requests.Load())
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestSSEParsersReportReadErrors(t *testing.T) {
	for name, parse := range map[string]func(io.Reader, func(StreamEvent) bool){
		"codex": parseCodexSSE, "claude": parseClaudeSSE, "openai": parseOpenAISSE,
	} {
		t.Run(name, func(t *testing.T) {
			var got StreamEvent
			parse(brokenReader{}, func(ev StreamEvent) bool { got = ev; return true })
			if got.Type != "stream_error" || !errors.Is(got.Err, io.ErrUnexpectedEOF) {
				t.Fatalf("read failure was hidden: %+v", got)
			}
		})
	}
}

func TestChatReturnsCancellation(t *testing.T) {
	c, err := NewClient(ClientConfig{APIKey: "test", Proxy: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Chat(ctx, "gpt-4o", "ping"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestCredentialsAndTLSVerification(t *testing.T) {
	t.Setenv("ANYROUTER_API_KEY", "")
	if _, err := NewClient(ClientConfig{}); err == nil {
		t.Fatal("missing key must fail")
	}
	t.Setenv("ANYROUTER_API_KEY", " sk-environment ")
	c, err := NewClient(ClientConfig{})
	if err != nil || c.apiKey != "sk-environment" {
		t.Fatalf("environment key: %v", err)
	}
	if c.httpClient.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Fatal("TLS certificate verification must be enabled")
	}
}
