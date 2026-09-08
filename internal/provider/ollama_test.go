package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseContentCallsSingle(t *testing.T) {
	calls := parseContentCalls("{\n  \"name\": \"write_file\",\n  \"arguments\": {\"path\": \"a.txt\", \"content\": \"hi\"}\n}")
	if len(calls) != 1 || calls[0].Name != "write_file" || calls[0].Args["path"] != "a.txt" {
		t.Fatalf("got %+v", calls)
	}
}

func TestParseContentCallsFenced(t *testing.T) {
	calls := parseContentCalls("```json\n{\"name\": \"glob\", \"arguments\": {\"pattern\": \"*.go\"}}\n```")
	if len(calls) != 1 || calls[0].Name != "glob" {
		t.Fatalf("got %+v", calls)
	}
}

func TestParseContentCallsProseReturnsNil(t *testing.T) {
	if calls := parseContentCalls("Both files are written and verified."); len(calls) != 0 {
		t.Fatalf("prose must not parse as calls: %+v", calls)
	}
}

func TestParseContentCallsConcatenated(t *testing.T) {
	// Narrowed acceptance (strict nothing-but-JSON): back-to-back
	// objects are NOT a single value or an array, so they stay prose.
	// Previously this parsed as 2 calls — behavior change is intended.
	in := "{\"name\": \"write_file\", \"arguments\": {\"path\": \"a.txt\", \"content\": \"x\"}}\n" +
		"{\"name\": \"write_file\", \"arguments\": {\"path\": \"b.txt\", \"content\": \"y\"}}"
	if calls := parseContentCalls(in); len(calls) != 0 {
		t.Fatalf("concatenated objects must stay prose, got %+v", calls)
	}
}

func TestParseContentCallsProseWithSnippetStaysProse(t *testing.T) {
	in := "Sure, I'll write it:\n{\"name\": \"write_file\", \"arguments\": {\"path\": \"a.txt\", \"content\": \"hi\"}}\nDone!"
	if calls := parseContentCalls(in); len(calls) != 0 {
		t.Fatalf("prose + snippet must NOT dispatch, got %+v", calls)
	}
}

func TestParseContentCallsLeadingProseStaysProse(t *testing.T) {
	in := "Here are the calls: {\"name\": \"glob\", \"arguments\": {\"pattern\": \"*.go\"}}"
	if calls := parseContentCalls(in); len(calls) != 0 {
		t.Fatalf("leading prose must NOT dispatch, got %+v", calls)
	}
}

func TestParseContentCallsArray(t *testing.T) {
	in := "[{\"name\": \"glob\", \"arguments\": {\"pattern\": \"*.go\"}}, {\"name\": \"read_file\", \"arguments\": {\"path\": \"a.txt\"}}]"
	calls := parseContentCalls(in)
	if len(calls) != 2 || calls[0].Name != "glob" || calls[1].Args["path"] != "a.txt" {
		t.Fatalf("got %+v", calls)
	}
}

func TestParseContentCallsTrailingGarbageStaysProse(t *testing.T) {
	in := "{\"name\": \"glob\", \"arguments\": {\"pattern\": \"*.go\"}} trailing words"
	if calls := parseContentCalls(in); len(calls) != 0 {
		t.Fatalf("trailing prose must NOT dispatch, got %+v", calls)
	}
}

func TestParseContentCallsStringifiedArgs(t *testing.T) {
	calls := parseContentCalls("{\"name\": \"read_file\", \"arguments\": \"{\\\"path\\\": \\\"a.txt\\\"}\"}")
	if len(calls) != 1 || calls[0].Args["path"] != "a.txt" {
		t.Fatalf("got %+v", calls)
	}
}

func TestOllamaUsageParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"hi"},"prompt_eval_count":12,"eval_count":34}`))
	}))
	defer srv.Close()
	p := NewOllama("test-model")
	p.Host = srv.URL
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Usage != (Usage{Prompt: 12, Completion: 34, Total: 46}) {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

func TestOllamaCapturesThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"hi","thinking":"considering x"},"done":true}`))
	}))
	defer srv.Close()
	p := NewOllama("test-model")
	p.Host = srv.URL
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Content != "hi" || resp.Thinking != "considering x" {
		t.Fatalf("content=%q thinking=%q", resp.Content, resp.Thinking)
	}
}

func TestOllamaOmitsToolsWhenEmpty(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"hi"}}`))
	}))
	defer srv.Close()
	p := NewOllama("test-model")
	p.Host = srv.URL
	if _, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if _, present := seen["tools"]; present {
		t.Fatalf("tools key must be absent when empty, got: %v", seen)
	}
}

func TestOllamaRetry429Then200(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"message":{"content":"hi"}}`))
	}))
	defer srv.Close()
	p := NewOllama("test-model")
	p.Host = srv.URL
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

