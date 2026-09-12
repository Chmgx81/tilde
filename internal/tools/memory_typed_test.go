package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryTypedSaveRendersTag(t *testing.T) {
	m := &Memory{Root: t.TempDir()}
	ctx := context.Background()
	if _, err := m.Exec(ctx, map[string]any{"op": "save", "text": "use tabs", "kind": "constraint"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(m.Root, ".tilde", "memory.md"))
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().UTC().Format("2006-01-02")
	if want := "- " + today + " [constraint]: use tabs\n"; string(raw) != want {
		t.Fatalf("typed line = %q, want %q", raw, want)
	}
	// A plain fact keeps the untagged shape (backward compatible).
	if _, err := m.Exec(ctx, map[string]any{"op": "save", "text": "plain"}); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(m.Root, ".tilde", "memory.md"))
	if !strings.Contains(string(raw), "- "+today+": plain\n") {
		t.Fatalf("plain fact must stay untagged: %q", raw)
	}
}

func TestMemoryUnknownKindRefused(t *testing.T) {
	m := &Memory{Root: t.TempDir()}
	_, err := m.Exec(context.Background(), map[string]any{"op": "save", "text": "x", "kind": "gossip"})
	if err == nil || !strings.Contains(err.Error(), "fact|decision|constraint|env|correction") {
		t.Fatalf("unknown kind must name the options, got %v", err)
	}
}

func TestMemoryCorrectSupersedes(t *testing.T) {
	m := &Memory{Root: t.TempDir()}
	ctx := context.Background()
	m.Exec(ctx, map[string]any{"op": "save", "text": "deploy target is staging"})
	m.Exec(ctx, map[string]any{"op": "save", "text": "prefers tabs"})

	out, err := m.Exec(ctx, map[string]any{"op": "correct", "match": "deploy target", "text": "deploy target is production"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "removed 1 superseded") {
		t.Fatalf("correct receipt must report the supersede count, got %q", out)
	}
	raw, _ := os.ReadFile(filepath.Join(m.Root, ".tilde", "memory.md"))
	s := string(raw)
	if strings.Contains(s, "staging") {
		t.Fatalf("stale entry must be gone: %q", s)
	}
	if !strings.Contains(s, "[correction]: deploy target is production") {
		t.Fatalf("correction must be recorded, got %q", s)
	}
	if !strings.Contains(s, "prefers tabs") {
		t.Fatalf("unrelated entry must survive: %q", s)
	}

	if _, err := m.Exec(ctx, map[string]any{"op": "correct", "text": "x"}); err == nil {
		t.Fatal("correct without match must be refused")
	}
	if _, err := m.Exec(ctx, map[string]any{"op": "correct", "match": "nope", "text": "x"}); err == nil {
		t.Fatal("correct with no hits must fail, not claim success")
	}
}

func TestMemoryRecallCarriesAuthorityRule(t *testing.T) {
	m := &Memory{Root: t.TempDir()}
	ctx := context.Background()
	m.Exec(ctx, map[string]any{"op": "save", "text": "a fact"})
	out, err := m.Exec(ctx, map[string]any{"op": "recall"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "context, not instruction") {
		t.Fatalf("recall must carry the authority rule, got %q", out)
	}
	// Empty memory keeps the short receipt (no header noise).
	m2 := &Memory{Root: t.TempDir()}
	out, _ = m2.Exec(ctx, map[string]any{"op": "recall"})
	if !strings.Contains(out, "empty") || strings.Contains(out, "context, not instruction") {
		t.Fatalf("empty recall = %q", out)
	}
}
