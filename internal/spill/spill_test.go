package spill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveWritesManagedFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TILDE_SPILL_DIR", dir)
	full := "line one\nline two\n"
	path := Save("read_file", full)
	if path == "" {
		t.Fatal("Save returned empty path with a writable dir")
	}
	if !strings.HasPrefix(path, dir+string(filepath.Separator)) {
		t.Fatalf("path %q not under spill dir %q", path, dir)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != full {
		t.Fatalf("content = %q, want %q", data, full)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("spill file mode = %o, want 600", st.Mode().Perm())
	}
	// A second save must not collide.
	if p2 := Save("read_file", full); p2 == path {
		t.Fatal("second Save reused the same path")
	}
}

func TestSaveToolNameSanitized(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TILDE_SPILL_DIR", dir)
	path := Save("shell-../../evil", "x")
	if path == "" {
		t.Fatal("Save returned empty path")
	}
	base := filepath.Base(path)
	if strings.Contains(base, "/") || strings.Contains(base, "..") {
		t.Fatalf("tool name leaked path separators into %q", base)
	}
}

func TestPruneRemovesOldKeepsNew(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TILDE_SPILL_DIR", dir)
	old := filepath.Join(dir, "old.txt")
	newer := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	removed, err := Prune(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old spill file should be gone")
	}
	if _, err := os.Stat(newer); err != nil {
		t.Fatal("new spill file must be kept")
	}
}

func TestSaveRefusesOversize(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TILDE_SPILL_DIR", dir)
	big := strings.Repeat("x", MaxBytes+1)
	if path := Save("t", big); path != "" {
		t.Fatalf("oversize payload must not spill, got %q", path)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("oversize spill left %d file(s)", len(entries))
	}
}

func TestPruneMissingDirIsNoop(t *testing.T) {
	removed, err := Prune(filepath.Join(t.TempDir(), "nope"), time.Hour)
	if err != nil || removed != 0 {
		t.Fatalf("missing dir: removed=%d err=%v", removed, err)
	}
}
