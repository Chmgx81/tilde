package session

import "testing"

func TestAppendRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	l, err := Open("test-session")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := l.Append("user", map[string]any{"content": "hi"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := NewID(); len(got) < len("session_") {
		t.Fatalf("bad id: %q", got)
	}
}
