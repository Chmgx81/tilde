package session

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// age creates name in dir with mtime now-d and returns its full path.
func age(t *testing.T, dir, name string, d time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-d)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Stat(p)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatalf("stat %s: %v", p, err)
	return false
}

func TestPruneOldDeletedRecentKept(t *testing.T) {
	dir := t.TempDir()
	old := age(t, dir, "old.jsonl", 72*time.Hour)
	recent := age(t, dir, "recent.jsonl", time.Hour)

	deleted, err := Prune(dir, 24*time.Hour, 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if !slices.Equal(deleted, []string{"old.jsonl"}) {
		t.Fatalf("deleted = %q, want [old.jsonl]", deleted)
	}
	if exists(t, old) {
		t.Fatal("old.jsonl survives prune")
	}
	if !exists(t, recent) {
		t.Fatal("recent.jsonl wrongly pruned")
	}
}

func TestPruneBoundaryInclusive(t *testing.T) {
	dir := t.TempDir()
	window := 24 * time.Hour
	// Just inside the window (2s margin absorbs clock drift between
	// Chtimes and Prune's internal now): age < window, must be kept.
	inside := age(t, dir, "inside.jsonl", window-2*time.Second)
	// Safely outside the window: must go.
	outside := age(t, dir, "outside.jsonl", window+time.Hour)

	deleted, err := Prune(dir, window, 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if !slices.Equal(deleted, []string{"outside.jsonl"}) {
		t.Fatalf("deleted = %q, want [outside.jsonl]", deleted)
	}
	if !exists(t, inside) {
		t.Fatal("inside-window file wrongly pruned (boundary must keep)")
	}
	if exists(t, outside) {
		t.Fatal("outside-window file survives prune")
	}
}

func TestPruneSkipsDotfilesAndNonJSONL(t *testing.T) {
	dir := t.TempDir()
	// All safely older than the window; none may be touched.
	marker := age(t, dir, ".clean", 72*time.Hour)
	hidden := age(t, dir, ".hidden.jsonl", 72*time.Hour)
	txt := age(t, dir, "notes.txt", 72*time.Hour)
	bare := age(t, dir, "noext", 72*time.Hour)
	if err := os.Mkdir(filepath.Join(dir, "sub.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}

	deleted, err := Prune(dir, 24*time.Hour, 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted = %q, want none", deleted)
	}
	for _, p := range []string{marker, hidden, txt, bare} {
		if !exists(t, p) {
			t.Fatalf("%s wrongly pruned", p)
		}
	}
}

func TestPruneKeepMinFloor(t *testing.T) {
	dir := t.TempDir()
	// Three old files, newest-first: c > b > a by mtime.
	age(t, dir, "a.jsonl", 72*time.Hour)
	age(t, dir, "b.jsonl", 48*time.Hour)
	age(t, dir, "c.jsonl", 30*time.Hour)

	deleted, err := Prune(dir, 24*time.Hour, 2)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	// b and c are protected by the floor; only the oldest goes.
	if !slices.Equal(deleted, []string{"a.jsonl"}) {
		t.Fatalf("deleted = %q, want [a.jsonl]", deleted)
	}
	if !exists(t, filepath.Join(dir, "b.jsonl")) || !exists(t, filepath.Join(dir, "c.jsonl")) {
		t.Fatal("keepMin floor files wrongly pruned")
	}
}

func TestPruneMissingDirNoOp(t *testing.T) {
	deleted, err := Prune(filepath.Join(t.TempDir(), "does-not-exist"), 24*time.Hour, 0)
	if err != nil {
		t.Fatalf("missing dir must be no-op, got err: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted = %q, want none", deleted)
	}
}

func TestPruneEmptyDirErrors(t *testing.T) {
	if _, err := Prune("", 24*time.Hour, 0); err == nil {
		t.Fatal("empty dir must fail loud, got nil")
	}
}

func TestPruneDeletedSorted(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"z.jsonl", "m.jsonl", "a.jsonl"} {
		age(t, dir, n, 72*time.Hour)
	}
	deleted, err := Prune(dir, 24*time.Hour, 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if !slices.Equal(deleted, []string{"a.jsonl", "m.jsonl", "z.jsonl"}) {
		t.Fatalf("deleted = %q, want sorted", deleted)
	}
}
