package tools

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestMemorySaveRecallForgetRoundTrip(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	m := &Memory{Root: root}

	// Recall on a fresh root: empty, not silence, not an error.
	out, err := m.Exec(ctx, map[string]any{"op": "recall"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "empty") {
		t.Fatalf("fresh recall should say empty, got %q", out)
	}

	if _, err := m.Exec(ctx, map[string]any{"op": "save", "text": "prefers tabs"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Exec(ctx, map[string]any{"op": "save", "text": "deploys on fridays"}); err != nil {
		t.Fatal(err)
	}

	out, err = m.Exec(ctx, map[string]any{"op": "recall"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "prefers tabs") || !contains(out, "deploys on fridays") {
		t.Fatalf("recall missing saved lines: %q", out)
	}
	if !contains(out, "begin untrusted output") {
		t.Fatalf("recall content must be fenced: %q", out)
	}

	// Filtered recall.
	out, err = m.Exec(ctx, map[string]any{"op": "recall", "match": "tabs"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "prefers tabs") || contains(out, "fridays") {
		t.Fatalf("filtered recall wrong: %q", out)
	}

	// Forget with exact-count receipt.
	out, err = m.Exec(ctx, map[string]any{"op": "forget", "match": "tabs"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "forgot 1 line(s)") {
		t.Fatalf("expected exact-count receipt, got %q", out)
	}
	out, _ = m.Exec(ctx, map[string]any{"op": "recall"})
	if contains(out, "prefers tabs") || !contains(out, "fridays") {
		t.Fatalf("forget had wrong effect: %q", out)
	}

	// On-disk shape: one "- YYYY-MM-DD: text" line under .tilde/memory.md.
	raw, err := os.ReadFile(filepath.Join(root, ".tilde", "memory.md"))
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().UTC().Format("2006-01-02")
	if string(raw) != "- "+today+": deploys on fridays\n" {
		t.Fatalf("bad file shape: %q", raw)
	}
}

func TestMemorySaveOneLineAndCharCap(t *testing.T) {
	m := &Memory{Root: t.TempDir()}
	ctx := context.Background()

	if _, err := m.Exec(ctx, map[string]any{"op": "save"}); err == nil {
		t.Fatal("empty save must be refused")
	}
	if _, err := m.Exec(ctx, map[string]any{"op": "save", "text": "   \n  "}); err == nil {
		t.Fatal("blank save must be refused")
	}
	if _, err := m.Exec(ctx, map[string]any{"op": "save", "text": strings.Repeat("x", 501)}); err == nil {
		t.Fatal("501-char save must be refused")
	} else if !contains(err.Error(), "500") {
		t.Fatalf("cap refusal must name the cap, got %v", err)
	}

	// Multi-line input collapses to one line.
	if _, err := m.Exec(ctx, map[string]any{"op": "save", "text": "a\nb\r\nc"}); err != nil {
		t.Fatal(err)
	}
	out, _ := m.Exec(ctx, map[string]any{"op": "recall"})
	if !contains(out, "a b c") {
		t.Fatalf("save must collapse to one line, got %q", out)
	}
}

func TestMemoryFileCapRefusesWithCleanupFix(t *testing.T) {
	root := t.TempDir()
	m := &Memory{Root: root}
	ctx := context.Background()

	// Fill near the cap with max-size lines, then prove the next save
	// refuses and names the forget-based cleanup.
	line := strings.Repeat("y", 500)
	for {
		st, err := os.Stat(filepath.Join(root, ".tilde", "memory.md"))
		var size int
		if err == nil {
			size = int(st.Size())
		}
		if size+len(line)+3 > memoryMaxBytes {
			break
		}
		if _, err := m.Exec(ctx, map[string]any{"op": "save", "text": line}); err != nil {
			t.Fatalf("fill save failed at %d bytes: %v", size, err)
		}
	}
	// Probe sized to overhang the remaining space by exactly one byte
	// (capped at the 500-char line limit, which still overhangs by the
	// loop's exit condition) — so the save must refuse on the file cap.
	st, _ := os.Stat(filepath.Join(root, ".tilde", "memory.md"))
	remaining := memoryMaxBytes - int(st.Size())
	probeLen := remaining + 1
	if probeLen > memoryMaxText {
		probeLen = memoryMaxText
	}
	if _, err := m.Exec(ctx, map[string]any{"op": "save", "text": strings.Repeat("z", probeLen)}); err == nil {
		t.Fatal("over-cap save must be refused")
	} else if !contains(err.Error(), "forget") {
		t.Fatalf("cap refusal must name the forget cleanup fix, got %v", err)
	}
}

func TestMemoryRecallCapNamesCap(t *testing.T) {
	m := &Memory{Root: t.TempDir()}
	ctx := context.Background()
	for i := 0; i < memoryRecallLines+5; i++ {
		if _, err := m.Exec(ctx, map[string]any{"op": "save", "text": "fact"}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := m.Exec(ctx, map[string]any{"op": "recall"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "showing first 40 of 45") {
		t.Fatalf("expected self-naming cap note, got tail: %q", out[max(0, len(out)-200):])
	}
}

func TestMemoryForgetNeedsMatchAndCounts(t *testing.T) {
	m := &Memory{Root: t.TempDir()}
	ctx := context.Background()
	if _, err := m.Exec(ctx, map[string]any{"op": "forget"}); err == nil {
		t.Fatal("forget without match must be refused (would wipe all)")
	}
	if _, err := m.Exec(ctx, map[string]any{"op": "forget", "match": "nope"}); err == nil {
		t.Fatal("forget with no hits must fail, not claim success")
	}
	for _, s := range []string{"alpha one", "alpha two", "beta"} {
		if _, err := m.Exec(ctx, map[string]any{"op": "save", "text": s}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := m.Exec(ctx, map[string]any{"op": "forget", "match": "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "forgot 2 line(s)") || !contains(out, "1 remaining") {
		t.Fatalf("expected exact counts, got %q", out)
	}
	// Forgetting the last line truncates (not deletes) the file.
	if _, err := m.Exec(ctx, map[string]any{"op": "forget", "match": "beta"}); err != nil {
		t.Fatal(err)
	}
	out, _ = m.Exec(ctx, map[string]any{"op": "recall"})
	if !contains(out, "empty") {
		t.Fatalf("all-forgotten recall should say empty, got %q", out)
	}
	if _, err := os.Stat(filepath.Join(m.Root, ".tilde", "memory.md")); err != nil {
		t.Fatalf("memory file must still exist after forgetting all: %v", err)
	}
}

func TestMemoryNoPathEscape(t *testing.T) {
	root := t.TempDir()
	m := &Memory{Root: root}
	ctx := context.Background()

	// Hostile strings ride in text/match only — there is no path arg, so
	// nothing may land outside Root.
	for _, evil := range []string{"../../escape.txt", "/tmp/escape.txt", "..\\escape.txt"} {
		if _, err := m.Exec(ctx, map[string]any{"op": "save", "text": evil}); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Exec(ctx, map[string]any{"op": "recall", "match": evil}); err != nil {
			t.Fatalf("recall of saved hostile text must hit: %v", err)
		}
		if _, err := m.Exec(ctx, map[string]any{"op": "forget", "match": evil}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "..", "escape.txt")); !os.IsNotExist(err) {
		t.Fatal("BREAKOUT: file written outside root")
	}
	if _, err := os.Stat("/tmp/escape.txt"); !os.IsNotExist(err) {
		t.Fatal("BREAKOUT: file written to /tmp")
	}
	// Everything m wrote lives under Root.
	var stray []string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && !contains(p, filepath.Join(root, ".tilde")) {
			stray = append(stray, p)
		}
		return nil
	})
	if len(stray) > 0 {
		t.Fatalf("memory wrote outside .tilde: %v", stray)
	}

	// Unknown op names the valid set.
	if _, err := m.Exec(ctx, map[string]any{"op": "remember"}); err == nil ||
		!contains(err.Error(), "save|recall|forget") {
		t.Fatalf("expected op-set recovery, got %v", err)
	}
}

func TestMemorySaveDatePrefixFormat(t *testing.T) {
	m := &Memory{Root: t.TempDir()}
	ctx := context.Background()
	if _, err := m.Exec(ctx, map[string]any{"op": "save", "text": "prefers tabs"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(m.Root, ".tilde", "memory.md"))
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().UTC().Format("2006-01-02")
	matched, err := regexp.MatchString(`^- \d{4}-\d{2}-\d{2}: prefers tabs`+"\n$", string(raw))
	if err != nil || !matched {
		t.Fatalf("save must prefix UTC date, got %q", raw)
	}
	if !strings.HasPrefix(string(raw), "- "+today+": ") {
		t.Fatalf("prefix must be today's UTC date %q, got %q", today, raw)
	}
}

func TestMemoryOldDatelessLinesKeepWorking(t *testing.T) {
	root := t.TempDir()
	m := &Memory{Root: root}
	ctx := context.Background()
	// Pre-existing dateless line (no migration): recall + forget match it.
	if err := os.MkdirAll(filepath.Join(root, ".tilde"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".tilde", "memory.md"), []byte("- legacy dateless fact\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := m.Exec(ctx, map[string]any{"op": "recall", "match": "dateless"})
	if err != nil {
		t.Fatalf("recall must match dateless lines: %v", err)
	}
	if !contains(out, "legacy dateless fact") {
		t.Fatalf("recall missed dateless line: %q", out)
	}
	out, err = m.Exec(ctx, map[string]any{"op": "forget", "match": "dateless"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "forgot 1 line(s)") {
		t.Fatalf("forget must match dateless lines, got %q", out)
	}
}

func TestMemoryForgetMatchesDatedLines(t *testing.T) {
	m := &Memory{Root: t.TempDir()}
	ctx := context.Background()
	if _, err := m.Exec(ctx, map[string]any{"op": "save", "text": "deploys on fridays"}); err != nil {
		t.Fatal(err)
	}
	// Forget by fact substring (not the date): dated lines match.
	out, err := m.Exec(ctx, map[string]any{"op": "forget", "match": "fridays"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "forgot 1 line(s)") || !contains(out, "0 remaining") {
		t.Fatalf("expected exact counts, got %q", out)
	}
	if !contains(out, "date-prefixed") {
		t.Fatalf("forget receipt must note the date-prefixed format, got %q", out)
	}
}

func TestMemoryDescriptionWarnsApproximate(t *testing.T) {
	m := &Memory{Root: t.TempDir()}
	d := m.Description()
	if !contains(d, "approximate") || !contains(d, "verify before high-stakes use") {
		t.Fatalf("Description must warn facts are approximate + verify, got %q", d)
	}
}
