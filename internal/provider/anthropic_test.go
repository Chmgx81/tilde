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

func TestAnthropicToolUseRoundTrip(t *testing.T) {
	var seen map[string]any
	var gotVersion, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.Header.Get("anthropic-version")
		gotKey = r.Header.Get("x-api-key")
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"working on it"},{"type":"tool_use","id":"toolu_1","name":"write_file","input":{"path":"a.txt"}}],"stop_reason":"tool_use","usage":{"input_tokens":11,"output_tokens":7}}`))
	}))
	defer srv.Close()
	p := NewAnthropic("test-model", srv.URL, "test-key")
	resp, err := p.Chat(context.Background(),
		[]Message{{Role: "system", Content: "sys prompt"}, {Role: "user", Content: "hi"}},
		[]ToolDef{{Name: "write_file", Description: "write", Schema: map[string]any{"type": "object"}}})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if gotVersion != "2023-06-01" {
		t.Fatalf("anthropic-version header: %q", gotVersion)
	}
	if gotKey != "test-key" {
		t.Fatalf("x-api-key header: %q", gotKey)
	}
	if resp.Content != "working on it" {
		t.Fatalf("content: %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "write_file" || resp.ToolCalls[0].Args["path"] != "a.txt" {
		t.Fatalf("calls: %+v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].ID != "toolu_1" {
		t.Fatalf("call id not preserved: %+v", resp.ToolCalls[0])
	}
	if resp.Usage != (Usage{Prompt: 11, Completion: 7, Total: 18}) {
		t.Fatalf("usage: %+v", resp.Usage)
	}
	// System prompt travels top-level, not as a message.
	if seen["system"] != "sys prompt" {
		t.Fatalf("system field: %v", seen)
	}
	msgs, ok := seen["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("system message must not appear in messages: %v", seen)
	}
	tools, ok := seen["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools array missing or wrong: %v", seen)
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "write_file" || tool["input_schema"] == nil {
		t.Fatalf("tool must use input_schema format: %v", tool)
	}
	if _, ok := tool["input_schema"].(map[string]any); !ok {
		t.Fatalf("input_schema must be an object: %v", tool)
	}
}

func TestAnthropicTextOnlyOmitsTools(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`))
	}))
	defer srv.Close()
	p := NewAnthropic("test-model", srv.URL, "test-key")
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Content != "hello" {
		t.Fatalf("content: %q", resp.Content)
	}
	if _, present := seen["tools"]; present {
		t.Fatalf("tools key must be absent, got: %v", seen)
	}
	if _, present := seen["system"]; present {
		t.Fatalf("system key must be absent with no system messages, got: %v", seen)
	}
}

func TestAnthropicMergesConsecutiveUserMessages(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()
	p := NewAnthropic("test-model", srv.URL, "test-key")
	_, err := p.Chat(context.Background(), []Message{
		{Role: "user", Content: "goal: do x"},
		{Role: "user", Content: "Tool glob result:\n*.go"},
		{Role: "user", Content: "Tool read_file result:\nhi"},
	}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	msgs, ok := seen["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("consecutive user turns must merge into one, got: %v", seen["messages"])
	}
	m, _ := msgs[0].(map[string]any)
	if m["role"] != "user" || !strings.Contains(m["content"].(string), "goal: do x") || !strings.Contains(m["content"].(string), "Tool read_file result:") {
		t.Fatalf("merged content wrong: %v", m)
	}
}

func TestAnthropicStopReasonMaxTokensKeepsPartialText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"partial..."}],"stop_reason":"max_tokens","usage":{"input_tokens":100,"output_tokens":10}}`))
	}))
	defer srv.Close()
	p := NewAnthropic("test-model", srv.URL, "test-key")
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("max_tokens with partial text must not error: %v", err)
	}
	if resp.Content != "partial..." {
		t.Fatalf("content: %q", resp.Content)
	}
}

func TestAnthropicSkipsThinkingBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"done"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()
	p := NewAnthropic("test-model", srv.URL, "test-key")
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Content != "done" {
		t.Fatalf("thinking blocks must not leak into prose, got %q", resp.Content)
	}
	if resp.Thinking != "hmm" {
		t.Fatalf("thinking must be captured for display, got %q", resp.Thinking)
	}
}

func TestAnthropicSkipsRedactedThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"redacted_thinking","data":"opaque"},{"type":"text","text":"done"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()
	p := NewAnthropic("test-model", srv.URL, "test-key")
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Content != "done" || resp.Thinking != "" {
		t.Fatalf("redacted thinking is opaque — nothing to display: content=%q thinking=%q", resp.Content, resp.Thinking)
	}
}

func TestAnthropic401MentionsKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()
	p := NewAnthropic("test-model", srv.URL, "test-key")
	_, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "key") {
		t.Fatalf("expected key-hint error, got: %v", err)
	}
}

func TestAnthropicMalformedInputDegrades(t *testing.T) {
	// Per-call degrade (mirrors openai.go): a non-object input keeps the
	// call with empty args plus a prose note instead of aborting the turn.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"tool_use","id":"t1","name":"write_file","input":[1,2]}],"stop_reason":"tool_use"}`))
	}))
	defer srv.Close()
	p := NewAnthropic("test-model", srv.URL, "test-key")
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}},
		[]ToolDef{{Name: "write_file"}})
	if err != nil {
		t.Fatalf("malformed input must degrade, not abort: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "write_file" {
		t.Fatalf("expected one degraded call, got: %+v", resp.ToolCalls)
	}
	if !strings.Contains(resp.Content, "write_file") {
		t.Fatalf("prose must name the bad call, got %q", resp.Content)
	}
}

func TestAnthropicRetry429Then200(t *testing.T) {
	retryBaseDelay = time.Millisecond
	defer func() { retryBaseDelay = 200 * time.Millisecond }()
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"slow down"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()
	p := NewAnthropic("test-model", srv.URL, "test-key")
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Content != "hi" {
		t.Fatalf("content: %q", resp.Content)
	}
	if attempts != 2 {
		t.Fatalf("attempts=%d, want 2", attempts)
	}
}

func TestAnthropicNoRetryOn401(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()
	p := NewAnthropic("test-model", srv.URL, "test-key")
	if _, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d, want 1 (fail fast)", attempts)
	}
}

func TestAnthropicEmptyReplyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()
	p := NewAnthropic("test-model", srv.URL, "test-key")
	_, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error for empty reply")
	}
}

func TestAnthropicUsageAbsentYieldsZeros(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()
	p := NewAnthropic("test-model", srv.URL, "test-key")
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Usage != (Usage{}) {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}
