package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHeadlessPlainNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if !headlessPlain() {
		t.Fatal("NO_COLOR=1 must degrade headless glyphs")
	}
}

func TestConflictingFlags(t *testing.T) {
	if !conflictingFlags("do x", true, false) {
		t.Fatal("prompt+resume must conflict")
	}
	if !conflictingFlags("do x", false, true) {
		t.Fatal("prompt+eval must conflict")
	}
	if !conflictingFlags("do x", true, true) {
		t.Fatal("prompt+resume+eval must conflict")
	}
	if conflictingFlags("do x", false, false) {
		t.Fatal("prompt alone must not conflict")
	}
	if conflictingFlags("", true, true) {
		t.Fatal("resume+eval without prompt must not conflict here")
	}
}

func TestHeadlessExitCode(t *testing.T) {
	if got := headlessExitCode(nil, "", false); got != 0 {
		t.Fatalf("clean success = 0, got %d", got)
	}
	if got := headlessExitCode(nil, "", true); got != 5 {
		t.Fatalf("deny-tier hit = 5, got %d", got)
	}
	doom := fmt.Errorf("Same call (shell_command) issued 3 times in a row with no progress — reverting to Plan mode. Nothing further will be changed.")
	if got := headlessExitCode(doom, "", false); got != 4 {
		t.Fatalf("doom handoff = 4, got %d", got)
	}
	cap := fmt.Errorf("Iteration cap (25) reached — stopping cheap instead of looping.")
	if got := headlessExitCode(cap, "", false); got != 4 {
		t.Fatalf("iteration cap = 4, got %d", got)
	}
	if got := headlessExitCode(fmt.Errorf("boom"), "x\n\n[HANDOFF TO PLAN: stuck]", false); got != 4 {
		t.Fatalf("HANDOFF text marker = 4, got %d", got)
	}
	if got := headlessExitCode(fmt.Errorf("ollama: POST http://localhost:11434/api/chat: refused"), "", false); got != 3 {
		t.Fatalf("provider error = 3, got %d", got)
	}
	if got := headlessExitCode(fmt.Errorf("openai: status 429 for model %q", "x"), "", false); got != 3 {
		t.Fatalf("provider error = 3, got %d", got)
	}
	if got := headlessExitCode(fmt.Errorf("context canceled"), "", false); got != 1 {
		t.Fatalf("uncategorized = 1, got %d", got)
	}
}

func TestNewestUnclosedSession(t *testing.T) {
	if got := newestUnclosedSession(time.Time{}); got != "" {
		t.Fatalf("zero clean time must stay silent, got %q", got)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".tilde", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	old := filepath.Join(dir, "session_old.jsonl")
	recent := filepath.Join(dir, "session_new.jsonl")
	os.WriteFile(old, []byte("{}\n"), 0o600)
	os.WriteFile(recent, []byte("{}\n"), 0o600)
	os.Chtimes(old, base, base)
	os.Chtimes(recent, base.Add(30*time.Minute), base.Add(30*time.Minute))
	// Clean marker between the two: only the newer one is unclosed.
	if got := newestUnclosedSession(base.Add(10 * time.Minute)); got != "session_new" {
		t.Fatalf("want session_new, got %q", got)
	}
	// Clean marker newer than everything: silence.
	if got := newestUnclosedSession(time.Now()); got != "" {
		t.Fatalf("fresh clean marker must stay silent, got %q", got)
	}
}