func TestOllamaNoRetryOn401(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"nope"}`))
	}))
	defer srv.Close()
	p := NewOllama("test-model")
	p.Host = srv.URL
	if _, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d, want 1 (fail fast)", attempts)
	}
}

func TestOllamaTruncationFlagged(t *testing.T) {
	truncated := func(t *testing.T, body string) Response {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		defer srv.Close()
		p := NewOllama("test-model")
		p.Host = srv.URL
		resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}},
			[]ToolDef{{Name: "write_file", Description: "write", Schema: map[string]any{"type": "object"}}})
		if err != nil {
			t.Fatalf("chat: %v", err)
		}
		return resp
	}
	// done_reason=length: arguments may be cut mid-JSON — flag it.
	if got := truncated(t, `{"message":{"content":"","tool_calls":[{"function":{"name":"write_file","arguments":{"path":"a.txt"}}}]} ,"done":true,"done_reason":"length"}`); !got.Truncated {
		t.Fatal("done_reason=length must set Truncated")
	}
	// Explicit done=false: the reply stopped early — flag it.
	if got := truncated(t, `{"message":{"content":"","tool_calls":[{"function":{"name":"write_file","arguments":{"path":"a.txt"}}}]} ,"done":false}`); !got.Truncated {
		t.Fatal("done=false must set Truncated")
	}
	// Normal completion: no flag.
	if got := truncated(t, `{"message":{"content":"hi"},"done":true,"done_reason":"stop"}`); got.Truncated {
		t.Fatal("done=true/stop must not set Truncated")
	}
	// Omitted done fields (older mocks/proxies): no signal, no flag.
	if got := truncated(t, `{"message":{"content":"hi"}}`); got.Truncated {
		t.Fatal("absent done fields must not set Truncated")
	}
}

func TestOllamaUsageAbsentYieldsZeros(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"hi"}}`))
	}))
	defer srv.Close()
	p := NewOllama("test-model")
	p.Host = srv.URL
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Usage != (Usage{}) {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

func TestOllamaCloudAuthHeader(t *testing.T) {
	// $OLLAMA_API_KEY must ride as Bearer (cloud API); absent without it.
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"ok"},"done":true}`))
	}))
	defer srv.Close()
	p := NewOllama("m")
	p.Host = srv.URL
	t.Setenv("OLLAMA_API_KEY", "secret-1")
	if _, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if got != "Bearer secret-1" {
		t.Fatalf("auth header = %q, want Bearer secret-1", got)
	}
}

func chatOnce(t *testing.T, payload string) Response {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	p := NewOllama("test-model")
	p.Host = srv.URL
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	return resp
}

func TestOllamaReasoningContentCaptured(t *testing.T) {
	resp := chatOnce(t, `{"message":{"content":"hi","reasoning_content":"weighing x"},"done":true}`)
	if resp.Content != "hi" || resp.Thinking != "weighing x" {
		t.Fatalf("content=%q thinking=%q", resp.Content, resp.Thinking)
	}
}

func TestOllamaInlineThinkExtracted(t *testing.T) {
	resp := chatOnce(t, `{"message":{"content":"<think>plan: check x</think>Read the file"},"done":true}`)
	if resp.Thinking != "plan: check x" {
		t.Fatalf("thinking=%q", resp.Thinking)
	}
	if resp.Content != "Read the file" {
		t.Fatalf("reasoning must leave prose, content=%q", resp.Content)
	}
}

func TestOllamaThinkWrappedCallStillDispatches(t *testing.T) {
	// A reasoning block around the call JSON must not demote a real
	// tool call to prose under the strict content-call gate.
	resp := chatOnce(t, `{"message":{"content":"<think>deciding: needs a read</think>{\"name\":\"read_file\",\"arguments\":{\"path\":\"a.txt\"}}"},"done":true}`)
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "read_file" {
		t.Fatalf("calls=%+v content=%q thinking=%q", resp.ToolCalls, resp.Content, resp.Thinking)
	}
	if resp.Thinking != "deciding: needs a read" {
		t.Fatalf("thinking=%q", resp.Thinking)
	}
}

func TestOllamaUnclosedThinkTakesRemainder(t *testing.T) {
	// Truncated output: partial reasoning stays visible as thinking
	// instead of leaking into prose.
	resp := chatOnce(t, `{"message":{"content":"Working on it <think>partial plan"},"done":true}`)
	if resp.Thinking != "partial plan" {
		t.Fatalf("thinking=%q", resp.Thinking)
	}
	if resp.Content != "Working on it" {
		t.Fatalf("content=%q", resp.Content)
	}
}

func TestOllamaNoThinkingFallback(t *testing.T) {
	resp := chatOnce(t, `{"message":{"content":"just prose"},"done":true}`)
	if resp.Thinking != "" {
		t.Fatalf("plain models must leave thinking empty, got %q", resp.Thinking)
	}
	if resp.Content != "just prose" {
		t.Fatalf("content=%q", resp.Content)
	}
}

func TestSplitThinkTagsUnit(t *testing.T) {
	prose, thinking := splitThinkTags("no tags here")
	if prose != "no tags here" || thinking != "" {
		t.Fatalf("tagless content must pass through byte-identical: %q %q", prose, thinking)
	}
	prose, thinking = splitThinkTags("<THINK>caps</THINK>after")
	if thinking != "caps" || prose != "after" {
		t.Fatalf("tags match case-insensitively: %q %q", prose, thinking)
	}
	prose, thinking = splitThinkTags("a<think>one</think>b<think>two</think>c")
	if thinking != "one\n\ntwo" || prose != "abc" {
		t.Fatalf("multi-block: %q %q", prose, thinking)
	}
	prose, thinking = splitThinkTags("<think></think>kept")
	if thinking != "" || prose != "kept" {
		t.Fatalf("empty blocks vanish silently: %q %q", prose, thinking)
	}
}
