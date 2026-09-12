package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Regression: a traversal id must never reach the filesystem. Before the
// guard, `../../secret` would join to a path outside the sessions dir and
// copy that file's contents into a new branch.
func TestForkRejectsTraversalID(t *testing.T) {
	parent := t.TempDir()
	sessions := filepath.Join(parent, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	// A juicy file one level up that a traversal id would target.
	outside := filepath.Join(parent, "secret.jsonl")
	os.WriteFile(outside, []byte(`{"type":"user","data":{"x":1}}`+"\n"), 0o600)

	for _, id := range []string{
		"../secret",
		"../../etc/passwd",
		"a/b",
		"..",
		".",
		"",
		strings.Repeat("a", 65),
	} {
		if _, err := Fork(sessions, id, ""); err == nil {
			t.Errorf("Fork(%q) must refuse a traversal/oversized id", id)
		}
	}
	// And nothing was created.
	entries, err := os.ReadDir(sessions)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("refused fork left files behind: %v", entries)
	}
}

func TestValidID(t *testing.T) {
	for _, ok := range []string{"session_abc123", "A-b_9", "x"} {
		if !ValidID(ok) {
			t.Errorf("ValidID(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", "../x", "a\\b", "a b", "a.jsonl", strings.Repeat("a", 65)} {
		if ValidID(bad) {
			t.Errorf("ValidID(%q) = true, want false", bad)
		}
	}
}

// writeSrc writes entries with fixed timestamps so cutoff tests are
// deterministic. Returns the src id and the exact raw lines written.
func writeSrc(t *testing.T, dir, id string, stamps ...time.Time) []string {
	t.Helper()
	var sb strings.Builder
	var lines []string
	for i, ts := range stamps {
		e := Entry{TS: ts, Type: "user", Data: map[string]any{"n": i}}
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}
		lines = append(lines, string(b))
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return lines
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	trimmed := strings.TrimSuffix(string(raw), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// dirFiles returns basenames of regular files in dir (used to prove a
// failed fork left no half-written branch behind).
func dirFiles(t *testing.T, dir string) map[string]bool {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	out := map[string]bool{}
	for _, e := range ents {
		if e.Type().IsRegular() {
			out[e.Name()] = true
		}
	}
	return out
}

func checkMarker(t *testing.T, line, wantFrom, wantAt string) {
	t.Helper()
	var m struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("marker is not valid JSON: %v (%q)", err, line)
	}
	if m.Type != "fork" {
		t.Fatalf("marker type = %q, want fork", m.Type)
	}
	if m.Data["from"] != wantFrom || m.Data["at"] != wantAt {
		t.Fatalf("marker data = %v, want from=%q at=%q", m.Data, wantFrom, wantAt)
	}
}

var forkStamps = []time.Time{
	time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
}

func TestForkFullCopy(t *testing.T) {
	dir := t.TempDir()
	srcID := "session_src"
	srcLines := writeSrc(t, dir, srcID, forkStamps...)

	newID, err := Fork(dir, srcID, "")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if newID == "" || newID == srcID {
		t.Fatalf("bad new id: %q", newID)
	}
	got := readLines(t, filepath.Join(dir, newID+".jsonl"))
	if len(got) != len(srcLines)+1 {
		t.Fatalf("branch has %d lines, want %d + marker", len(got), len(srcLines))
	}
	for i, ln := range srcLines {
		if got[i] != ln {
			t.Fatalf("line %d altered by fork:\n got: %s\nwant: %s", i+1, got[i], ln)
		}
	}
	checkMarker(t, got[len(got)-1], srcID, "")

	// Source untouched.
	if again := readLines(t, filepath.Join(dir, srcID+".jsonl")); len(again) != len(srcLines) {
		t.Fatalf("source modified: %d lines, want %d", len(again), len(srcLines))
	}
}

func TestForkCutoff(t *testing.T) {
	dir := t.TempDir()
	srcID := "session_src"
	srcLines := writeSrc(t, dir, srcID, forkStamps...)

	// Cutoff exactly at the 2nd entry: inclusive — first two lines kept.
	newID, err := Fork(dir, srcID, "2026-01-02T00:00:00Z")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	got := readLines(t, filepath.Join(dir, newID+".jsonl"))
	if len(got) != 3 {
		t.Fatalf("branch has %d lines, want 2 + marker", len(got))
	}
	if got[0] != srcLines[0] || got[1] != srcLines[1] {
		t.Fatalf("branch prefix mismatch:\n got: %q\nwant first two src lines", got[:2])
	}
	for _, ln := range got[:2] {
		if strings.Contains(ln, "2026-01-03") {
			t.Fatalf("later line leaked into branch: %s", ln)
		}
	}
	checkMarker(t, got[2], srcID, "2026-01-02T00:00:00Z")
}

func TestForkUnknownID(t *testing.T) {
	dir := t.TempDir()
	before := dirFiles(t, dir)
	if _, err := Fork(dir, "session_nope", ""); err == nil {
		t.Fatal("fork unknown id: want error, got nil")
	} else if !strings.Contains(err.Error(), "session_nope") {
		t.Fatalf("error should name the id, got: %v", err)
	}
	if after := dirFiles(t, dir); len(after) != len(before) {
		t.Fatalf("failed fork left files behind: %v", after)
	}
}

func TestForkCorruptAborts(t *testing.T) {
	dir := t.TempDir()
	srcID := "session_src"
	writeSrc(t, dir, srcID, forkStamps[:2]...)
	f, err := os.OpenFile(filepath.Join(dir, srcID+".jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if _, err := f.WriteString("this is not json\n"); err != nil {
		t.Fatalf("append corrupt line: %v", err)
	}
	_ = f.Close()

	before := dirFiles(t, dir)
	_, err = Fork(dir, srcID, "")
	if err == nil {
		t.Fatal("fork corrupt src: want error, got nil")
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("error should name line 3, got: %v", err)
	}
	if after := dirFiles(t, dir); len(after) != len(before) {
		t.Fatalf("corrupt fork left a half-written branch: before=%v after=%v", before, after)
	}
}

func TestForkEmptyCutoffRefuses(t *testing.T) {
	dir := t.TempDir()
	srcID := "session_src"
	writeSrc(t, dir, srcID, forkStamps...)

	before := dirFiles(t, dir)
	_, err := Fork(dir, srcID, "2025-12-31T23:59:59Z") // older than first entry
	if err == nil {
		t.Fatal("fork empty cutoff: want error, got nil")
	}
	if !strings.Contains(err.Error(), "later cutoff") {
		t.Fatalf("error should suggest a later cutoff, got: %v", err)
	}
	if after := dirFiles(t, dir); len(after) != len(before) {
		t.Fatalf("refused fork left files behind: %v", after)
	}
}

func TestForkPerms(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	srcID := "session_src"
	writeSrc(t, dir, srcID, forkStamps...)

	newID, err := Fork(dir, srcID, "")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, newID+".jsonl"))
	if err != nil {
		t.Fatalf("stat branch: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("branch mode = %o, want 600", fi.Mode().Perm())
	}
	dirfi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirfi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o, want 700", dirfi.Mode().Perm())
	}
}
