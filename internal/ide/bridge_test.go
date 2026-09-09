package ide

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// scripted RunFunc: echoes prompts, fails on "fail", oversizes on "big".
func testBridge() *Bridge {
	return New([]string{"read", "write", "shell"}, func(ctx context.Context, prompt string) (string, error) {
		switch prompt {
		case "fail":
			return "", errors.New("boom")
		case "big":
			return strings.Repeat("x", 5000), nil
		default:
			return "echo:" + prompt, nil
		}
	})
}

// serve runs Serve over input and decodes each output line.
func serve(t *testing.T, b *Bridge, input string) []Response {
	t.Helper()
	var out bytes.Buffer
	if err := b.Serve(strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var resps []Response
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r Response
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		resps = append(resps, r)
	}
	return resps
}

// result re-decodes a Response result into a map for assertions.
func result(t *testing.T, r Response) map[string]any {
	t.Helper()
	raw, err := json.Marshal(r.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return m
}

func reqLine(id any, method, params string) string {
	if params == "" {
		params = "{}"
	}
	return fmt.Sprintf(`{"id":%v,"method":%q,"params":%s}`, id, method, params)
}

func TestInitialize(t *testing.T) {
	resps := serve(t, testBridge(), reqLine(1, "initialize", "")+"\n")
	if len(resps) != 1 || !resps[0].OK {
		t.Fatalf("initialize not ok: %+v", resps)
	}
	m := result(t, resps[0])
	if m["version"] == "" {
		t.Fatal("initialize: empty version")
	}
	if m["protocol"] != float64(1) {
		t.Fatalf("initialize: protocol=%v, want 1", m["protocol"])
	}
	tools, _ := m["tools"].([]any)
	if len(tools) != 3 || tools[0] != "read" {
		t.Fatalf("initialize: tools=%v, want injected [read write shell]", m["tools"])
	}
}

func TestHealth(t *testing.T) {
	resps := serve(t, testBridge(), reqLine(2, "health", "")+"\n")
	if len(resps) != 1 || !resps[0].OK {
		t.Fatalf("health not ok: %+v", resps)
	}
	m := result(t, resps[0])
	if m["ok"] != true || m["sandbox"] == nil || m["backend"] == nil {
		t.Fatalf("health: missing keys in %v", m)
	}
}

func TestCreateChatHistoryFlow(t *testing.T) {
	b := testBridge()
	got := serve(t, b, reqLine(1, "session.create", "")+"\n")
	if !got[0].OK {
		t.Fatalf("create: %+v", got[0])
	}
	sid, _ := result(t, got[0])["session_id"].(string)
	if len(sid) != 16 {
		t.Fatalf("create: session_id=%q, want 16 random hex chars", sid)
	}

	got = serve(t, b, reqLine(2, "session.chat", fmt.Sprintf(`{"session_id":%q,"prompt":"hello"}`, sid))+"\n")
	if !got[0].OK {
		t.Fatalf("chat: %+v", got[0])
	}
	if m := result(t, got[0]); m["text"] != "echo:hello" || m["session_id"] != sid {
		t.Fatalf("chat result=%v", m)
	}

	got = serve(t, b, reqLine(3, "session.history", `{"limit":5}`)+"\n")
	if !got[0].OK {
		t.Fatalf("history: %+v", got[0])
	}
	m := result(t, got[0])
	if m["count"] != float64(1) {
		t.Fatalf("history count=%v, want 1", m["count"])
	}
	entries, _ := m["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("history entries=%v", m["entries"])
	}
	e, _ := entries[0].(map[string]any)
	if e["text"] != "echo:hello" || e["session_id"] != sid {
		t.Fatalf("history entry=%v", e)
	}
}

func TestChatErrors(t *testing.T) {
	b := testBridge()
	got := serve(t, b, reqLine(1, "session.create", "")+"\n")
	sid, _ := result(t, got[0])["session_id"].(string)

	cases := []struct {
		name   string
		params string
	}{
		{"unknown session", `{"session_id":"deadbeefdeadbeef","prompt":"hi"}`},
		{"empty prompt", fmt.Sprintf(`{"session_id":%q,"prompt":"  "}`, sid)},
		{"run error", fmt.Sprintf(`{"session_id":%q,"prompt":"fail"}`, sid)},
		{"bad params", `{"session_id":42}`},
	}
	for _, c := range cases {
		got := serve(t, b, reqLine(2, "session.chat", c.params)+"\n")
		if len(got) != 1 || got[0].OK {
			t.Errorf("%s: want error, got %+v", c.name, got)
		}
	}
	// Errors store nothing.
	got = serve(t, b, reqLine(3, "session.history", "")+"\n")
	if m := result(t, got[0]); m["count"] != float64(0) {
		t.Fatalf("history after errors: count=%v, want 0", m["count"])
	}
}

func TestUnknownMethod(t *testing.T) {
	resps := serve(t, testBridge(), reqLine(9, "frobnicate", "")+"\n")
	if len(resps) != 1 || resps[0].OK {
		t.Fatalf("want error, got %+v", resps)
	}
	for _, m := range validMethods {
		if !strings.Contains(resps[0].Error, m) {
			t.Fatalf("error %q does not name valid method %q", resps[0].Error, m)
		}
	}
}

func TestMalformedLineKeepsLoop(t *testing.T) {
	resps := serve(t, testBridge(), strings.Join([]string{
		reqLine(1, "health", ""),
		`this is not json`,
		`{"id":2,"method":`,
		reqLine(3, "initialize", ""),
	}, "\n")+"\n")
	if len(resps) != 4 {
		t.Fatalf("want 4 responses, got %d: %+v", len(resps), resps)
	}
	if !resps[0].OK || resps[1].OK || resps[2].OK || !resps[3].OK {
		t.Fatalf("malformed lines must error without exiting: %+v", resps)
	}
}

func TestEOFExit(t *testing.T) {
	var out bytes.Buffer
	if err := testBridge().Serve(strings.NewReader(""), &out); err != nil {
		t.Fatalf("Serve on EOF: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("want no output on EOF, got %q", out.String())
	}
	// No trailing newline on the last line is still one request, then EOF.
	resps := serve(t, testBridge(), strings.TrimSuffix(reqLine(1, "health", ""), "\n"))
	if len(resps) != 1 || !resps[0].OK {
		t.Fatalf("final line without newline: %+v", resps)
	}
}

func TestBusy(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	b := New(nil, func(ctx context.Context, prompt string) (string, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	sid := func() string {
		r := b.handle(Request{ID: 0, Method: "session.create"})
		if !r.OK {
			t.Fatalf("create: %+v", r)
		}
		m, _ := r.Result.(map[string]any)
		return m["session_id"].(string)
	}()
	chat := func(id int) Request {
		return Request{ID: id, Method: "session.chat", Params: json.RawMessage(
			fmt.Sprintf(`{"session_id":%q,"prompt":"hi"}`, sid),
		)}
	}
	first := make(chan Response, 1)
	go func() { first <- b.handle(chat(1)) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first chat never entered RunFunc")
	}
	second := b.handle(chat(2))
	if second.OK || !strings.Contains(second.Error, "busy") {
		t.Fatalf("second chat while busy: %+v, want busy error", second)
	}
	close(release)
	if r := <-first; !r.OK {
		t.Fatalf("first chat: %+v", r)
	}
}

func TestHistoryCapAndTruncate(t *testing.T) {
	b := testBridge()
	got := serve(t, b, reqLine(0, "session.create", "")+"\n")
	sid, _ := result(t, got[0])["session_id"].(string)

	var sb strings.Builder
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&sb, "%s\n", reqLine(i+1, "session.chat", fmt.Sprintf(`{"session_id":%q,"prompt":"m%d"}`, sid, i)))
	}
	serve(t, b, sb.String())
	got = serve(t, b, reqLine(99, "session.history", "")+"\n")
	m := result(t, got[0])
	if m["count"] != float64(20) {
		t.Fatalf("history count=%v, want capped 20", m["count"])
	}
	entries, _ := m["entries"].([]any)
	first, _ := entries[0].(map[string]any)
	if first["text"] != "echo:m5" {
		t.Fatalf("oldest kept=%v, want echo:m5 (25 chats, cap 20)", first["text"])
	}

	serve(t, b, reqLine(100, "session.chat", fmt.Sprintf(`{"session_id":%q,"prompt":"big"}`, sid))+"\n")
	got = serve(t, b, reqLine(101, "session.history", `{"n":1}`)+"\n")
	entries, _ = result(t, got[0])["entries"].([]any)
	last, _ := entries[len(entries)-1].(map[string]any)
	if n := len(last["text"].(string)); n != 4*1024 {
		t.Fatalf("stored text len=%d, want truncated %d", n, 4*1024)
	}
}

func TestChatTimeout(t *testing.T) {
	old := chatTimeout
	chatTimeout = 50 * time.Millisecond
	defer func() { chatTimeout = old }()

	b := New(nil, func(ctx context.Context, prompt string) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
			return "too late", nil
		}
	})
	r := b.handle(Request{ID: 0, Method: "session.create"})
	m, _ := r.Result.(map[string]any)
	sid := m["session_id"].(string)
	got := b.handle(Request{ID: 1, Method: "session.chat", Params: json.RawMessage(
		fmt.Sprintf(`{"session_id":%q,"prompt":"slow"}`, sid),
	)})
	if got.OK || !strings.Contains(got.Error, "deadline") {
		t.Fatalf("slow chat: %+v, want deadline error", got)
	}
}
