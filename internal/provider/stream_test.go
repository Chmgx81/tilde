package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testDefs() []ToolDef {
	return []ToolDef{{Name: "read_file", Description: "read", Schema: map[string]any{"type": "object"}}}
}

func TestOllamaStreamConcat(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl, _ := w.(http.Flusher)
		for _, line := range []string{
			`{"message":{"content":"hel"},"done":false}`,
			`{"message":{"content":"lo "},"done":false}`,
			`{"message":{"content":"world"},"done":true}`,
		} {
			_, _ = w.Write([]byte(line + "\n"))
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer srv.Close()
	p := NewOllama("test-model")
	p.Host = srv.URL
	got, err := Collect(context.Background(), p, []Message{{Role: "user", Content: "hi"}}, testDefs())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got.Content != "hello world" {
		t.Fatalf("concat = %q, want %q", got.Content, "hello world")
	}
	if len(got.ToolCalls) != 0 {
		t.Fatalf("unexpected calls: %v", got.ToolCalls)
	}
	if seen["stream"] != true {
		t.Fatalf("stream:true missing from request: %v", seen)
	}
	if _, ok := seen["tools"]; !ok {
		t.Fatalf("tools missing from stream request: %v", seen)
	}
}

func TestCollectWithReportsDeltasWithoutChangingResponse(t *testing.T) {
	p := &scriptedStreamer{events: []StreamEvent{{Text: "hel"}, {Text: "lo"}}}
	var got []string
	resp, err := CollectWith(context.Background(), p, nil, nil, func(s string) { got = append(got, s) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "") != "hello" || resp.Content != "hello" {
		t.Fatalf("deltas=%q response=%q", got, resp.Content)
	}
}

func TestCollectWithDoesNotExposeThinkTags(t *testing.T) {
	p := &scriptedStreamer{events: []StreamEvent{{Text: "<think>secret"}, {Text: "</think>hello"}}}
	var got []string
	resp, err := CollectWith(context.Background(), p, nil, nil, func(s string) { got = append(got, s) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "") != "hello" || resp.Content != "hello" || resp.Thinking != "secret" {
		t.Fatalf("deltas=%q response=%q thinking=%q", got, resp.Content, resp.Thinking)
	}
}

type scriptedStreamer struct{ events []StreamEvent }

func (s *scriptedStreamer) Stream(context.Context, []Message, []ToolDef) (<-chan StreamEvent, <-chan error) {
	events := make(chan StreamEvent, len(s.events))
	errs := make(chan error)
	for _, event := range s.events {
		events <- event
	}
	close(events)
	close(errs)
	return events, errs
}

func TestOllamaStreamToolCalls(t *testing.T) {
	// Prose + native calls in one turn: both must survive (the P2-D
	// regression was prose taking a text-only fast path and dropping
	// the calls).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, line := range []string{
			`{"message":{"content":"reading now"},"done":false}`,
			`{"message":{"content":"","tool_calls":[{"id":"c1","function":{"name":"read_file","arguments":{"path":"go.mod"}}}]},"done":true}`,
		} {
			_, _ = w.Write([]byte(line + "\n"))
		}
	}))
	defer srv.Close()
	p := NewOllama("test-model")
	p.Host = srv.URL
	got, err := Collect(context.Background(), p, []Message{{Role: "user", Content: "hi"}}, testDefs())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got.Content != "reading now" {
		t.Fatalf("content = %q, want prose preserved", got.Content)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "read_file" {
		t.Fatalf("calls = %v, want one read_file", got.ToolCalls)
	}
	if got.ToolCalls[0].ID != "c1" {
		t.Fatalf("call id = %q, want c1", got.ToolCalls[0].ID)
	}
	if p, _ := got.ToolCalls[0].Args["path"].(string); p != "go.mod" {
		t.Fatalf("args = %v, want path=go.mod", got.ToolCalls[0].Args)
	}
}

func TestOllamaStreamLengthTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{\"message\":{\"content\":\"half\"},\"done\":true,\"done_reason\":\"length\"}\n"))
	}))
	defer srv.Close()
	p := NewOllama("test-model")
	p.Host = srv.URL
	got, err := Collect(context.Background(), p, []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !got.Truncated {
		t.Fatal("done_reason=length must mark Truncated")
	}
}

