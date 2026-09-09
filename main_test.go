package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	if got := headlessExitCode(fmt.Errorf("anthropic: status 403 for model %q", "x"), "", false); got != 3 {
		t.Fatalf("provider error = 3, got %d", got)
	}
	if got := headlessExitCode(fmt.Errorf("gemini: status 429 for model %q", "x"), "", false); got != 3 {
		t.Fatalf("provider error = 3, got %d", got)
	}
	if got := headlessExitCode(fmt.Errorf("openrouter: status 429 for model %q", "x"), "", false); got != 3 {
		t.Fatalf("provider error = 3, got %d", got)
	}
	if got := headlessExitCode(fmt.Errorf("opencode: status 429 for model %q", "x"), "", false); got != 3 {
		t.Fatalf("provider error = 3, got %d", got)
	}
	if got := headlessExitCode(fmt.Errorf("context canceled"), "", false); got != 1 {
		t.Fatalf("uncategorized = 1, got %d", got)
	}
}

func TestRunExportCmdMissingSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := runExportCmd("nosuchsession", "")
	if err == nil {
		t.Fatal("missing session must fail loud, got nil")
	}
	if !strings.Contains(err.Error(), "no session") {
		t.Fatalf("error should name the missing session, got: %v", err)
	}
}

func TestRunExportCmdBadID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := runExportCmd("../evil", ""); err == nil {
		t.Fatal("traversal id must fail loud, got nil")
	} else if !strings.Contains(err.Error(), "bad session id") {
		t.Fatalf("error should name the bad id, got: %v", err)
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

func TestPluginLifecycleCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := t.TempDir()
	writeTestPluginSource(t, src)
	if err := runPluginCmd([]string{"plugin", "install", src}); err != nil {
		t.Fatalf("install command: %v", err)
	}
	if err := runPluginCmd([]string{"plugin", "disable", "demo-plugin"}); err != nil {
		t.Fatalf("disable command: %v", err)
	}
	if err := runPluginCmd([]string{"plugin", "enable", "demo-plugin"}); err != nil {
		t.Fatalf("enable command: %v", err)
	}
	if err := runPluginCmd([]string{"plugin", "verify", "../demo-plugin"}); err == nil {
		t.Fatal("verify command accepted traversal name")
	}
	if err := runPluginCmd([]string{"plugin", "remove", "demo-plugin"}); err != nil {
		t.Fatalf("remove command: %v", err)
	}
	if err := runPluginCmd([]string{"plugin", "remove", "demo-plugin"}); err == nil {
		t.Fatal("remove command silently accepted missing plugin")
	}
}

func writeTestPluginSource(t *testing.T, dir string) {
	t.Helper()
	writeTestFile(t, dir, "tilde-plugin.yaml", "name: demo-plugin\nversion: 1.2.3\ndescription: test plugin\nskills:\n  - skills/a.md\n")
	writeTestFile(t, dir, "skills/a.md", "skill\n")
}

func writeTestFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
