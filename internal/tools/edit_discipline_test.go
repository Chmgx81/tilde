package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLines(t *testing.T, root, name string, n int) string {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	content := strings.TrimRight(b.String(), "\n")
	if err := os.WriteFile(filepath.Join(root, name), []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return content
}

func TestEditDisciplineWarnsOnWholesaleRewrite(t *testing.T) {
	root := t.TempDir()
	seen := NewSeenMap(root)
	orig := writeLines(t, root, "big.txt", 40)
	if _, err := (&ReadFile{Root: root, Seen: seen}).Exec(context.Background(), map[string]any{"path": "big.txt"}); err != nil {
		t.Fatal(err)
	}
	out, err := (&EditFile{Root: root, Seen: seen}).Exec(context.Background(), map[string]any{
		"path": "big.txt", "old_string": orig, "new_string": strings.ReplaceAll(orig, "line", "row"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "edit discipline") || !strings.Contains(out, "touched ~") {
		t.Fatalf("wholesale rewrite should warn, got tail: %q", out[max(0, len(out)-200):])
	}
}

func TestEditDisciplineSmallEditSilent(t *testing.T) {
	root := t.TempDir()
	seen := NewSeenMap(root)
	writeLines(t, root, "big.txt", 40)
	ctx := context.Background()
	if _, err := (&ReadFile{Root: root, Seen: seen}).Exec(ctx, map[string]any{"path": "big.txt"}); err != nil {
		t.Fatal(err)
	}
	out, err := (&EditFile{Root: root, Seen: seen}).Exec(ctx, map[string]any{
		"path": "big.txt", "old_string": "line 1\n", "new_string": "LINE ONE\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "edit discipline") {
		t.Fatalf("a one-line edit must not warn, got tail: %q", out[max(0, len(out)-200):])
	}
}

func TestEditDisciplineWarnsOnThrash(t *testing.T) {
	root := t.TempDir()
	seen := NewSeenMap(root)
	writeLines(t, root, "churn.txt", 10)
	ctx := context.Background()
	if _, err := (&ReadFile{Root: root, Seen: seen}).Exec(ctx, map[string]any{"path": "churn.txt"}); err != nil {
		t.Fatal(err)
	}
	ef := &EditFile{Root: root, Seen: seen}
	var last string
	for i := 1; i <= editThrashWarn; i++ {
		oldS := fmt.Sprintf("line %d\n", i)
		newS := fmt.Sprintf("line %d edited\n", i)
		out, err := ef.Exec(ctx, map[string]any{"path": "churn.txt", "old_string": oldS, "new_string": newS})
		if err != nil {
			t.Fatal(err)
		}
		last = out
	}
	if !strings.Contains(last, "edited") || !strings.Contains(last, "edit discipline") {
		t.Fatalf("repeated edits should warn at the threshold, got tail: %q", last[max(0, len(last)-200):])
	}
}