func TestOpenAIStreamSSEDone(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, line := range []string{
			`data: {"choices":[{"delta":{"content":"hel"}}]}`,
			`data: {"choices":[{"delta":{"content":"lo"}}]}`,
			``,
			`: ping`,
			`data: {"choices":[{"delta":{}}]}`,
			`data: [DONE]`,
		} {
			_, _ = w.Write([]byte(line + "\n"))
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
	got, err := Collect(context.Background(), p, []Message{{Role: "user", Content: "hi"}}, testDefs())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got.Content != "hello" {
		t.Fatalf("concat = %q, want %q", got.Content, "hello")
	}
	if seen["stream"] != true {
		t.Fatalf("stream:true missing from request: %v", seen)
	}
	if _, ok := seen["tools"]; !ok {
		t.Fatalf("tools missing from stream request: %v", seen)
	}
}

func TestOpenAIStreamIndexedCalls(t *testing.T) {
	// Fragmented arguments across deltas, two indexed calls, one with
	// no id (call_N fallback), plus a length finish_reason.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range []string{
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a1","function":{"name":"write_file","arguments":"{\"path\":"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"name":"grep","arguments":""}}]}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"length"}]}`,
			`data: [DONE]`,
		} {
			_, _ = w.Write([]byte(line + "\n"))
		}
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
	got, err := Collect(context.Background(), p, []Message{{Role: "user", Content: "hi"}}, testDefs())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(got.ToolCalls) != 2 {
		t.Fatalf("calls = %v, want 2", got.ToolCalls)
	}
	if got.ToolCalls[0].ID != "a1" || got.ToolCalls[0].Name != "write_file" {
		t.Fatalf("call0 = %+v", got.ToolCalls[0])
	}
	if p, _ := got.ToolCalls[0].Args["path"].(string); p != "x" {
		t.Fatalf("call0 args = %v", got.ToolCalls[0].Args)
	}
	if got.ToolCalls[1].ID != "call_1" || len(got.ToolCalls[1].Args) != 0 {
		t.Fatalf("call1 = %+v, want call_1 with empty args", got.ToolCalls[1])
	}
	if !got.Truncated {
		t.Fatal("finish_reason=length must mark Truncated")
	}
}

func TestOpenAIStreamMalformedArgsDegrade(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, line := range []string{
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"m1","function":{"name":"edit_file","arguments":"{oops"}}]}}]}`,
			`data: [DONE]`,
		} {
			_, _ = w.Write([]byte(line + "\n"))
		}
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
	got, err := Collect(context.Background(), p, []Message{{Role: "user", Content: "hi"}}, testDefs())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(got.ToolCalls) != 1 || len(got.ToolCalls[0].Args) != 0 {
		t.Fatalf("malformed call must be kept with empty args: %+v", got.ToolCalls)
	}
	if !strings.Contains(got.Content, "malformed tool arguments") {
		t.Fatalf("prose must pin the parse failure, got %q", got.Content)
	}
}

func TestStreamContentCallFallback(t *testing.T) {
	// No structured calls, but prose echoes call JSON: Chat's fallback
	// applies on the stream path too.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{\"message\":{\"content\":\"{\\\"name\\\": \\\"grep\\\", \\\"arguments\\\": {\\\"pattern\\\": \\\"x\\\"}}\"},\"done\":true}\n"))
	}))
	defer srv.Close()
	p := NewOllama("test-model")
	p.Host = srv.URL
	got, err := Collect(context.Background(), p, []Message{{Role: "user", Content: "hi"}}, testDefs())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "grep" {
		t.Fatalf("fallback calls = %+v", got.ToolCalls)
	}
	if got.Content != "" {
		t.Fatalf("content must clear on fallback, got %q", got.Content)
	}
}

func TestStreamMidErrorSurfaces(t *testing.T) {
	// NDJSON: one good chunk, then garbage — the collector must report
	// an ordinary provider error (the loop turns it into a Chat fallback).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte("{\"message\":{\"content\":\"partial\"},\"done\":false}\n"))
		_, _ = w.Write([]byte("this is not json\n"))
	}))
	defer srv.Close()
	p := NewOllama("test-model")
	p.Host = srv.URL
	if _, err := Collect(context.Background(), p, []Message{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Fatal("expected mid-stream decode error")
	} else if Classify(err) == "" {
		t.Fatalf("stream error must Classify, got %v", err)
	}

	// SSE: one good delta, then garbage — same contract.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n"))
		_, _ = w.Write([]byte("data: {broken\n"))
	}))
	defer srv2.Close()
	p2 := NewOpenAI("test-model", srv2.URL, "test-key")
	if _, err := Collect(context.Background(), p2, []Message{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Fatal("expected mid-stream SSE error")
	}
}

func TestStreamNon200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
	if _, err := Collect(context.Background(), p, []Message{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Fatal("expected status error")
	} else if !strings.Contains(err.Error(), "401") {
		t.Fatalf("status must survive in the error, got: %v", err)
	}
}

func TestStreamFirstEventTimeout(t *testing.T) {
	old := StreamFirstChunkTimeout
	StreamFirstChunkTimeout = 20 * time.Millisecond
	defer func() { StreamFirstChunkTimeout = old }()
	// Server accepts the request but never sends an event.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
	// Cancellable ctx: Collect gives up at first-event timeout,
	// cancel releases the hung handler so srv.Close() can return.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := Collect(ctx, p, []Message{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Fatal("expected first-event timeout")
	} else if !strings.Contains(err.Error(), "first event") {
		t.Fatalf("want first-event timeout, got: %v", err)
	}
}
