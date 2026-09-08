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

func TestOpenAIReasoningContentCaptured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi","reasoning_content":"weighing options"}}]}`))
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Content != "hi" || resp.Thinking != "weighing options" {
		t.Fatalf("content=%q thinking=%q", resp.Content, resp.Thinking)
	}
}

func TestOpenAIToolCallRoundTrip(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"working on it","tool_calls":[{"id":"call_1","function":{"name":"write_file","arguments":"{\"path\":\"a.txt\"}"}}]}}]}`))
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}},
		[]ToolDef{{Name: "write_file", Description: "write", Schema: map[string]any{"type": "object"}}})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Content != "working on it" {
		t.Fatalf("content: %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "write_file" || resp.ToolCalls[0].Args["path"] != "a.txt" {
		t.Fatalf("calls: %+v", resp.ToolCalls)
	}
	tools, ok := seen["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools array missing or wrong: %v", seen)
	}
}

func TestOpenAITextOnlyOmitsTools(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("auth header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hello"}}]}`))
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
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
}

func TestOpenAI401MentionsKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
	_, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "key") {
		t.Fatalf("expected key-hint error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "/login openai") {
		t.Fatalf("401 remedy must be command-shaped (/login openai), got: %v", err)
	}
}

func TestOpenAI404PointsToCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"message":"model not found"}}`))
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
	_, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "/model") {
		t.Fatalf("404 remedy must point at /model catalog, got: %v", err)
	}
}

func TestOpenAIMalformedArgsError(t *testing.T) {
	// Per-call degrade (behavior change, intended): a malformed call no
	// longer aborts the whole turn — it degrades to an empty-args call
	// (dispatch reports it as a tool-result error) plus a prose note.
	// Previously this returned a hard error with zero calls.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"c1","function":{"name":"write_file","arguments":"{not json"}}]}}]}`))
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}},
		[]ToolDef{{Name: "write_file"}})
	if err != nil {
		t.Fatalf("malformed call must degrade, not abort: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "write_file" {
		t.Fatalf("expected one degraded call, got: %+v", resp.ToolCalls)
	}
	if !strings.Contains(resp.Content, "write_file") {
		t.Fatalf("prose must name the bad call, got %q", resp.Content)
	}
}

func TestOpenAIMalformedArgsKeepsGoodCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"on it","tool_calls":[` +
			`{"id":"c1","function":{"name":"glob","arguments":"{\"pattern\":\"*.go\"}"}},` +
			`{"id":"c2","function":{"name":"write_file","arguments":"{not json}"}}]}}]}`))
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}},
		[]ToolDef{{Name: "glob"}, {Name: "write_file"}})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("good call must survive the bad one: %+v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Args["pattern"] != "*.go" {
		t.Fatalf("good call args damaged: %+v", resp.ToolCalls[0])
	}
	if !strings.Contains(resp.Content, "write_file") {
		t.Fatalf("prose must note the bad call, got %q", resp.Content)
	}
}

func TestOpenAIRetry429Then200(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
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

func TestOpenAINoRetryOn401(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
	if _, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d, want 1 (fail fast)", attempts)
	}
}

func TestOpenAIEmptyReplyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""}}]}`))
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
	_, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error for empty reply")
	}
}

func TestOpenAIUsageParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`))
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Usage != (Usage{Prompt: 10, Completion: 20, Total: 30}) {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

func TestOpenAITruncationLengthFlagged(t *testing.T) {
	truncated := func(t *testing.T, body string) Response {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		defer srv.Close()
		p := NewOpenAI("test-model", srv.URL, "test-key")
		resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}},
			[]ToolDef{{Name: "write_file"}})
		if err != nil {
			t.Fatalf("chat: %v", err)
		}
		return resp
	}
	// finish_reason=length: arguments may be cut mid-JSON — flag it.
	got := truncated(t, `{"choices":[{"finish_reason":"length","message":{"content":"","tool_calls":[{"id":"c1","function":{"name":"write_file","arguments":"{\"path\":\"a.txt\"}"}}]}}]}`)
	if !got.Truncated {
		t.Fatal("finish_reason=length must set Truncated")
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("truncated calls are still returned (the loop fails them closed): %+v", got.ToolCalls)
	}
	// Normal stop: no flag.
	got = truncated(t, `{"choices":[{"finish_reason":"stop","message":{"content":"","tool_calls":[{"id":"c1","function":{"name":"write_file","arguments":"{\"path\":\"a.txt\"}"}}]}}]}`)
	if got.Truncated {
		t.Fatal("finish_reason=stop must not set Truncated")
	}
	// Omitted finish_reason (some compatible servers): no flag.
	got = truncated(t, `{"choices":[{"message":{"content":"hi"}}]}`)
	if got.Truncated {
		t.Fatal("absent finish_reason must not set Truncated")
	}
}

func TestOpenAIUsageAbsentYieldsZeros(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Usage != (Usage{}) {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

func TestNewOpenAIEnvBase(t *testing.T) {
	// Regression: $OPENAI_BASE_URL was documented but ignored — requests
	// silently went to api.openai.com (caught live against Zen).
	t.Setenv("OPENAI_BASE_URL", "https://example-proxy.invalid/v1/")
	if got := NewOpenAI("m", "", "k").Base; got != "https://example-proxy.invalid/v1" {
		t.Fatalf("env base not honored (trailing slash must trim): %q", got)
	}
	t.Setenv("OPENAI_BASE_URL", "")
	if got := NewOpenAI("m", "", "k").Base; got != "https://api.openai.com/v1" {
		t.Fatalf("default base changed: %q", got)
	}
	if got := NewOpenAI("m", "https://flag.example/v1", "k").Base; got != "https://flag.example/v1" {
		t.Fatalf("explicit base must beat env: %q", got)
	}
}

func TestOpenAIEmptyChoicesRetried(t *testing.T) {
	// Flaky gateways answer 200 with an empty choices array (observed
	// live on free-tier proxies): the call must retry like any other
	// transient failure instead of killing the turn.
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		if attempts == 1 {
			_, _ = w.Write([]byte(`{"choices":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"recovered"}}]}`))
	}))
	defer srv.Close()
	p := NewOpenAI("test-model", srv.URL, "test-key")
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("empty-then-valid must recover: %v", err)
	}
	if resp.Content != "recovered" || attempts != 2 {
		t.Fatalf("content=%q attempts=%d, want recovered/2", resp.Content, attempts)
	}
}
