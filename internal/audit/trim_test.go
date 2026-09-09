package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeAuditLine(t *testing.T, f *os.File, e AuditEvent) {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

// fixture writes path with two old events, one just-inside-window event,
// one recent event, plus a corrupt line and a blank line.
func fixture(t *testing.T, path string, window time.Duration) (oldTools []string) {
	t.Helper()
	now := time.Now().UTC()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	writeAuditLine(t, f, AuditEvent{TS: now.Add(-3 * window), Tool: "old1", Decision: "allow", RedactedArgsHash: "h1", Detail: "d1"})
	writeAuditLine(t, f, AuditEvent{TS: now.Add(-2 * window), Tool: "old2", Decision: "deny", RedactedArgsHash: "h2", Detail: "d2"})
	// 2s margin absorbs drift between fixture write and Trim's cutoff.
	writeAuditLine(t, f, AuditEvent{TS: now.Add(-window + 2*time.Second), Tool: "edge", Decision: "allow", RedactedArgsHash: "h3", Detail: "d3"})
	writeAuditLine(t, f, AuditEvent{TS: now, Tool: "fresh", Decision: "allow", RedactedArgsHash: "h4", Detail: "d4"})
	if _, err := f.WriteString("{this is not json\n\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return []string{"old1", "old2"}
}

func TestTrimCountsAndContent(t *testing.T) {
	window := 90 * 24 * time.Hour
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	fixture(t, path, window)

	kept, dropped, err := Trim(path, window)
	if err != nil {
		t.Fatalf("trim: %v", err)
	}
	// 2 age-expired + 1 corrupt = 3 dropped; edge + fresh = 2 kept.
	if kept != 2 {
		t.Fatalf("kept = %d, want 2", kept)
	}
	if dropped != 3 {
		t.Fatalf("dropped = %d, want 3", dropped)
	}

	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if len(got) != 2 || got[0].Tool != "edge" || got[1].Tool != "fresh" {
		t.Fatalf("remaining events wrong: %+v", got)
	}
	for _, e := range got {
		if e.RedactedArgsHash == "" || e.Detail == "" {
			t.Fatalf("trim mangled event: %+v", e)
		}
	}
}

func TestTrimPreservesPerms(t *testing.T) {
	window := 24 * time.Hour
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	fixture(t, path, window)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := Trim(path, window); err != nil {
		t.Fatalf("trim: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestTrimCorruptLinesDroppedWithCount(t *testing.T) {
	// Fully corrupt file: zero valid events, every line counts as dropped.
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(path, []byte("{bad\n[1,2,3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kept, dropped, err := Trim(path, 24*time.Hour)
	if err != nil {
		t.Fatalf("trim: %v", err)
	}
	if kept != 0 {
		t.Fatalf("kept = %d, want 0", kept)
	}
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2 (ReadAll tolerance count)", dropped)
	}
	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d events, want 0", len(got))
	}
}

func TestTrimNothingToDropLeavesFileAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	l, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustAppend(t, l, AuditEvent{Tool: "bash", Decision: "allow", RedactedArgsHash: "h", Detail: "fresh"})
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	kept, dropped, err := Trim(path, 24*time.Hour)
	if err != nil {
		t.Fatalf("trim: %v", err)
	}
	if kept != 1 || dropped != 0 {
		t.Fatalf("kept = %d dropped = %d, want 1/0", kept, dropped)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("no-op trim must not rewrite the file")
	}
}

func TestTrimMissingFileNoOp(t *testing.T) {
	kept, dropped, err := Trim(filepath.Join(t.TempDir(), "nope.jsonl"), 24*time.Hour)
	if err != nil {
		t.Fatalf("missing file must be no-op, got err: %v", err)
	}
	if kept != 0 || dropped != 0 {
		t.Fatalf("kept = %d dropped = %d, want 0/0", kept, dropped)
	}
}

func TestTrimEmptyPathErrors(t *testing.T) {
	if _, _, err := Trim("", 24*time.Hour); err == nil {
		t.Fatal("empty path must fail loud, got nil")
	}
}
