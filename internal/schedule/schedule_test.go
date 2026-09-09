package schedule

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func dueIDs(now time.Time, jobs []Job, s State) []string {
	var out []string
	for _, j := range Due(now, jobs, s) {
		out = append(out, j.ID)
	}
	return out
}

func has(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func TestDueIntervalElapsed(t *testing.T) {
	now := time.Now()
	jobs := []Job{{ID: "a", Prompt: "p", Every: time.Hour, Enabled: true}}
	s := State{"a": now.Add(-2 * time.Hour)}
	if ids := dueIDs(now, jobs, s); !has(ids, "a") {
		t.Fatalf("expected a due, got %v", ids)
	}
}

func TestDueNeverRun(t *testing.T) {
	now := time.Now()
	jobs := []Job{{ID: "a", Prompt: "p", Every: 24 * time.Hour, Enabled: true}}
	if ids := dueIDs(now, jobs, State{}); !has(ids, "a") {
		t.Fatalf("never-run job must be due, got %v", ids)
	}
}

func TestDueDisabled(t *testing.T) {
	now := time.Now()
	jobs := []Job{{ID: "a", Prompt: "p", Every: time.Hour, Enabled: false}}
	s := State{"a": now.Add(-48 * time.Hour)}
	if ids := dueIDs(now, jobs, s); len(ids) != 0 {
		t.Fatalf("disabled job must never be due, got %v", ids)
	}
	// Never-run but disabled: still not due.
	if ids := dueIDs(now, jobs, State{}); len(ids) != 0 {
		t.Fatalf("disabled never-run job must not be due, got %v", ids)
	}
}

func TestDueIntervalNotElapsed(t *testing.T) {
	now := time.Now()
	jobs := []Job{{ID: "a", Prompt: "p", Every: 24 * time.Hour, Enabled: true}}
	s := State{"a": now.Add(-time.Hour)}
	if ids := dueIDs(now, jobs, s); len(ids) != 0 {
		t.Fatalf("interval not elapsed: not due, got %v", ids)
	}
}

func TestDueAtBoundary(t *testing.T) {
	loc := time.Now().Location()
	// Today 09:00 local; now exactly at 09:00; last run yesterday 08:00
	// with a 24h interval (not elapsed) — the at trigger makes it due.
	now := time.Date(2026, 9, 9, 9, 0, 0, 0, loc)
	last := time.Date(2026, 9, 9, 8, 0, 0, 0, loc)
	jobs := []Job{{ID: "a", Prompt: "p", Every: 24 * time.Hour, At: "09:00", Enabled: true}}
	if ids := dueIDs(now, jobs, State{"a": last}); !has(ids, "a") {
		t.Fatalf("at boundary (now == today at) must be due")
	}
	// Same day but before at: not due.
	before := time.Date(2026, 9, 9, 8, 59, 0, 0, loc)
	if ids := dueIDs(before, jobs, State{"a": last}); len(ids) != 0 {
		t.Fatalf("before today's at: not due, got %v", ids)
	}
	// Already ran after today's at: not due.
	after := time.Date(2026, 9, 9, 10, 0, 0, 0, loc)
	ranToday := time.Date(2026, 9, 9, 9, 5, 0, 0, loc)
	if ids := dueIDs(after, jobs, State{"a": ranToday}); len(ids) != 0 {
		t.Fatalf("ran after today's at: not due, got %v", ids)
	}
}

func TestMarkRunRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".tilde", "schedule-state.json")
	now := time.Now().UTC().Truncate(time.Second)
	s, err := LoadState(path) // missing = empty
	if err != nil {
		t.Fatal(err)
	}
	s = MarkRun(s, "a", now)
	if err := SaveState(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	ts, ok := got["a"]
	if !ok {
		t.Fatalf("missing id after roundtrip: %v", got)
	}
	if !ts.Equal(now) {
		t.Fatalf("roundtrip mismatch: got %v want %v", ts, now)
	}
	// Due right after mark: not due.
	jobs := []Job{{ID: "a", Prompt: "p", Every: time.Hour, Enabled: true}}
	if ids := dueIDs(time.Now(), jobs, got); len(ids) != 0 {
		t.Fatalf("just ran: not due, got %v", ids)
	}
}

func TestLoadStrictUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.yaml")
	writeFile(t, path, "- id: a\n  prompt: p\n  every: 1h\n  enabled: true\n  bogus: 1\n")
	if _, err := LoadFile(path); err == nil {
		t.Fatal("unknown keys must be rejected")
	}
}

func TestLoadInvalidDuration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.yaml")
	writeFile(t, path, "- id: a\n  prompt: p\n  every: daily\n  enabled: true\n")
	if _, err := LoadFile(path); err == nil {
		t.Fatal("invalid duration must fail closed")
	}
}

func TestLoadEmptyIDPromptRefused(t *testing.T) {
	dir := t.TempDir()
	for i, body := range []string{
		"- id: ''\n  prompt: p\n  every: 1h\n  enabled: true\n",
		"- id: a\n  prompt: ''\n  every: 1h\n  enabled: true\n",
	} {
		path := filepath.Join(dir, filepath.FromSlash("sched.yaml"))
		_ = i
		writeFile(t, path, body)
		if _, err := LoadFile(path); err == nil {
			t.Fatalf("empty id/prompt must be refused (body %q)", body)
		}
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	jobs, err := LoadFile(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil || len(jobs) != 0 {
		t.Fatalf("missing file = empty, not error: %v %v", jobs, err)
	}
}

func TestAcquireLockExcludesOverlapAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".tilde", "run-due.lock")
	release, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("overlapping scheduler run must be refused with ErrLocked, got %v", err)
	}
	release()
	if again, err := AcquireLock(path); err != nil {
		t.Fatalf("lock must be reusable after release: %v", err)
	} else {
		again()
	}
}
