package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestAppendRedactsSecrets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	l, err := Open("redact-session")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	fake := "sk-abcdefghij1234567890XY"
	if err := l.Append("tool_result", map[string]any{"output": "key=" + fake}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	raw, err := os.ReadFile(l.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), fake) {
		t.Fatalf("session file leaks secret: %q", raw)
	}
	if !strings.Contains(string(raw), "<<REDACTED:openai>>") {
		t.Fatalf("session file missing redaction marker: %q", raw)
	}
	var e Entry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &e); err != nil {
		t.Fatalf("redacted line is not valid JSON: %v (%q)", err, raw)
	}
}

func TestOpenHealsPermsAndIDsUnique(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	l, err := Open("perm-session")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = l.Close()
	if err := os.Chmod(l.Path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	l2, err := Open("perm-session")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = l2.Close()
	fi, err := os.Stat(l.Path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", fi.Mode().Perm())
	}
	dirfi, err := os.Stat(filepath.Dir(l.Path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirfi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o, want 700", dirfi.Mode().Perm())
	}
	if a, b := NewID(), NewID(); a == b {
		t.Fatalf("NewID collision: %q", a)
	}
}
