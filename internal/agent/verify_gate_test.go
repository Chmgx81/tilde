package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tilde/internal/mode"
	"tilde/internal/policy"
	"tilde/internal/provider"
)

// writeThenFinish is the scripted shape the gate targets: change a file,
// then try to declare done without running the verify command.
func verifyLoop(t *testing.T, script []provider.Response) (*Loop, string) {
	t.Helper()
	root := t.TempDir()
	loop := &Loop{
		Prov: &fakeProv{script: script},
		Reg:  testRegistry(root),
		Cfg:  Config{MaxIters: 10, DoomRepeats: 5, Root: root, Mode: mode.Build, Pol: &policy.Policy{AlwaysAllow: true}},
	}
	return loop, root
}

func TestVerifyGateWarnsThenFinishes(t *testing.T) {
	t.Setenv("TILDE_VERIFY", "warn")
	t.Setenv("TILDE_VERIFY_CMD", "true")
	loop, root := verifyLoop(t, []provider.Response{
		{ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Args: map[string]any{"path": "x.txt", "content": "hi"}}}},
		{Content: "done"}, // would finish before verifying
		{ToolCalls: []provider.ToolCall{{ID: "2", Name: "shell_command", Args: map[string]any{"command": "true"}}}},
		{Content: "verified"},
	})
	reminded := false
	text, err := loop.Run(context.Background(), "goal", func(e Event) {
		if e.Kind == "system" && strings.Contains(e.Text, "Verification required") {
			reminded = true
		}
	})
	if err != nil {
		t.Fatalf("warn mode must finish, got %v", err)
	}
	if !reminded {
		t.Fatal("expected a verify reminder before finishing")
	}
	if !strings.Contains(text, "verified") {
		t.Fatalf("final text = %q", text)
	}
	if _, err := os.Stat(filepath.Join(root, "x.txt")); err != nil {
		t.Fatal("write_file should have landed")
	}
}

func TestVerifyGateOffDoesNotWarn(t *testing.T) {
	t.Setenv("TILDE_VERIFY", "off")
	t.Setenv("TILDE_VERIFY_CMD", "true")
	loop, _ := verifyLoop(t, []provider.Response{
		{ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Args: map[string]any{"path": "x.txt", "content": "hi"}}}},
		{Content: "done"},
	})
	reminded := false
	if _, err := loop.Run(context.Background(), "goal", func(e Event) {
		if e.Kind == "system" && strings.Contains(e.Text, "Verification required") {
			reminded = true
		}
	}); err != nil {
		t.Fatal(err)
	}
	if reminded {
		t.Fatal("off mode must not inject a verify reminder")
	}
}

func TestVerifyGateStrictRefuses(t *testing.T) {
	t.Setenv("TILDE_VERIFY", "strict")
	t.Setenv("TILDE_VERIFY_CMD", "true")
	loop, _ := verifyLoop(t, []provider.Response{
		{ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Args: map[string]any{"path": "x.txt", "content": "hi"}}}},
		{Content: "done"}, // never verifies; fakeProv repeats "done" after the script
	})
	_, err := loop.Run(context.Background(), "goal", func(Event) {})
	if err == nil {
		t.Fatal("strict mode must refuse to finish unverified")
	}
	if !strings.Contains(err.Error(), "Verification still outstanding") {
		t.Fatalf("error should name the pending verification, got %v", err)
	}
}

// A read-only run has nothing to verify: no reminder even with the gate on.
func TestVerifyGateIgnoresReadOnlyRun(t *testing.T) {
	t.Setenv("TILDE_VERIFY", "warn")
	t.Setenv("TILDE_VERIFY_CMD", "true")
	loop, _ := verifyLoop(t, []provider.Response{{Content: "just an answer"}})
	reminded := false
	if _, err := loop.Run(context.Background(), "goal", func(e Event) {
		if e.Kind == "system" && strings.Contains(e.Text, "Verification required") {
			reminded = true
		}
	}); err != nil {
		t.Fatal(err)
	}
	if reminded {
		t.Fatal("a run that changed nothing must not require verification")
	}
}
