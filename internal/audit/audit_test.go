package audit

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestAppendProducesValidJSONLines(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	events := []AuditEvent{
		{Tool: "bash", Decision: "allow", RedactedArgsHash: "abc123", Detail: "ls redacted"},
		{Tool: "write", Decision: "deny", RedactedArgsHash: "def456", Detail: "blocked path"},
	}
	for _, e := range events {
		if err := l.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	raw, err := os.ReadFile(l.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != len(events) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(events), raw)
	}
	for i, line := range lines {
		var got AuditEvent
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d is not valid JSON: %v (%q)", i, err, line)
		}
		if got.TS.IsZero() {
			t.Fatalf("line %d missing TS: %q", i, line)
		}
		if got.Tool != events[i].Tool || got.Decision != events[i].Decision ||
			got.RedactedArgsHash != events[i].RedactedArgsHash || got.Detail != events[i].Detail {
			t.Fatalf("line %d round-trip mismatch: got %+v want %+v", i, got, events[i])
		}
	}
}

func TestAppendNeverStoresRawSecret(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	fake := "sk-abcdefghij1234567890XY"
	// Caller contract: only hash + redacted detail reach Append. Even if a
	// secret slips into Detail, the scrubber must catch it on the way out.
	if err := l.Append(AuditEvent{
		Tool:             "webfetch",
		Decision:         "allow",
		RedactedArgsHash: "hash-of-redacted-args",
		Detail:           "note key=" + fake,
	}); err != nil {
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
		t.Fatalf("audit file leaks secret: %q", raw)
	}
	var got AuditEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &got); err != nil {
		t.Fatalf("redacted line is not valid JSON: %v (%q)", err, raw)
	}
	if !strings.Contains(got.Detail, "<<REDACTED:openai>>") {
		t.Fatalf("audit file missing redaction marker: %q", raw)
	}
}

func TestOpenHealsPerms(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = l.Close()
	if err := os.Chmod(l.Path, 0o644); err != nil {
		t.Fatalf("chmod file: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	l2, err := Open(dir)
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
	dirfi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirfi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o, want 700", dirfi.Mode().Perm())
	}
}
