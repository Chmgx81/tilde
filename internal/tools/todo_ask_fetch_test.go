package tools

import (
	"context"
	"strings"
	"testing"
)

func TestTodoWriteAddListDone(t *testing.T) {
	m := &TodoManager{}
	tw := &TodoWrite{Mgr: m}
	if out, err := tw.Exec(context.Background(), map[string]any{"op": "add", "text": "fix scrub"}); err != nil || !strings.Contains(out, "fix scrub") {
		t.Fatalf("add: out=%q err=%v", out, err)
	}
	out, err := tw.Exec(context.Background(), map[string]any{"op": "list"})
	if err != nil || !strings.Contains(out, "1:") {
		t.Fatalf("list: out=%q err=%v", out, err)
	}
	if _, err := tw.Exec(context.Background(), map[string]any{"op": "done", "id": float64(1)}); err != nil {
		t.Fatalf("done: %v", err)
	}
	out, _ = tw.Exec(context.Background(), map[string]any{"op": "list"})
	if !strings.Contains(out, "[x]") {
		t.Fatalf("done mark missing: %q", out)
	}
}

func TestAskUserNilDenies(t *testing.T) {
	a := &Ask{}
	out, err := a.Exec(context.Background(), map[string]any{"question": "proceed?"})
	if err != nil {
		t.Fatalf("ask nil err: %v", err)
	}
	if !strings.Contains(out, "denied") {
		t.Fatalf("nil callback must deny: %q", out)
	}
}

func TestWebFetchDenyAndBadURL(t *testing.T) {
	w := &WebFetch{}
	out, err := w.Exec(context.Background(), map[string]any{"url": "https://example.com"})
	if err != nil || !strings.Contains(out, "denied") {
		t.Fatalf("default-deny: out=%q err=%v", out, err)
	}
	w2 := &WebFetch{AllowNet: func() bool { return true }}
	if _, err := w2.Exec(context.Background(), map[string]any{"url": "ftp://x/y"}); err == nil {
		t.Fatalf("non-http must fail")
	}
}
