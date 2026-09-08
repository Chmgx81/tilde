package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpanDiffStatAndHunk(t *testing.T) {
	before := []string{"package a", "", "func Old() {}", ""}
	after := []string{"package a", "", "func New() {}", ""}
	got := SpanDiff("a.go", before, after, 2, 1, 1, "")
	lines := strings.Split(got, "\n")
	if lines[0] != "+1 -1" {
		t.Fatalf("stat line = %q, want +1 -1", lines[0])
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"--- a.go", "+++ a.go", "@@ -3,1 +3,1 @@", "-func Old() {}", "+func New() {}", " package a"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("payload missing %q:\n%s", want, got)
		}
	}
}

func TestSpanDiffCarriesNote(t *testing.T) {
	got := SpanDiff("a.go", []string{"x"}, []string{"y"}, 0, 1, 1, " (matched ignoring indentation — verify the result)")
	if !strings.HasPrefix(got, "+1 -1 (matched ignoring indentation") {
		t.Fatalf("repair note must ride the stat line:\n%s", got)
	}
}

func TestSpanDiffTruncatesGiantSpans(t *testing.T) {
	var before, after []string
	for i := 0; i < 300; i++ {
		before = append(before, "old")
		after = append(after, "new")
	}
	got := SpanDiff("big.go", before, after, 0, 300, 300, "")
	if !strings.Contains(got, "diff truncated") {
		t.Fatal("giant spans must truncate with notice")
	}
	if n := len(strings.Split(got, "\n")); n > diffCap+8 {
		t.Fatalf("bounded payload exceeded cap: %d lines", n)
	}
}

func TestEditFileEmitsDiffPayload(t *testing.T) {
	root := testRoot(t)
	os.WriteFile(filepath.Join(root, "e.go"), []byte("package a\n\nfunc Old() {}\n"), 0o644)
	led := NewSeenMap(root)
	r := &ReadFile{Root: root, Seen: led}
	if _, err := r.Exec(context.Background(), map[string]any{"path": "e.go"}); err != nil {
		t.Fatalf("read: %v", err)
	}
	e := &EditFile{Root: root, Seen: led}
	out, err := e.Exec(context.Background(), map[string]any{"path": "e.go", "old_string": "func Old() {}", "new_string": "func New() {}"})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	lines := strings.Split(out, "\n")
	if lines[0] != "+1 -1" {
		t.Fatalf("edit stat = %q, want line counts +1 -1", lines[0])
	}
	for _, want := range []string{"--- e.go", "+++ e.go", "@@", "-func Old() {}", "+func New() {}"} {
		if !strings.Contains(out, want) {
			t.Fatalf("edit payload missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "bytes") || strings.Contains(out, "Re-read") {
		t.Fatalf("prose receipt must be gone, got:\n%s", out)
	}
}

func TestWriteFileNewPayload(t *testing.T) {
	root := testRoot(t)
	led := NewSeenMap(root)
	w := &WriteFile{Root: root, Seen: led}
	out, err := w.Exec(context.Background(), map[string]any{"path": "n.go", "content": "package a\n\nfunc F() {}\n"})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	lines := strings.Split(out, "\n")
	if lines[0] != "+3 (new file: n.go)" {
		t.Fatalf("new-file stat = %q", lines[0])
	}
	if !strings.Contains(out, "package a") || !strings.Contains(out, "func F() {}") {
		t.Fatalf("new-file body must carry the content:\n%s", out)
	}
}

func TestWriteFileOverwriteEmitsDiff(t *testing.T) {
	root := testRoot(t)
	os.WriteFile(filepath.Join(root, "o.go"), []byte("package a\n"), 0o644)
	led := NewSeenMap(root)
	r := &ReadFile{Root: root, Seen: led}
	if _, err := r.Exec(context.Background(), map[string]any{"path": "o.go"}); err != nil {
		t.Fatalf("read: %v", err)
	}
	w := &WriteFile{Root: root, Seen: led}
	out, err := w.Exec(context.Background(), map[string]any{"path": "o.go", "content": "package b\n"})
	if err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if !strings.HasPrefix(out, "+1 -1") || !strings.Contains(out, "@@") {
		t.Fatalf("overwrite must render as an edit diff:\n%s", out)
	}
	if !strings.Contains(out, "overwrote existing file") {
		t.Fatalf("overwrite diff must say so:\n%s", out)
	}
}
