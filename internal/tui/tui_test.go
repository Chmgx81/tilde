package tui

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"tilde/internal/agent"
	"tilde/internal/mode"
	"tilde/internal/sandbox"
	"tilde/internal/skills"
	"tilde/internal/tools"
)

func newTestLoop() *agent.Loop { return &agent.Loop{} }

func newUndoLoop(t *testing.T, root string) (*agent.Loop, *tools.SeenMap, *tools.UndoManager, *tools.Registry) {
	t.Helper()
	seen := tools.NewSeenMap(root)
	undo := &tools.UndoManager{Root: root}
	reg := tools.NewRegistry()
	reg.Register(&tools.ReadFile{Root: root, Seen: seen})
	reg.Register(&tools.WriteFile{Root: root, Seen: seen})
	reg.Register(&tools.EditFile{Root: root, Seen: seen})
	reg.Register(&tools.GitDiff{Root: root})
	reg.Undo = undo
	return &agent.Loop{Reg: reg}, seen, undo, reg
}

func TestParseSlash(t *testing.T) {
	cmd, args := parseSlash("/mode build")
	if cmd != "/mode" || args != "build" {
		t.Fatalf("got %q %q", cmd, args)
	}
	if cmd, args := parseSlash("/clear"); cmd != "/clear" || args != "" {
		t.Fatalf("got %q %q", cmd, args)
	}
	if cmd, _ := parseSlash("not a command"); cmd != "" {
		t.Fatalf("plain text must not parse: %q", cmd)
	}
	if cmd, args := parseSlash("/compact token budget"); cmd != "/compact" || args != "token budget" {
		t.Fatalf("got %q %q", cmd, args)
	}
}

func TestFilterSlashRanks(t *testing.T) {
	rows := filterSlash("/mod")
	if len(rows) < 2 {
		t.Fatalf("expected /model + /mode, got %v", rows)
	}
	// Equal rank: the shorter command name wins the tie (spec: /mode
	// must not hide behind /model when the user typed /mod).
	if rows[0].cmd != "/mode <plan|build|auto>" || rows[1].cmd != "/model <model>" {
		t.Fatalf("shortest-name tie-break violated: %v", rows)
	}
	if len(filterSlash("/zzz")) != 0 {
		t.Fatal("expected no matches")
	}
}

func TestFuzzyPrefersBasename(t *testing.T) {
	files := []string{"docs/config.md", "internal/config/config.go", "internal/config/loader_test.go"}
	rows := matchFiles(files, "conf")
	if len(rows) != 3 {
		t.Fatalf("expected 3 matches, got %v", rows)
	}
	// Basename-start match first; capped at 9 tested below implicitly.
	if !strings.Contains(rows[0].path, "config") {
		t.Fatalf("unexpected top hit: %v", rows[0].path)
	}
}

func TestFuzzyNoMatch(t *testing.T) {
	if rows := matchFiles([]string{"a.go", "b.go"}, "zzz"); len(rows) != 0 {
		t.Fatalf("got %v", rows)
	}
}

func TestFuzzyCapNine(t *testing.T) {
	var files []string
	for i := 0; i < 30; i++ {
		files = append(files, filepath.Join("dir", strings.Repeat("a", 3)+string(rune('a'+i%26))+string(rune('a'+i/26))+".go"))
	}
	rows := matchFiles(files, "a")
	if len(rows) > 9 {
		t.Fatalf("expected cap of 9, got %d", len(rows))
	}
}

func TestActiveAtQuery(t *testing.T) {
	if q, ok := activeAtQuery("Look at @conf"); !ok || q != "conf" {
		t.Fatalf("got %q %v", q, ok)
	}
	if _, ok := activeAtQuery("email@host"); ok {
		t.Fatal("mid-word @ must not trigger")
	}
	if _, ok := activeAtQuery("@done now"); ok {
		t.Fatal("query with space must close")
	}
	if _, ok := activeAtQuery("plain text"); ok {
		t.Fatal("no @, no picker")
	}
}

func TestFilterIgnoredRespectsGitignore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\n"), 0o644)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "b.log"), []byte("x"), 0o644)
	got := walkFiles(root)
	for _, f := range got {
		if f == "b.log" {
			t.Fatalf("gitignored file surfaced: %v", got)
		}
		if f == ".gitignore" {
			continue
		}
	}
	found := false
	for _, f := range got {
		if f == "a.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a.go missing: %v", got)
	}
}

func TestWalkSkipsGitDir(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git", "objects"), 0o755)
	os.WriteFile(filepath.Join(root, ".git", "objects", "x"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "ok.go"), []byte("x"), 0o644)
	for _, f := range walkFiles(root) {
		if strings.HasPrefix(f, ".git") {
			t.Fatalf(".git surfaced: %v", f)
		}
	}
}

func TestLoadSessionRestoresCompactedState(t *testing.T) {
	log := `{"ts":"2026-09-05T00:00:00Z","type":"user","data":{"content":"goal: build it"}}
{"ts":"2026-09-05T00:00:01Z","type":"assistant","data":{"content":"ancient plan"}}
{"ts":"2026-09-05T00:00:02Z","type":"compacted","data":{"dropped":2,"summary":"SUMMARY-HERE"}}
{"ts":"2026-09-05T00:00:03Z","type":"user","data":{"content":"recent question"}}
{"ts":"2026-09-05T00:00:04Z","type":"assistant","data":{"content":"recent answer"}}
{"ts":"2026-09-05T00:00:05Z","type":"tool_call","data":{"name":"glob","args":{}}}
{"ts":"2026-09-05T00:00:06Z","type":"tool_result","data":{"name":"glob","output":"a.go"}}
corrupt line that must not kill the resume
`
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(log), 0o644)
	lines, msgs, err := loadSessionFile(p, 78)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) == 0 {
		t.Fatal("no scrollback rebuilt")
	}
	// Context: summary + post-compaction only — never the raw bulk.
	if len(msgs) == 0 || msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "SUMMARY-HERE") {
		t.Fatalf("missing compacted summary head: %+v", msgs)
	}
	joined := ""
	for _, m := range msgs {
		joined += m.Content + "\n"
	}
	if strings.Contains(joined, "ancient plan") || strings.Contains(joined, "goal: build it") {
		t.Fatalf("pre-compaction bulk leaked into context: %q", joined)
	}
	if !strings.Contains(joined, "recent answer") || !strings.Contains(joined, "Tool glob result") {
		t.Fatalf("post-compaction turns lost: %q", joined)
	}
}

func TestLoadSessionWithoutCompactionKeepsAll(t *testing.T) {
	log := `{"ts":"2026-09-05T00:00:00Z","type":"user","data":{"content":"hi"}}
{"ts":"2026-09-05T00:00:01Z","type":"assistant","data":{"content":"hello"}}
`
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(log), 0o644)
	_, msgs, err := loadSessionFile(p, 78)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %+v", msgs)
	}
}

func TestRelTime(t *testing.T) {
	now := time.Now()
	if relTime(now) != "just now" {
		t.Fatal("now")
	}
	if relTime(now.Add(-90*time.Minute)) != "1h ago" {
		t.Fatal("hours")
	}
	if relTime(now.Add(-72*time.Hour)) != "3d ago" {
		t.Fatal("days")
	}
}

func TestSplashRendersOnNew(t *testing.T) {
	m := New(nil, mode.Plan, "/tmp", "ollama/test-model", 32000)
	joined := strings.Join(m.lines, "\n")
	// Model lives on the status bar, not the splash (spec §2.1 trim) —
	// assert it there instead of in the transcript.
	for _, want := range []string{"Welcome to tilde", "Sandbox:", "32.0k", "bubblewrap"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("splash missing %q:\n%s", want, joined)
		}
	}
	if got := stripANSI(m.statusBar()); !strings.Contains(got, "ollama/test-model") {
		t.Fatalf("status bar must carry the model: %q", got)
	}
}

func TestSelectResumeRestoresCautiousPlan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := sessionsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := `{"ts":"2026-09-05T00:00:00Z","type":"meta","data":{"root":"/tmp/x"}}
{"ts":"2026-09-05T00:00:01Z","type":"user","data":{"content":"do the thing"}}
{"ts":"2026-09-05T00:00:02Z","type":"assistant","data":{"content":"did it"}}
`
	if err := os.WriteFile(filepath.Join(dir, "session_abc.jsonl"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := ListSessions()
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%v err=%v", items, err)
	}
	if items[0].First != "do the thing" {
		t.Fatalf("first message not picked up: %+v", items[0])
	}
	loop := newTestLoop()
	m := New(loop, mode.Build, "/tmp", "ollama/m", 32000)
	m.resumeItems = items
	m.selectResume()
	if m.curMode != mode.Plan {
		t.Fatal("resume must open in Plan")
	}
	if len(loop.Msgs) != 2 {
		t.Fatalf("context not restored: %+v", loop.Msgs)
	}
	if len(m.lines) == 0 {
		t.Fatal("scrollback not rebuilt")
	}
	if loop.Log == nil {
		t.Fatal("log not reattached")
	}
	_ = loop.Log.Close()
}

func TestSlashUnknownNamesPalette(t *testing.T) {
	loop := newTestLoop()
	m := New(loop, mode.Plan, "/tmp", "ollama/m", 32000)
	m.runSlash("/frobnicate", "", slashRow{})
	if len(m.lines) == 0 || !strings.Contains(m.lines[len(m.lines)-1], "unknown command") {
		t.Fatalf("lines=%v", m.lines)
	}
}

func TestSandboxCommandPrintsTier(t *testing.T) {
	loop := newTestLoop()
	m := New(loop, mode.Plan, "/tmp", "ollama/m", 32000)
	m.runSlash("/sandbox", "", slashRow{})
	joined := strings.Join(m.lines, "\n")
	if !strings.Contains(joined, "sandbox:") || !strings.Contains(joined, "ask:") {
		t.Fatalf("sandbox status missing:\n%s", joined)
	}
}

func TestUndoCommandRevertsBadEdit(t *testing.T) {
	root := t.TempDir()
	loop, seen, undo, reg := newUndoLoop(t, root)
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("good\n"), 0o644)
	reg.Dispatch(context.Background(), "read_file", map[string]any{"path": "f.txt"})
	reg.Dispatch(context.Background(), "edit_file", map[string]any{"path": "f.txt", "old_string": "good", "new_string": "bad"})
	if undo.Depth() != 1 {
		t.Fatal("edit should snapshot")
	}
	m := New(loop, mode.Plan, root, "ollama/m", 32000)
	m.runSlash("/undo", "", slashRow{})
	if data, _ := os.ReadFile(filepath.Join(root, "f.txt")); string(data) != "good\n" {
		t.Fatalf("f.txt=%q after /undo", data)
	}
	joined := strings.Join(m.lines, "\n")
	if !strings.Contains(joined, "undone 1 step") || !strings.Contains(joined, "restored f.txt") {
		t.Fatalf("report missing:\n%s", joined)
	}
	_ = seen
}

func TestUndoCommandEmpty(t *testing.T) {
	loop, _, _, _ := newUndoLoop(t, t.TempDir())
	m := New(loop, mode.Plan, "/tmp", "ollama/m", 32000)
	m.runSlash("/undo", "", slashRow{})
	if last := m.lines[len(m.lines)-1]; !strings.Contains(last, "nothing to undo") {
		t.Fatalf("got %q", last)
	}
}

func TestUndoCommandBadCount(t *testing.T) {
	loop, _, _, _ := newUndoLoop(t, t.TempDir())
	m := New(loop, mode.Plan, "/tmp", "ollama/m", 32000)
	m.runSlash("/undo", "zzz", slashRow{})
	if last := m.lines[len(m.lines)-1]; !strings.Contains(last, "n >= 1") {
		t.Fatalf("got %q", last)
	}
}

func TestDiffCommandShowsWork(t *testing.T) {
	root := t.TempDir()
	loop, _, _, _ := newUndoLoop(t, root)
	ctx := context.Background()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-qb", "main")
	git("config", "user.email", "t@t.t")
	git("config", "user.name", "t")
	os.WriteFile(filepath.Join(root, "d.txt"), []byte("v1\n"), 0o644)
	git("add", ".")
	git("commit", "-qm", "init")
	os.WriteFile(filepath.Join(root, "d.txt"), []byte("v2\n"), 0o644)
	m := New(loop, mode.Plan, root, "ollama/m", 32000)
	m.runSlash("/diff", "", slashRow{})
	joined := strings.Join(m.lines, "\n")
	if !strings.Contains(joined, "● diff") || !strings.Contains(joined, "+v2") {
		t.Fatalf("diff missing:\n%s", joined)
	}
	_ = ctx
}

func TestDiffCommandNonRepo(t *testing.T) {
	loop, _, _, _ := newUndoLoop(t, t.TempDir())
	m := New(loop, mode.Plan, "/tmp", "ollama/m", 32000)
	m.runSlash("/diff", "", slashRow{})
	if last := m.lines[len(m.lines)-1]; !strings.Contains(last, "✗") {
		t.Fatalf("expected loud failure, got %q", last)
	}
}

func TestSkillsPickerListsAndLoads(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".tilde", "skills"), 0o755)
	os.WriteFile(filepath.Join(root, ".tilde", "skills", "shouty.md"),
		[]byte("---\nname: shouty\ndescription: Talk loud for picker tests\n---\nSHOUT BODY\n"), 0o644)
	loop := newTestLoop()
	m := New(loop, mode.Plan, root, "ollama/m", 32000)
	m.runSlash("/skills", "", slashRow{})
	if !m.skillsOpen || len(m.skillsItems) != 1 {
		t.Fatalf("picker did not open with the skill: %+v", m.skillsItems)
	}
	m.loadSkill(m.skillsItems[0])
	if m.skillsOpen {
		t.Fatal("picker must close on load")
	}
	if len(loop.Msgs) != 1 || !strings.Contains(loop.Msgs[0].Content, "SHOUT BODY") {
		t.Fatalf("body not in context: %+v", loop.Msgs)
	}
	joined := strings.Join(m.lines, "\n")
	if !strings.Contains(joined, "Loaded skill: shouty (project)") {
		t.Fatalf("audit line missing:\n%s", joined)
	}
}

func TestSkillsPickerEmptyGuides(t *testing.T) {
	loop := newTestLoop()
	m := New(loop, mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.runSlash("/skills", "", slashRow{})
	if m.skillsOpen {
		t.Fatal("empty picker must not open")
	}
	if last := m.lines[len(m.lines)-1]; !strings.Contains(last, ".tilde/skills") {
		t.Fatalf("expected install guidance, got %q", last)
	}
}

func TestRefreshPreservesCursorAcrossBlinks(t *testing.T) {
	// Regression: background messages (cursor blink) re-ran the filter and
	// snapped the cursor to 0, making navigation impossible.
	loop := newTestLoop()
	m := New(loop, mode.Plan, "/tmp", "ollama/m", 32000)
	m.ta.SetValue("/s")
	m.refreshPickers()
	if !m.slashOpen || len(m.slashItems) == 0 {
		t.Fatalf("palette did not open: %v", m.slashItems)
	}
	m.slashCursor = 2
	m.refreshPickers() // simulated blink tick, same text
	m.refreshPickers()
	if m.slashCursor != 2 {
		t.Fatalf("cursor snapped back to %d", m.slashCursor)
	}
	m.ta.SetValue("/se")
	m.refreshPickers()
	if m.slashCursor != 0 {
		t.Fatalf("cursor must reset on new query, got %d", m.slashCursor)
	}
}

func TestEscDismissalSurvivesRefresh(t *testing.T) {
	// Regression: Esc closed the picker, but the next blink refresh with
	// unchanged text re-opened it.
	loop := newTestLoop()
	m := New(loop, mode.Plan, "/tmp", "ollama/m", 32000)
	m.ta.SetValue("/s")
	m.refreshPickers()
	if !m.slashOpen {
		t.Fatal("palette should be open")
	}
	m.dismissed = m.ta.Value()
	m.slashOpen = false
	m.refreshPickers() // blink tick, same text
	if m.slashOpen {
		t.Fatal("dismissed picker re-opened on background refresh")
	}
	m.ta.SetValue("/se")
	m.refreshPickers()
	if !m.slashOpen {
		t.Fatal("new typing must re-arm the picker")
	}
}

func TestListSessionsSkipsForeignFormats(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := sessionsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := "{\"time\":\"2026-09-05T02:09:00Z\",\"role\":\"user\",\"content\":\"hi\"}\n"
	os.WriteFile(filepath.Join(dir, "2026-09-05-0209.jsonl"), []byte(foreign), 0o644)
	mine := "{\"ts\":\"2026-09-05T00:00:00Z\",\"type\":\"user\",\"data\":{\"content\":\"hey\"}}\n"
	os.WriteFile(filepath.Join(dir, "session_abc.jsonl"), []byte(mine), 0o644)
	items, err := ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "session_abc" {
		t.Fatalf("foreign log must be skipped: %+v", items)
	}
}

func TestSplashHeaderAndMode(t *testing.T) {
	lines := splashLines("/tmp/root", "ollama/m", 32000, 78)
	// Centered title: the one centered line in the UI (a title, not a
	// log event). Padding is (width - text) / 2.
	wantText := "/tmp/root · tilde " + appVersion
	if got := strings.TrimLeft(lines[0], " "); got != wantText {
		t.Fatalf("context header must lead the splash: %q", lines[0])
	}
	if lead := len(lines[0]) - len(wantText); lead != (78-len([]rune(wantText)))/2 {
		t.Fatalf("header must be centered, got %d leading spaces: %q", lead, lines[0])
	}
	// Spec §2.1 (trimmed): the splash keeps only what exists nowhere
	// else — Budget ceiling + Sandbox state on one line. Model and Mode
	// live on the ever-present status bar, so they must NOT repeat here.
	budgetAt := -1
	for i, l := range lines {
		plain := stripANSI(l)
		if strings.HasPrefix(l, "  Budget:") {
			budgetAt = i
			if !strings.Contains(plain, "Sandbox:") {
				t.Fatalf("Budget and Sandbox must share one line: %q", l)
			}
		}
		if strings.Contains(plain, "Mode:") || strings.Contains(plain, "Model:") {
			t.Fatalf("Model/Mode must not repeat in the splash (status bar carries both): %q", l)
		}
	}
	if budgetAt < 0 {
		t.Fatalf("missing Budget/Sandbox line:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSplashEchoesNoChrome(t *testing.T) {
	// The composer placeholder and the hint bar are ever-present chrome
	// below the viewport; the splash transcript must not echo their
	// wording, or the first frame shows both twice.
	lines := splashLines("/tmp/root", "ollama/m", 32000, 78)
	for _, l := range lines {
		plain := stripANSI(l)
		if strings.Contains(plain, "→ Plan, search, build anything") {
			t.Fatalf("splash must not echo the composer placeholder: %q", l)
		}
		if strings.HasPrefix(plain, "  / commands") {
			t.Fatalf("splash must not echo the hint bar: %q", l)
		}
	}
	// Splash ends on the Budget/Sandbox block (plus breathing room).
	last := -1
	for i, l := range lines {
		if strings.TrimSpace(stripANSI(l)) != "" {
			last = i
		}
	}
	if !strings.HasPrefix(lines[last], "  Budget:") {
		t.Fatalf("splash must end on the Budget line, ended on %q", lines[last])
	}
}

func TestSplashPanelGeometry(t *testing.T) {
	for _, w := range []int{40, 78, 120} {
		lines := splashLines("/tmp/root", "ollama/m", 32000, w)
		joined := strings.Join(lines, "\n")
		plain := stripANSI(joined)
		for _, want := range []string{"Welcome to tilde.", "bubblewrap", "policies.yaml"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("width %d: splash missing %q:\n%s", w, want, plain)
			}
		}
		if strings.Contains(plain, "○") {
			t.Fatalf("width %d: splash must not use the pending ○ glyph", w)
		}
		if strings.Contains(plain, "→ Plan, search, build anything") {
			t.Fatalf("width %d: splash must not echo the composer placeholder", w)
		}
		for _, l := range lines {
			if strings.HasPrefix(stripANSI(l), "  / commands") {
				t.Fatalf("width %d: splash must not echo the hint bar: %q", w, l)
			}
		}
		// Panel borders: first border line opens with ╭, last closes with ╰.
		top, bottom := -1, -1
		for i, l := range lines {
			p := stripANSI(l)
			if strings.Contains(p, "╭") && top < 0 {
				top = i
			}
			if strings.Contains(p, "╰") {
				bottom = i
			}
		}
		if top < 0 || bottom < 0 || bottom <= top {
			t.Fatalf("width %d: panel must open with ╭ and close with ╰:\n%s", w, plain)
		}
		for i := top; i <= bottom; i++ {
			if got := ansi.StringWidth(lines[i]); got > w {
				t.Fatalf("width %d: panel line %d too wide (%d): %q", w, i, got, stripANSI(lines[i]))
			}
		}
		// The Budget/Sandbox line is space-between (gap computed from the
		// live width), so it fits every width >= 78 exactly; at 40 only
		// the panel + header are asserted for width.
		if w >= 78 {
			for _, l := range lines {
				if got := ansi.StringWidth(l); got > w {
					t.Fatalf("width %d: splash line too wide (%d): %q", w, got, stripANSI(l))
				}
			}
		} else {
			for i, l := range lines {
				if i >= top && i <= bottom {
					continue
				}
				if strings.HasPrefix(stripANSI(l), "  Model:") || strings.HasPrefix(stripANSI(l), "  Budget:") {
					continue // legacy fixed-width lines, exempt below 78
				}
				if got := ansi.StringWidth(l); got > w {
					t.Fatalf("width %d: splash line %d too wide (%d): %q", w, i, got, stripANSI(l))
				}
			}
		}
	}
}

func TestSplashTinyWidthStaysContained(t *testing.T) {
	for _, width := range []int{20, 24, 31} {
		for _, line := range splashLines("/tmp/root", "ollama/m", 32000, width) {
			if got := ansi.StringWidth(line); got > width {
				t.Fatalf("width %d: tiny splash line too wide (%d): %q", width, got, stripANSI(line))
			}
		}
	}
}

func TestSplashResizeRebuildsOnlyWhenFresh(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, "/tmp/root", "ollama/m", 32000)
	if m.splashN <= 0 || len(m.lines) != m.splashN {
		t.Fatalf("fresh session must record splashN=%d lines=%d", m.splashN, len(m.lines))
	}
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	after := nm.(Model)
	if after.vp.Width != 116 {
		t.Fatalf("vp width must track the resize, got %d", after.vp.Width)
	}
	want := m.freshLines(after.vp.Width)
	if len(after.lines) != len(want) || after.splashN != len(after.lines) {
		t.Fatalf("fresh splash must rebuild on resize: lines=%d want=%d splashN=%d",
			len(after.lines), len(want), after.splashN)
	}
	for i := range want {
		if stripANSI(after.lines[i]) != stripANSI(want[i]) {
			t.Fatalf("rebuilt line %d diverged:\n got %q\nwant %q", i, stripANSI(after.lines[i]), stripANSI(want[i]))
		}
	}
	for _, l := range after.lines {
		if got := ansi.StringWidth(l); got > 120 {
			t.Fatalf("resized splash line too wide (%d): %q", got, stripANSI(l))
		}
	}
	if after.curMode != m.curMode || after.stick != m.stick {
		t.Fatal("resize must not touch non-splash state")
	}
	// Once the transcript moves, resizes must not rebuild the prefix.
	after.append("hello-resize-guard")
	guarded := len(after.lines)
	nm2, _ := after.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	after2 := nm2.(Model)
	if len(after2.lines) != guarded {
		t.Fatalf("touched transcript must not rebuild: before=%d after=%d", guarded, len(after2.lines))
	}
	found := false
	for _, l := range after2.lines {
		if strings.Contains(stripANSI(l), "hello-resize-guard") {
			found = true
		}
	}
	if !found {
		t.Fatal("appended content must survive a resize verbatim")
	}
}

func TestHighlightNonASCIIPath(t *testing.T) {
	// 'e' after 'é': rune index 5, byte index 6 — proves rune-based indices.
	_, idx := fuzzyScore("e", "données")
	if len(idx) != 1 || idx[0] != 5 {
		t.Fatalf("expected rune index [5], got %v", idx)
	}
	path := "café/héllo.go"
	_, hidx := fuzzyScore("héllo", path)
	if len(hidx) != 5 || hidx[0] != 5 {
		t.Fatalf("expected 5 matches from rune 5, got %v", hidx)
	}
	got := highlight(path, hidx)
	if stripped := stripANSI(got); stripped != path {
		t.Fatalf("highlight corrupted multibyte path: %q", stripped)
	}
	// Batching renders one style per contiguous run: 'h' bold, "éll" un-
	// matched, "o" bold — two bold runs for 5 matched runes. Asserted
	// only when lipgloss's active profile emits SGR at all (plain test
	// runs default to the Ascii profile, which strips styling).
	if strings.Contains(got, "\x1b[") {
		if n := strings.Count(got, "\x1b[1m"); n != 2 {
			t.Fatalf("bold runs=%d, want 2 (per-run batching)", n)
		}
	}
}

func TestSkillsBackspaceMultibyte(t *testing.T) {
	m := Model{skillsQuery: "日本語テ"}
	nm, _ := m.updateSkills(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := nm.(Model).skillsQuery; got != "日本語" {
		t.Fatalf("backspace split a rune: %q", got)
	}
	nm, _ = m.updateSkills(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := nm.(Model).skillsQuery; !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8 after backspace: %q", got)
	}
	empty := Model{}
	nm, _ = empty.updateSkills(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := nm.(Model).skillsQuery; got != "" {
		t.Fatalf("backspace on empty query: %q", got)
	}
}

func TestSkillsScopeColumnAligned(t *testing.T) {
	m := Model{skillsItems: []skills.Skill{
		{Name: "a", Description: "short", Scope: "user"},
		{Name: "a-much-longer-skill-name", Description: "a somewhat longer description here", Scope: "project"},
		{Name: "mid", Description: "x", Scope: "user"},
	}}
	view := m.skillsView()
	if strings.Contains(view, "type to filter") || !strings.Contains(view, "/ search") {
		t.Fatalf("footer must say / search:\n%s", view)
	}
	var widths []int
	for _, ln := range strings.Split(view, "\n") {
		s := stripANSI(ln)
		if !strings.Contains(s, ". ") {
			continue // header / footer, not a skill row
		}
		widths = append(widths, len([]rune(s)))
	}
	if len(widths) != 3 {
		t.Fatalf("expected 3 skill rows, got %d:\n%s", len(widths), view)
	}
	for _, w := range widths[1:] {
		if w != widths[0] {
			t.Fatalf("scope column not aligned, widths=%v:\n%s", widths, view)
		}
	}
}

func TestSkillsSlashResetsFilter(t *testing.T) {
	m := Model{skillsQuery: "code", skillsOpen: true}
	nm, _ := m.updateSkills(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	res := nm.(Model)
	if res.skillsQuery != "" {
		t.Fatalf("slash must reset filter, got %q", res.skillsQuery)
	}
	if !res.skillsOpen {
		t.Fatal("slash must keep the picker open")
	}
	// Ordinary typing still appends.
	m2 := Model{skillsQuery: "cod", skillsOpen: true}
	nm2, _ := m2.updateSkills(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if got := nm2.(Model).skillsQuery; got != "code" {
		t.Fatalf("typing must append, got %q", got)
	}
}

func TestGitBranchCleanDirtyAndMissing(t *testing.T) {
	root := t.TempDir()
	if got := gitBranch(root); got != "" {
		t.Fatalf("non-repo must be empty, got %q", got)
	}
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-qb", "main")
	git("config", "user.email", "t@t.t")
	git("config", "user.name", "t")
	if got := gitBranch(root); got != "main" {
		t.Fatalf("unborn HEAD: got %q", got)
	}
	os.WriteFile(root+"/f.txt", []byte("x"), 0o644)
	git("add", ".")
	git("commit", "-qm", "init")
	if got := gitBranch(root); got != "main" {
		t.Fatalf("clean: got %q", got)
	}
	os.WriteFile(root+"/f.txt", []byte("y"), 0o644)
	os.WriteFile(root+"/g.txt", []byte("z"), 0o644)
	if got := gitBranch(root); got != "main [+2]" {
		t.Fatalf("dirty: got %q", got)
	}
}

func TestHardCut(t *testing.T) {
	if got := hardCut("abcdef", 10); got != "abcdef" {
		t.Fatalf("short untouched: %q", got)
	}
	if got := hardCut("abcdef", 4); got != "abc…" {
		t.Fatalf("cut wrong: %q", got)
	}
	if got := hardCut("abcdef", 1); got != "" {
		t.Fatalf("width 1: %q", got)
	}
}

func TestUndoRefusesWhileBackgroundRunning(t *testing.T) {
	root := t.TempDir()
	loop, _, _, reg := newUndoLoop(t, root)
	mgr := &tools.TaskManager{LogDir: t.TempDir()}
	sh := &tools.Shell{Root: root, BgMax: time.Minute, Tasks: mgr}
	reg.Register(sh)
	out, err := sh.Exec(context.Background(), map[string]any{"command": "sleep 60", "background": true})
	if err != nil || !strings.Contains(out, "task_1") {
		t.Fatalf("bg start: %q %v", out, err)
	}
	// Point the shared Shell instance at the same manager.
	if s, ok := reg.Get("shell_command"); ok {
		if ss, ok := s.(*tools.Shell); ok {
			ss.Tasks = mgr
		}
	}
	m := New(loop, mode.Plan, root, "ollama/m", 32000)
	m.runSlash("/undo", "", slashRow{})
	last := m.lines[len(m.lines)-1]
	if !strings.Contains(last, "still running") {
		t.Fatalf("expected running-task refusal, got %q", last)
	}
	for _, id := range mgr.KillAll() {
		_ = id
	}
}

func TestHandoffRendersRedPanelAndAmberToast(t *testing.T) {
	loop := newTestLoop()
	m := New(loop, mode.Build, "/tmp", "ollama/m", 32000)
	cmd := m.renderEvent(agent.Event{Kind: "handoff", Text: "Same call (x) issued 3 times — done."})
	_ = cmd
	joined := strings.Join(m.lines, "\n")
	if !strings.Contains(joined, "Partial diff and full history are preserved below.") {
		t.Fatalf("spec wording missing:\n%s", joined)
	}
	if !m.toastAmber || m.curMode != mode.Plan {
		t.Fatalf("amber=%v mode=%v", m.toastAmber, m.curMode)
	}
}

func TestScrollFollowTail(t *testing.T) {
	// New models pin to the tail; appends keep it.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	if !m.stick {
		t.Fatal("fresh session must follow the tail")
	}
	for i := 0; i < 60; i++ {
		m.append(fmt.Sprintf("line %d", i))
	}
	if !m.vp.AtBottom() {
		t.Fatal("append with stick=true must stay at bottom")
	}
	// Scrolling up unpins; a later append must not yank the view down.
	m.updateScroll(tea.KeyMsg{Type: tea.KeyUp})
	m.stick = false
	m.append("new event while reading history")
	if m.vp.AtBottom() {
		t.Fatal("append must not yank the user back to the tail")
	}
	// End re-pins.
	m.updateScroll(tea.KeyMsg{Type: tea.KeyEnd})
	if !m.stick || !m.vp.AtBottom() {
		t.Fatal("End must re-pin to the tail")
	}
	m.append("another event")
	if !m.vp.AtBottom() {
		t.Fatal("append while pinned must follow the tail")
	}
}

func TestScrollKeys(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	for i := 0; i < 80; i++ {
		m.append(fmt.Sprintf("line %d", i))
	}
	if _, cmd := m.updateScroll(tea.KeyMsg{Type: tea.KeyHome}); cmd != nil || m.stick || m.vp.YOffset != 0 {
		t.Fatalf("Home must pin to the top (off=%d stick=%v)", m.vp.YOffset, m.stick)
	}
	if _, _ = m.updateScroll(tea.KeyMsg{Type: tea.KeyEnd}); !m.stick {
		t.Fatal("End must re-pin")
	}
	// PgUp scrolls; multiline drafts release ↑↓ to the textarea but not PgUp.
	if _, _ = m.updateScroll(tea.KeyMsg{Type: tea.KeyPgUp}); m.vp.YOffset == 0 {
		t.Fatal("PgUp must scroll")
	}
	m.ta.InsertString("one\n")
	m.syncComposer()
	if m.ta.Height() != 2 {
		t.Fatalf("composer must grow with its content, height=%d", m.ta.Height())
	}
	if handled, cmd := m.updateScroll(tea.KeyMsg{Type: tea.KeyUp}); handled || cmd != nil {
		t.Fatal("↑ with a multiline draft must edit, not scroll")
	}
	if handled, _ := m.updateScroll(tea.KeyMsg{Type: tea.KeyShiftUp}); !handled {
		t.Fatal("Shift+↑ must always scroll")
	}
}

func TestWrapLineANSI(t *testing.T) {
	styled := lipgloss.NewStyle().Foreground(fgMuted).Render(strings.Repeat("ab", 60))
	wrapped := wrapLine(styled, 50)
	lines := strings.Split(wrapped, "\n")
	if len(lines) < 3 {
		t.Fatalf("styled line must wrap to width, got %d segments", len(lines))
	}
	for _, ln := range lines {
		if ansi.StringWidth(ln) > 50 {
			t.Fatalf("wrapped segment too wide: %d", ansi.StringWidth(ln))
		}
	}
	if got := strings.ReplaceAll(stripANSI(wrapped), "\n", ""); got != strings.Repeat("ab", 60) {
		t.Fatal("wrapping must preserve all visible content")
	}
	if wrapLine("short", 50) != "short" {
		t.Fatal("short lines must pass through unchanged")
	}
}

func TestRenderToolResultDiffBlock(t *testing.T) {
	diff := "diff --git a/x.go b/x.go\n@@ -1,2 +1,2 @@\n-old\n+new\n context"
	out := renderToolResult(diff)
	plain := stripANSI(out)
	for _, want := range []string{"x.go", "+new", "-old", "context"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("diff block lost %q:\n%s", want, plain)
		}
	}
	if !strings.Contains(out, "\n") {
		t.Fatal("multi-line results must render as a block, not ⎿ firstLine")
	}
	// One-line results keep the ⎿ shape.
	one := renderToolResult("ok  tilde/internal/tui 0.4s")
	if !strings.Contains(one, "⎿") || strings.Contains(one, "\n") {
		t.Fatalf("one-line result must stay a single ⎿ line: %q", one)
	}
	// Truncation is announced, never silent.
	var big strings.Builder
	for i := 0; i < maxToolResultLines+10; i++ {
		big.WriteString("noise\n")
	}
	if got := renderToolResult(big.String()); !strings.Contains(got, "truncated") {
		t.Fatal("oversized tool output must announce truncation")
	}
}

func TestSlashRankExactBeatsPrefix(t *testing.T) {
	rows := filterSlash("/mode")
	if rows[0].cmd != "/mode <plan|build|auto>" {
		t.Fatalf("exact /mode must rank first, got %q", rows[0].cmd)
	}
	rows = filterSlash("/m")
	if rows[0].cmd != "/mode <plan|build|auto>" {
		t.Fatalf("shorter command must win ties, got %q", rows[0].cmd)
	}
}

func TestSlashDropdownWidth(t *testing.T) {
	rows := filterSlash("/")
	got := stripANSI(slashDropdown(rows, 0, 40))
	for _, ln := range strings.Split(got, "\n") {
		if n := len([]rune(ln)); n > 40 {
			t.Fatalf("palette row overflows narrow terminal: %d cols: %q", n, ln)
		}
	}
}

func TestHintBarScrollIndicator(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	for i := 0; i < 50; i++ {
		m.append(fmt.Sprintf("line %d", i))
	}
	if got := stripANSI(m.hintBar("base")); got != "base" {
		t.Fatalf("at bottom the hint bar is the base hint, got %q", got)
	}
	m.stick = false
	m.vp.ScrollUp(5)
	got := stripANSI(m.hintBar("base"))
	if !strings.Contains(got, "% of history") {
		t.Fatalf("scrolled-up state must show a scroll indicator, got %q", got)
	}
}

func TestPasteTruncationNotice(t *testing.T) {
	// A giant single-line paste now collapses to a token (spec §2.21)
	// instead of inline-truncating: the box holds only the placeholder.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	big := strings.Repeat("x", maxInputChars+500)
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(big), Paste: true})
	after := nm.(Model)
	if got := after.ta.Value(); !strings.HasPrefix(got, "[Pasted text #1 +1 lines]") {
		t.Fatalf("giant paste must collapse, box=%q", got)
	}
	if int(after.ta.Length()) > maxInputChars {
		t.Fatalf("token must fit the cap, got %d", after.ta.Length())
	}
	// The inline cap still binds box-nearly-full overflows: a small
	// paste (below collapse thresholds) exceeding the remaining room
	// truncates with notice.
	m2 := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m2.ta.SetValue(strings.Repeat("y", maxInputChars-50))
	nm2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("z", 200)), Paste: true})
	after2 := nm2.(Model)
	if int(after2.ta.Length()) > maxInputChars {
		t.Fatalf("paste must be capped at %d, got %d", maxInputChars, after2.ta.Length())
	}
	found := false
	for _, ln := range after2.lines {
		if strings.Contains(stripANSI(ln), "large paste truncated") {
			found = true
		}
	}
	if !found {
		t.Fatal("oversized paste must announce truncation in the transcript")
	}
}

func TestMouseWheelScrolls(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	for i := 0; i < 50; i++ {
		m.append(fmt.Sprintf("line %d", i))
	}
	bottom := m.vp.YOffset
	nm, _ := m.Update(tea.MouseMsg{X: 0, Y: 0, Button: tea.MouseButtonWheelUp})
	after := nm.(Model)
	if after.stick || after.vp.YOffset >= bottom {
		t.Fatal("wheel-up must scroll into history and unpin")
	}
	for i := 0; i < 60; i++ { // keep wheeling: must clamp at the very top
		nm, _ = after.Update(tea.MouseMsg{X: 0, Y: 0, Button: tea.MouseButtonWheelUp})
		after = nm.(Model)
	}
	if after.vp.YOffset != 0 {
		t.Fatalf("wheel-up must reach the top, got offset %d", after.vp.YOffset)
	}
	nm, _ = after.Update(tea.MouseMsg{X: 0, Y: 0, Button: tea.MouseButtonWheelDown})
	after = nm.(Model)
	for after.vp.YOffset < bottom { // wheel back down to the live tail
		nm, _ = after.Update(tea.MouseMsg{X: 0, Y: 0, Button: tea.MouseButtonWheelDown})
		after = nm.(Model)
	}
	if !after.vp.AtBottom() || !after.stick {
		t.Fatal("wheel-down to the end must re-pin to the tail")
	}
}

func TestResizePreservesHistoryPosition(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	for i := 0; i < 80; i++ {
		m.append(fmt.Sprintf("history line %d", i))
	}
	m.stick = false
	m.vp.ScrollUp(8)
	before := m.vp.YOffset
	if m.vp.AtBottom() {
		t.Fatal("fixture must be scrolled away from the tail")
	}

	nm, _ := m.Update(tea.WindowSizeMsg{Width: 70, Height: 18})
	after := nm.(Model)
	if after.stick {
		t.Fatal("resize must not re-pin a user reading history")
	}
	if after.vp.AtBottom() {
		t.Fatalf("resize moved history reader to the tail: before=%d after=%d", before, after.vp.YOffset)
	}

	oldOffset := after.vp.YOffset
	after.append("new live output")
	if after.vp.YOffset != oldOffset {
		t.Fatalf("live output must not yank a scrolled reader: before=%d after=%d", oldOffset, after.vp.YOffset)
	}
}

func TestWheelMatchesShiftArrowPinDiscipline(t *testing.T) {
	// One wheel tick moves three rows — the same distance as three
	// Shift+↑/↓ presses — and both obey the same pin discipline:
	// up unpins the follow-tail, down re-pins at the very bottom.
	wheel := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	shift := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	for i := 0; i < 30; i++ {
		wheel.append(fmt.Sprintf("line %d", i))
		shift.append(fmt.Sprintf("line %d", i))
	}
	bottom := wheel.vp.YOffset

	nm, _ := wheel.Update(tea.MouseMsg{X: 0, Y: 0, Button: tea.MouseButtonWheelUp})
	wUp := nm.(Model)
	for i := 0; i < 3; i++ {
		nm2, _ := shift.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
		shift = nm2.(Model)
	}
	if wUp.vp.YOffset != shift.vp.YOffset {
		t.Fatalf("wheel-up and 3×Shift+Up diverge: %d/%d",
			wUp.vp.YOffset, shift.vp.YOffset)
	}
	if wUp.stick || shift.stick {
		t.Fatal("scrolling up must unpin the follow-tail")
	}
	nm, _ = wUp.Update(tea.MouseMsg{X: 0, Y: 0, Button: tea.MouseButtonWheelDown})
	wDown := nm.(Model)
	for i := 0; i < 3; i++ {
		nm2, _ := shift.Update(tea.KeyMsg{Type: tea.KeyShiftDown})
		shift = nm2.(Model)
	}
	if wDown.vp.YOffset != shift.vp.YOffset {
		t.Fatalf("wheel-down and 3×Shift+Down diverge: %d/%d",
			wDown.vp.YOffset, shift.vp.YOffset)
	}
	if !wDown.stick || !wDown.vp.AtBottom() || wDown.vp.YOffset != bottom {
		t.Fatal("wheel-down at the tail must re-pin")
	}
}

func TestWheelNeverRecallsHistory(t *testing.T) {
	// Scrolling into history must leave the prompt ring and the composer
	// draft untouched: wheel motion is reading, never recalling.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.pushHistory("cmd1")
	m.pushHistory("cmd2")
	for i := 0; i < 50; i++ {
		m.append(fmt.Sprintf("line %d", i))
	}
	m.ta.SetValue("draft")
	nm, _ := m.Update(tea.MouseMsg{X: 0, Y: 0, Button: tea.MouseButtonWheelUp})
	after := nm.(Model)
	if after.ta.Value() != "draft" {
		t.Fatalf("wheel must not touch the composer, box=%q", after.ta.Value())
	}
	if after.histIdx != len(after.hist) {
		t.Fatal("wheel must not move the history position")
	}
}

func TestMouseMotionDoesNotScroll(t *testing.T) {
	// Cell-motion tracking streams drag and release events too; only a
	// wheel press scrolls. Motion with no button (MouseButtonNone) and
	// button releases must stay inert.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	for i := 0; i < 50; i++ {
		m.append(fmt.Sprintf("line %d", i))
	}
	m.stick = false
	m.vp.ScrollUp(5)
	y0 := m.vp.YOffset
	s0 := m.stick
	nm, _ := m.Update(tea.MouseMsg{X: 12, Y: 7, Action: tea.MouseActionMotion, Button: tea.MouseButtonNone})
	after := nm.(Model)
	if after.vp.YOffset != y0 || after.stick != s0 {
		t.Fatal("motion events must not scroll the transcript")
	}
	nm2, _ := after.Update(tea.MouseMsg{X: 12, Y: 7, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	after2 := nm2.(Model)
	if after2.vp.YOffset != y0 || after2.stick != s0 {
		t.Fatal("release events must not scroll the transcript")
	}
}

func TestDragSelectCopiesOnRelease(t *testing.T) {
	// In-app drag-select: left press anchors, motion extends, release
	// copies the selected character range (plain text, ANSI stripped)
	// and toasts — no Alt+M, no terminal-native selection involved.
	old := clipboardWrite
	defer func() { clipboardWrite = old }()
	old52 := osc52Write
	defer func() { osc52Write = old52 }()

	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	mm := nm.(Model)
	mm.append("plain \x1b[31mred\x1b[0m end")
	mm.append("middle line")
	mm.append("tail line")

	var got string
	clipboardWrite = func(s string) error { got = s; return nil }
	osc52Write = func(string) bool {
		t.Fatal("OSC 52 must not fire while the clipboard works")
		return false
	}
	// Locate the appended rows in the actual transcript — the banner
	// length is an implementation detail, not something to guess.
	mid, tail := -1, -1
	for i, ln := range mm.lines {
		switch stripANSI(ln) {
		case "middle line":
			mid = i
		case "tail line":
			tail = i
		}
	}
	if mid < 0 || tail != mid+1 {
		t.Fatalf("test setup: expected adjacent appended lines, got mid=%d tail=%d", mid, tail)
	}
	// Press anchors on "middle line"; with the viewport pinned to the
	// tail and everything on screen, YOffset is 0 and rows map 1:1.
	nm, _ = mm.Update(tea.MouseMsg{X: 5, Y: mid, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	after := nm.(Model)
	if !after.selActive || after.selAnchor != mid {
		t.Fatalf("left press must anchor the selection, got %+v", after)
	}
	if !after.selVisible(mid) || after.selVisible(mid+2) {
		t.Fatal("the live drag highlight must cover the anchor row only")
	}
	// Motion extends the head.
	nm, _ = after.Update(tea.MouseMsg{X: 9, Y: mid + 1, Action: tea.MouseActionMotion})
	drag := nm.(Model)
	if !drag.selMoved || drag.selHead != mid+1 {
		t.Fatalf("motion must extend the selection, got %+v", drag)
	}
	// Release copies exactly the selected rows. The returned command
	// must carry the toast's dismiss tick — dropping it froze the
	// "Copied selection" toast on screen for good.
	nm, relCmd := drag.Update(tea.MouseMsg{X: 9, Y: mid + 1, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	done := nm.(Model)
	if !strings.HasSuffix(got, "e line\ntail line") || strings.Contains(got, "plain red") || strings.ContainsRune(got, '\x1b') {
		t.Fatalf("release must copy the selected character range as plain text, got %q", got)
	}
	if !strings.Contains(done.toast, "Copied selection") {
		t.Fatalf("release must toast receipt, got %q", done.toast)
	}
	if done.selActive {
		t.Fatal("release must end the selection")
	}
	if relCmd == nil {
		t.Fatal("release must return the toast dismiss command")
	}
	for _, msg := range execCmdChain(t, relCmd) {
		if _, ok := msg.(toastTickMsg); ok {
			nm, _ = done.Update(msg)
			done = nm.(Model)
		}
	}
	if done.toast != "" {
		t.Fatalf("the copied toast must auto-dismiss, got %q", done.toast)
	}
}

func TestDragSelectionUsesTerminalCellsForUnicode(t *testing.T) {
	// Mouse coordinates are terminal cells, not Go rune indexes: 界 is two
	// cells and 🙂 is two cells. Copy and highlight must therefore use the
	// same half-open cell range or the pointer and clipboard disagree.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.lines = []string{"a界🙂z"}
	m.pasteEcho = map[int][]pasteSeg{}
	m.selActive = true
	m.selAnchor, m.selHead = 0, 0
	m.selAnchorX, m.selHeadX = 1, 5 // the two wide glyphs, excluding a/z

	if got := m.selectedText(); got != "界🙂" {
		t.Fatalf("cell selection copied %q, want %q", got, "界🙂")
	}
	from, to, ok := m.selectionRange(0)
	if !ok || from != 1 || to != 5 {
		t.Fatalf("cell selection range = (%d, %d, %v), want (1, 5, true)", from, to, ok)
	}
}

func TestDragSelectBareClickCopiesNothing(t *testing.T) {
	// A click without drag is navigation, not a copy — no clipboard
	// touch, no toast, selection cleanly reset.
	old := clipboardWrite
	defer func() { clipboardWrite = old }()
	old52 := osc52Write
	defer func() { osc52Write = old52 }()
	clipboardWrite = func(string) error {
		t.Fatal("a bare click must not touch the clipboard")
		return nil
	}
	osc52Write = func(string) bool {
		t.Fatal("a bare click must not fall back to OSC 52")
		return false
	}
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.append("one")
	nm, _ := m.Update(tea.MouseMsg{X: 3, Y: 1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	after := nm.(Model)
	nm, _ = after.Update(tea.MouseMsg{X: 3, Y: 1, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	done := nm.(Model)
	if done.selActive || done.toast != "" || done.toastAmber {
		t.Fatalf("a bare click must leave no trace, got %+v toast=%q", done, done.toast)
	}
}

func TestMousePassthroughToggle(t *testing.T) {
	// Alt+M arms passthrough: wheel becomes inert (native selection
	// owns the mouse), the hint bar announces the mode, and Alt+M again
	// re-arms wheel scrolling.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	for i := 0; i < 50; i++ {
		m.append(fmt.Sprintf("line %d", i))
	}
	bottom := m.vp.YOffset

	// Arm: Alt+m (lowercase and uppercase both toggle).
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true})
	after := nm.(Model)
	if !after.mousePass {
		t.Fatal("Alt+m must arm mouse passthrough")
	}
	if cmd == nil {
		t.Fatal("arming passthrough must return a command")
	}
	if !strings.Contains(after.toast, "passthrough") {
		t.Fatalf("arming must toast the mode change, got %q", after.toast)
	}
	if got := stripANSI(after.hintBar("base")); !strings.Contains(got, "Alt+M") {
		t.Fatalf("passthrough must be announced in the hint bar, got %q", got)
	}
	// Wheel is inert while armed.
	nm, _ = after.Update(tea.MouseMsg{X: 0, Y: 0, Button: tea.MouseButtonWheelUp})
	inert := nm.(Model)
	if inert.vp.YOffset != bottom || inert.stick != after.stick {
		t.Fatal("wheel must not scroll while passthrough is armed")
	}
	if !inert.mousePass {
		t.Fatal("a wheel event must not silently disarm passthrough")
	}
	// Disarm: Alt+M re-arms scrolling.
	nm, _ = inert.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}, Alt: true})
	armed := nm.(Model)
	if armed.mousePass {
		t.Fatal("Alt+M again must disarm passthrough")
	}
	if !strings.Contains(armed.toast, "re-armed") {
		t.Fatalf("disarming must toast the mode change, got %q", armed.toast)
	}
	nm, _ = armed.Update(tea.MouseMsg{X: 0, Y: 0, Button: tea.MouseButtonWheelUp})
	back := nm.(Model)
	if back.vp.YOffset >= bottom {
		t.Fatal("wheel must scroll again after disarming passthrough")
	}
	// The returned command chain is runnable (Sequence → toast + the
	// runtime mouse command for the renderer).
	msgs := execCmdChain(t, cmd)
	if len(msgs) == 0 {
		t.Fatal("toggle must return a runnable command chain")
	}
}

// execCmdChain resolves a tea.Cmd and any Batch/Sequence children into
// their concrete messages.
func execCmdChain(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	var out []tea.Msg
	msg := cmd()
	if b, ok := msg.(tea.BatchMsg); ok {
		for _, c := range b {
			out = append(out, execCmdChain(t, c)...)
		}
		return out
	}
	out = append(out, msg)
	return out
}

func TestAppendWrapsWideLines(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.vp.Width = 40
	runeCount := func(s string) int {
		return len([]rune(strings.ReplaceAll(stripANSI(s), "\n", "")))
	}
	before := runeCount(strings.Join(m.lines, "\n")) // splash counts too
	wide := lipgloss.NewStyle().Foreground(fgMuted).Render(strings.Repeat("w", 120))
	m.append(wide)
	if got := runeCount(strings.Join(m.lines, "\n")) - before; got != 120 {
		t.Fatalf("wrap must preserve content, added %d runes, want 120", got)
	}
}

func TestQuestionOpensHelpOnEmptyComposer(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	after := nm.(Model)
	if !after.helpOpen {
		t.Fatal("? on an empty composer must open the help overlay")
	}
	if after.ta.Value() != "" {
		t.Fatalf("? must not land in the composer, got %q", after.ta.Value())
	}
	if got := after.View(); !strings.Contains(got, "Keybindings") {
		t.Fatalf("help overlay must render the keybinding reference, got %q", got)
	}
	// Esc closes the overlay; a second ? reopens it (toggle).
	nm, _ = after.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if nm.(Model).helpOpen {
		t.Fatal("Esc must close the help overlay")
	}
	// ? with draft text is literal input, not an overlay.
	m2 := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m2.ta.InsertString("what")
	nm, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if nm.(Model).helpOpen {
		t.Fatal("? with draft text must type literally, not open help")
	}
}

func TestOverlaysStayInsideNarrowFrame(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	m = nm.(Model)
	for _, view := range []string{
		m.View(),
		helpView(m.vp.Width),
		confirmFooter("shell_command", map[string]any{
			"command": "git diff --check && git status --short --branch",
			"reason":  "The model needs approval before running this command.",
		}, m.vp.Width, false),
	} {
		for _, line := range strings.Split(view, "\n") {
			if got := ansi.StringWidth(stripANSI(line)); got > m.vp.Width {
				t.Fatalf("overlay line exceeds frame: %d > %d: %q", got, m.vp.Width, stripANSI(line))
			}
		}
	}
}

func TestTruncMiddlePreservesUnicode(t *testing.T) {
	got := truncMiddle("日本語のパス/文件.md", 10)
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Fatalf("Unicode truncation must remain valid and show an ellipsis: %q", got)
	}
}

func TestUntrustedTranscriptTextCannotInjectTerminalControls(t *testing.T) {
	malicious := "before\x1b]52;c;SGVjcmV0\aafter\x1b[2J"
	for name, got := range map[string]string{
		"tool result": renderToolResult(malicious),
		"thinking":    renderThinking(malicious),
		"markdown":    renderMarkdownText(malicious, 60),
	} {
		if strings.Contains(got, "\x1b]52") || strings.Contains(got, "\x1b[2J") {
			t.Fatalf("%s retained terminal control data: %q", name, got)
		}
		if !strings.Contains(stripANSI(got), "before") || !strings.Contains(stripANSI(got), "after") {
			t.Fatalf("%s lost visible content while stripping controls: %q", name, got)
		}
	}
}

func TestPickReasonVerbCycle(t *testing.T) {
	// Fixed order injected: 2s cadence, wraps — deterministic under test.
	identity := []int{0, 1, 2, 3, 4, 5}
	want := []string{"Reading", "Mapping", "Tracing", "Probing", "Weighing", "Drafting", "Reading"}
	for i, w := range want {
		if got := pickReasonVerb(identity, time.Duration(i)*2*time.Second); got != w {
			t.Fatalf("step %d: got %q, want %q", i, got, w)
		}
	}
	if got := pickReasonVerb(identity, 3*time.Second); got != "Mapping" {
		t.Fatalf("mid-cadence must hold the current verb, got %q", got)
	}
	if got := pickReasonVerb(nil, 0); got != "Reading" {
		t.Fatalf("empty order must fall back to natural order, got %q", got)
	}
}

func TestShuffleVerbsIsFullCycle(t *testing.T) {
	// Whatever the RNG says, every verb appears exactly once per cycle —
	// no repeats (a repeat reads as frozen), no omissions.
	for seed := int64(0); seed < 20; seed++ {
		seen := map[int]int{}
		for _, idx := range shuffleVerbs(rand.New(rand.NewSource(seed))) {
			seen[idx]++
		}
		if len(seen) != len(reasonVerbs) {
			t.Fatalf("seed %d: cycle covers %d of %d verbs: %v", seed, len(seen), len(reasonVerbs), seen)
		}
		for idx, n := range seen {
			if n != 1 || idx < 0 || idx >= len(reasonVerbs) {
				t.Fatalf("seed %d: bad cycle entry %d x%d", seed, idx, n)
			}
		}
	}
}

func TestReasonVerbGating(t *testing.T) {
	now := time.Now()
	idle := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	if _, ok := idle.reasonVerb(); ok {
		t.Fatal("idle session must not show reasoning verbs")
	}
	running := idle
	running.turnStart = now.Add(-10 * time.Second)
	running.quietSince = now.Add(-time.Second) // chattering tools: below floor
	if _, ok := running.reasonVerb(); ok {
		t.Fatal("transcript activity below the 2s floor must stay Working")
	}
	running.quietSince = now.Add(-5 * time.Second) // model wait: past floor
	v, ok := running.reasonVerb()
	if !ok {
		t.Fatal("quiet past the floor must flip to a reasoning verb")
	}
	if !strings.HasPrefix(v, "◆ ") || !strings.Contains(v, " 10s") {
		t.Fatalf("verb shape must be '◆ <Verb> <turn>s', got %q", v)
	}
	running.confirm = &confirmState{}
	if _, ok := running.reasonVerb(); ok {
		t.Fatal("open confirm waits on the user, never the model — no verb")
	}
}

func TestStatusBarReasonVerb(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.turnStart = time.Now().Add(-9 * time.Second)
	m.quietSince = time.Now().Add(-5 * time.Second)
	got := stripANSI(m.statusBar())
	if !strings.Contains(got, "◆") || !strings.Contains(got, "Esc×2 cancels") {
		t.Fatalf("quiet running turn must show dim verb + cancel affordance: %q", got)
	}
	if strings.Contains(got, "Working") {
		t.Fatalf("verb must replace Working, not join it: %q", got)
	}
	m.quietSince = time.Now() // fresh activity: back to Working
	if got := stripANSI(m.statusBar()); !strings.Contains(got, "Working") || strings.Contains(got, "◆") {
		t.Fatalf("fresh activity must restore Working: %q", got)
	}
}

func TestTabCyclesModeViaUpdate(t *testing.T) {
	// P0 regression: `return m, m.handleModeCmd(...)` evaluated the
	// leading model copy before the pointer-receiver call, so Tab
	// silently swallowed the mode change. Driving through Update (the
	// real path) pins the returned model to the mutation.
	loop := newTestLoop()
	m := New(loop, mode.Plan, t.TempDir(), "ollama/m", 32000)
	for i, want := range []mode.Mode{mode.Build, mode.Auto, mode.Plan} {
		nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = nm.(Model)
		if m.curMode != want {
			t.Fatalf("tab %d: mode=%v, want %v", i, m.curMode, want)
		}
		if m.toast == "" {
			t.Fatalf("tab %d: mode change must toast", i)
		}
		wantBanners := 0
		if want == mode.Plan {
			wantBanners = 1
		}
		if got := countBanner(m); got != wantBanners {
			t.Fatalf("tab %d: banner count=%d, want %d for mode %s", i, got, wantBanners, want)
		}
	}
	// Tab while running is no longer a silent swallow: it surfaces the
	// working toast instead.
	m.running = true
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if after := nm.(Model); after.toast == "" || after.curMode != m.curMode {
		t.Fatalf("running Tab must toast without changing mode: %+v", after)
	}
}

func TestSessionsSubmitOpensResumeViaUpdate(t *testing.T) {
	// /sessions typed + Enter must open the resume picker. Through
	// Update: submit's runSlash mutations (slashOpen=false, resumeOpen
	// via OpenResume) have to survive in the returned model.
	t.Setenv("HOME", t.TempDir())
	dir, err := sessionsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := `{"ts":"2026-09-05T00:00:00Z","type":"meta","data":{"root":"/tmp/x"}}
{"ts":"2026-09-05T00:00:01Z","type":"user","data":{"content":"do the thing"}}
`
	if err := os.WriteFile(filepath.Join(dir, "session_abc.jsonl"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	loop := newTestLoop()
	m := New(loop, mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.ta.SetValue("/sessions")
	m.refreshPickers()
	if !m.slashOpen {
		t.Fatal("palette should be open for /sessions")
	}
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after := nm.(Model)
	if after.slashOpen || !after.resumeOpen {
		t.Fatalf("submit must close palette and open resume: slashOpen=%v resumeOpen=%v",
			after.slashOpen, after.resumeOpen)
	}
	if len(after.resumeItems) != 1 {
		t.Fatalf("resume picker must list the session: %+v", after.resumeItems)
	}
}

func TestPaletteEnterExecutesViaUpdate(t *testing.T) {
	// Palette ↑↓ + Enter runs the highlighted row through updatePicker's
	// runSlash call — its mutations must persist in the returned model.
	loop := newTestLoop()
	m := New(loop, mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.ta.SetValue("/mode build")
	m.refreshPickers()
	if !m.slashOpen || len(m.slashItems) == 0 {
		t.Fatal("palette should offer /mode")
	}
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after := nm.(Model)
	if after.curMode != mode.Build {
		t.Fatalf("palette Enter must switch mode, got %v", after.curMode)
	}
	if after.slashOpen || after.toast == "" {
		t.Fatalf("palette must close and toast: slashOpen=%v toast=%q", after.slashOpen, after.toast)
	}
}

func TestRenderMarkdownBold(t *testing.T) {
	// The literal-asterisks bug: agent prose must render strong text,
	// never leak raw markdown into the transcript.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	got := m.renderMarkdown("Hello! I'm **tilde**, an expert.", 60)
	if strings.Contains(got, "**") {
		t.Fatalf("raw markdown leaked into transcript: %q", got)
	}
	if !strings.Contains(got, "\x1b[1m") && !strings.Contains(got, ";1m") {
		t.Fatalf("strong text must render bold: %q", got)
	}
	if plain := stripANSI(got); !strings.Contains(plain, "Hello! I'm tilde, an expert.") {
		t.Fatal("rendering must preserve all visible content")
	}
	// Glamour pads wrapped lines with styled spaces — the transcript
	// must not inherit trailing whitespace (scrollback bloat, broken
	// copy-paste).
	for _, ln := range strings.Split(got, "\n") {
		if strings.HasSuffix(stripANSI(ln), " ") {
			t.Fatalf("rendered line carries padding: %q", ln)
		}
	}
}

func TestRenderMarkdownWidthAndGrace(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	long := "word " + strings.Repeat("lorem ", 30)
	for _, ln := range strings.Split(m.renderMarkdown(long, 40), "\n") {
		if ansi.StringWidth(ln) > 40 {
			t.Fatalf("rendered line exceeds width: %d: %q", ansi.StringWidth(ln), ln)
		}
	}
	// Truncated fence (as produced by the 500-char resume cap) must
	// degrade gracefully — content preserved, no panic, no raw fence leak
	// beyond the visible text.
	cut := "Intro line\n```go\nfunc broken( {"
	got := renderMarkdownText(cut, 60)
	if plain := stripANSI(got); !strings.Contains(plain, "Intro line") || !strings.Contains(plain, "broken") {
		t.Fatalf("truncated fence must keep visible content: %q", got)
	}
}

func TestAssistantEventRendersMarkdownViaUpdate(t *testing.T) {
	// End-to-end wiring: an assistant agentEventMsg through Update must
	// land Glamour-rendered (bold, no raw asterisks) in the transcript —
	// this is the exact path that produced the literal-**tilde** bug.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	nm, _ := m.Update(agentEventMsg(agent.Event{Kind: "assistant", Text: "Hello! I'm **tilde**, here to help."}))
	after := nm.(Model)
	joined := strings.Join(after.lines, "\n")
	if strings.Contains(joined, "**") {
		t.Fatalf("raw markdown in transcript after Update: %q", joined)
	}
	if !strings.Contains(joined, ";1m") {
		t.Fatalf("assistant prose must render strong text bold: %q", joined)
	}
}

func TestViewportPinsChromeToBottom(t *testing.T) {
	// The frame must land exactly on the terminal height — no dead rows
	// beneath the hint bar (the "spacing issue": a fixed -10 headroom
	// left a permanent gap).
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	after := nm.(Model)
	rows := strings.Count(after.View(), "\n") + 1
	if rows != 30 {
		t.Fatalf("frame must fill the terminal exactly, got %d rows for height 30", rows)
	}
	// A taller composer steals rows from the transcript instead of
	// opening a gap or pushing chrome off-screen.
	after.ta.InsertString("one\ntwo\nthree\n")
	after.syncComposer()
	rows = strings.Count(after.View(), "\n") + 1
	if rows != 30 {
		t.Fatalf("multiline composer must refit, got %d rows for height 30", rows)
	}
}

func TestApprovalFrameFitsSmallTerminal(t *testing.T) {
	// Approval is the highest-risk screen: a wrapped model reason must not
	// push the decision controls below the terminal or hide them in scrollback.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	m.confirm = &confirmState{Tool: "shell_command", Args: map[string]any{
		"command": "git diff --check && git status --short --branch",
		"reason":  strings.Repeat("Please verify the repository state before making this change. ", 4),
	}}
	if rows := strings.Count(m.View(), "\n") + 1; rows > 24 {
		t.Fatalf("approval frame overflows 24-row terminal: %d rows", rows)
	}
}

func TestWideTerminalCentersFrame(t *testing.T) {
	// Wide screens hold a centered maxAppWidth column instead of a
	// full-bleed stretch: content caps, the column carries one straight
	// left edge, and the height contract still holds exactly.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
	after := nm.(Model)
	if after.vp.Width != maxAppWidth-4 {
		t.Fatalf("wide viewport must cap at %d, got %d", maxAppWidth-4, after.vp.Width)
	}
	view := after.View()
	if rows := strings.Count(view, "\n") + 1; rows != 30 {
		t.Fatalf("centered frame must still fill height, got %d rows", rows)
	}
	lines := strings.Split(view, "\n")
	for _, ln := range lines {
		if got := ansi.StringWidth(ln); got > 200 {
			t.Fatalf("centered line overflows the terminal (%d): %q", got, stripANSI(ln))
		}
	}
	margin := -1
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		if lead := len(ln) - len(strings.TrimLeft(ln, " ")); margin < 0 || lead < margin {
			margin = lead
		}
	}
	if margin <= 0 {
		t.Fatalf("wide frame must carry a left margin, got %d", margin)
	}
	// Structural full-width lines (composer/panel borders) define the
	// column: they must share one straight left edge.
	for _, ln := range lines {
		if strings.Contains(ln, "╭") {
			if lead := len(ln) - len(strings.TrimLeft(ln, " ")); lead != margin {
				t.Fatalf("column edge must be straight, border at %d vs edge %d: %q", lead, margin, stripANSI(ln))
			}
		}
	}
	// Narrow terminal: full-bleed, no margin, unchanged behavior.
	nm2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	for _, ln := range strings.Split(nm2.(Model).View(), "\n") {
		if strings.Contains(ln, "╭") && strings.HasPrefix(ln, " ") {
			t.Fatalf("narrow frame must stay full-bleed: %q", stripANSI(ln))
		}
	}
}

func TestSplashBudgetSpaceBetween(t *testing.T) {
	// Space-between: Budget docks left, Sandbox docks right, the gap
	// computed from the live width — never a fixed pad.
	for _, w := range []int{100, 116} {
		var line string
		for _, l := range splashLines("/tmp/root", "ollama/m", 32000, w) {
			if strings.HasPrefix(l, "  Budget:") {
				line = l
			}
		}
		if line == "" {
			t.Fatalf("width %d: missing Budget line", w)
		}
		if got := ansi.StringWidth(line); got != w {
			t.Fatalf("width %d: Budget line must fill exactly %d, got %d: %q", w, w, got, stripANSI(line))
		}
		if !strings.HasSuffix(line, sandbox.StatusLine()) {
			t.Fatalf("width %d: Sandbox must dock right: %q", w, stripANSI(line))
		}
		if !strings.HasPrefix(line, "  Budget: 32.0k tokens") {
			t.Fatalf("width %d: Budget must dock left: %q", w, stripANSI(line))
		}
	}
}

func TestDoneReceiptNeedsWork(t *testing.T) {
	// A pure chat reply ends without applause; a turn that dispatched
	// tool calls closes with the ✓ Done receipt.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	base := len(m.lines)
	m.renderEvent(agent.Event{Kind: "assistant", Text: "hi"})
	m.renderEvent(agent.Event{Kind: "done", Text: "Done"})
	for _, ln := range m.lines[base:] {
		if strings.Contains(stripANSI(ln), "Done") {
			t.Fatalf("pure chat turn must not render a Done receipt: %q", stripANSI(ln))
		}
	}
	m2 := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	base2 := len(m2.lines)
	m2.renderEvent(agent.Event{Kind: "tool_call", Text: "read_file f.txt"})
	m2.renderEvent(agent.Event{Kind: "tool_result", Text: "ok"})
	m2.renderEvent(agent.Event{Kind: "done", Text: "Done"})
	found := false
	for _, ln := range m2.lines[base2:] {
		if strings.Contains(stripANSI(ln), "✓ Done") {
			found = true
		}
	}
	if !found {
		t.Fatal("working turn must close with ✓ Done")
	}
}

func TestFooterBreathingRoom(t *testing.T) {
	// One blank spacer above the status bar and one above the hint bar;
	// the frame still lands exactly on the terminal height.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	after := nm.(Model)
	view := strings.Split(after.View(), "\n")
	if len(view) != 30 {
		t.Fatalf("frame must fill the terminal exactly, got %d rows", len(view))
	}
	// Tail shape: … composer, "", status, "", hint.
	if strings.TrimSpace(stripANSI(view[len(view)-4])) != "" {
		t.Fatalf("composer and status bar need one blank line between, got %q", stripANSI(view[len(view)-4]))
	}
	if !strings.Contains(stripANSI(view[len(view)-3]), "Plan") {
		t.Fatalf("expected the status bar above the hint, got %q", stripANSI(view[len(view)-3]))
	}
	if strings.TrimSpace(stripANSI(view[len(view)-2])) != "" {
		t.Fatalf("status bar and hint bar need one blank line between, got %q", stripANSI(view[len(view)-2]))
	}
	if !strings.Contains(stripANSI(view[len(view)-1]), "/ commands") {
		t.Fatalf("expected the hint bar last, got %q", stripANSI(view[len(view)-1]))
	}
}

func TestQueryResponseGap(t *testing.T) {
	// One blank row separates the user echo from whatever answers it.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.ta.SetValue("hello")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after := nm.(Model)
	idx := -1
	for i, ln := range after.lines {
		if strings.Contains(stripANSI(ln), "→ hello") {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("user echo missing from transcript")
	}
	if idx+1 >= len(after.lines) || strings.TrimSpace(stripANSI(after.lines[idx+1])) != "" {
		t.Fatalf("one blank row must separate query and response, got %q", after.lines[idx+1])
	}
}

func TestTrimRenderedCollapsesBlankRuns(t *testing.T) {
	if got := trimRendered("a\n\n\n\nb"); got != "a\n\nb" {
		t.Fatalf("blank runs must collapse to one, got %q", got)
	}
	if got := trimRendered("\n\n\na\n\n"); got != "a" {
		t.Fatalf("leading/trailing blanks must drop, got %q", got)
	}
	got := renderMarkdownText("para one\n\n\n\npara two", 60)
	plain := stripANSI(got)
	if !strings.Contains(plain, "para one") || !strings.Contains(plain, "para two") {
		t.Fatalf("content lost: %q", plain)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("triple newline survived: %q", plain)
	}
}

func TestHintBarCentered(t *testing.T) {
	// The hint bar is the footer's quiet closer: centered in the frame
	// with balanced margins, filling the frame width exactly.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	after := nm.(Model)
	view := strings.Split(after.View(), "\n")
	last := view[len(view)-1]
	plain := stripANSI(last)
	if !strings.Contains(plain, "/ commands") {
		t.Fatalf("last line must be the hint bar, got %q", plain)
	}
	// Centered by left pad only: content lands centered with no trailing
	// whitespace to pollute terminal copy/paste.
	if strings.HasSuffix(plain, " ") {
		t.Fatalf("hint must not trail whitespace, got %q", plain)
	}
	lead := len(last) - len(strings.TrimLeft(last, " "))
	if want := (after.vp.Width - len([]rune(strings.TrimSpace(plain)))) / 2; lead != want {
		t.Fatalf("hint must center at margin %d, got %d: %q", want, lead, plain)
	}
	// Narrow screen: an overlong hint hard-cuts instead of spilling.
	nm2, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	after2 := nm2.(Model)
	nlast := stripANSI(strings.Split(after2.View(), "\n")[29])
	if got := len([]rune(nlast)); got > after2.vp.Width {
		t.Fatalf("narrow hint must not spill, width %d: %q", got, nlast)
	}
	if !strings.HasSuffix(nlast, "…") {
		t.Fatalf("narrow hint must ellipsize, got %q", nlast)
	}
}

func TestShellArmingPlaceholder(t *testing.T) {
	// A leading "!" arms shell mode but stays typeable in the box — the
	// shell signal is the hint row below (same slot as / and @), since a
	// placeholder only ever shows on an empty box.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	if !strings.Contains(m.ta.Placeholder, "Plan") {
		t.Fatalf("baseline must be the mode hint, got %q", m.ta.Placeholder)
	}
	m.ta.SetValue("!")
	m.refreshPickers()
	if !m.shellArmed {
		t.Fatal("leading ! must arm shell mode")
	}
	if m.ta.Value() != "!" {
		t.Fatalf("bang must stay typeable in the box, got %q", m.ta.Value())
	}
	// The armed signal is the dropdown row, visible in the frame.
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	found := false
	for _, ln := range strings.Split(nm.(Model).View(), "\n") {
		if strings.Contains(stripANSI(ln), "run as shell command") {
			found = true
		}
	}
	if !found {
		t.Fatal("armed box must show the shell hint row")
	}
	// Typing keeps bang + armed state; clearing the box disarms.
	m.ta.SetValue("!git status")
	m.refreshPickers()
	if !m.shellArmed || m.ta.Value() != "!git status" {
		t.Fatalf("typing must preserve bang and arming: armed=%v box=%q", m.shellArmed, m.ta.Value())
	}
	m.ta.SetValue("")
	m.refreshPickers()
	if m.shellArmed {
		t.Fatal("empty box must disarm shell mode")
	}
	// Mid-text bangs are literal — never an arming.
	m.ta.SetValue("echo !important")
	m.refreshPickers()
	if m.shellArmed {
		t.Fatal("mid-text ! must not arm shell mode")
	}
	if m.ta.Value() != "echo !important" {
		t.Fatalf("mid-text value must pass through untouched, got %q", m.ta.Value())
	}
}

func TestShellArmedSubmitRunsCommand(t *testing.T) {
	// An armed submit takes the shell path with the bang intact. The
	// zero-value test loop gates shell_command in Plan, so the refusal
	// itself proves the shell branch — an agent turn would have set
	// running instead.
	m := New(newTestLoop(), mode.Build, t.TempDir(), "ollama/m", 32000)
	m.ta.SetValue("!git status")
	m.refreshPickers()
	if !m.shellArmed || m.ta.Value() != "!git status" {
		t.Fatalf("setup: armed=%v box=%q", m.shellArmed, m.ta.Value())
	}
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after := nm.(Model)
	if after.running {
		t.Fatal("armed submit must take the shell path, not an agent turn")
	}
	last := stripANSI(after.lines[len(after.lines)-1])
	if !strings.Contains(last, "Plan mode") {
		t.Fatalf("expected the shell mode-gate refusal, got %q", last)
	}
	if after.shellArmed {
		t.Fatal("submit must disarm")
	}
}

func TestGroupReadsUnderParent(t *testing.T) {
	// Consecutive read-only pairs buffer and flush as one parent plus
	// indented glyph-less children on the next event. Successful reads
	// carry no result lines (spec §2.10) — the call line IS the receipt —
	// but a grep count survives on its call line.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	base := len(m.lines)
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "read_file a.txt"})
	m.renderEvent(agent.Event{Kind: "tool_result", Text: "content a"})
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "grep foo"})
	m.renderEvent(agent.Event{Kind: "tool_result", Text: "a.txt:1:foo"})
	if len(m.lines) != base {
		t.Fatalf("pairs must buffer until flushed, got %d new lines", len(m.lines)-base)
	}
	m.renderEvent(agent.Event{Kind: "assistant", Text: "hi"})
	got := m.lines[base:]
	if len(got) != 4 {
		t.Fatalf("parent + 2 calls + assistant = 4 lines (results suppressed), got %d:\n%s", len(got), stripANSI(strings.Join(got, "\n")))
	}
	parent := stripANSI(got[0])
	if !strings.HasPrefix(parent, "● ") || !strings.Contains(parent, "×2") {
		t.Fatalf("parent must landmark the run, got %q", parent)
	}
	if !strings.Contains(parent, "Read") || !strings.Contains(parent, "Grep") {
		t.Fatalf("parent must name both kinds in transcript vocabulary, got %q", parent)
	}
	if strings.Contains(parent, "read_file") {
		t.Fatalf("parent must not leak raw tool names, got %q", parent)
	}
	for _, ln := range got[1:3] {
		if !strings.HasPrefix(ln, "  ") {
			t.Fatalf("children must indent 2 spaces, got %q", stripANSI(ln))
		}
		if strings.HasPrefix(strings.TrimLeft(ln, " "), "●") {
			t.Fatalf("children must drop the glyph, got %q", stripANSI(ln))
		}
	}
	if child := stripANSI(got[1]); !strings.Contains(child, "Read") || strings.Contains(child, "read_file") {
		t.Fatalf("child must use transcript vocabulary, got %q", child)
	}
	if child := stripANSI(got[2]); !strings.Contains(child, "1 match in 1 file") {
		t.Fatalf("grep child must carry its count, got %q", child)
	}
	if !strings.Contains(stripANSI(got[3]), "hi") {
		t.Fatalf("assistant must follow the group, got %q", stripANSI(got[3]))
	}
}

func TestSinglePairUngrouped(t *testing.T) {
	// One pair is not a group: call line only (a lone read carries no
	// result line per spec §2.10), no parent.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	base := len(m.lines)
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "read_file a.txt"})
	m.renderEvent(agent.Event{Kind: "tool_result", Text: "content a"})
	m.renderEvent(agent.Event{Kind: "assistant", Text: "hi"})
	got := m.lines[base:]
	if len(got) != 2 {
		t.Fatalf("single call + assistant = 2 lines (result suppressed), got %d", len(got))
	}
	if plain := stripANSI(got[0]); !strings.HasPrefix(plain, "● ") || strings.Contains(plain, "×") {
		t.Fatalf("lone call keeps its glyph, no parent: %q", plain)
	}
}

func TestThoughtReceipt(t *testing.T) {
	// Provider-reported tokens: wall time plus generation rate.
	loop := newTestLoop()
	loop.TotCompletion = 82
	m := New(loop, mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.running = true
	m.turnStart = time.Now().Add(-3 * time.Second)
	nm, _ := m.Update(agentDoneMsg{NoDone: true})
	after := nm.(Model)
	joined := stripANSI(strings.Join(after.lines, "\n"))
	if !strings.Contains(joined, "◆ Thought for") {
		t.Fatalf("turn must close with a performance receipt:\n%s", joined)
	}
	if !strings.Contains(joined, "tok/s") {
		t.Fatalf("known tokens must show the rate:\n%s", joined)
	}
	if after.running {
		t.Fatal("done must stop the turn")
	}
	// Unknown tokens: wall time only, never an invented rate.
	m2 := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m2.running = true
	m2.turnStart = time.Now().Add(-3 * time.Second)
	nm2, _ := m2.Update(agentDoneMsg{NoDone: true})
	j2 := stripANSI(strings.Join(nm2.(Model).lines, "\n"))
	if !strings.Contains(j2, "◆ Thought for") || strings.Contains(j2, "tok/s") {
		t.Fatalf("unknown tokens: time yes, rate no:\n%s", j2)
	}
	// Shell escape (no turn started): command Done stays, no receipt.
	m3 := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	nm3, _ := m3.Update(agentDoneMsg{})
	j3 := stripANSI(strings.Join(nm3.(Model).lines, "\n"))
	if !strings.Contains(j3, "✓ Done") {
		t.Fatalf("shell done must stay:\n%s", j3)
	}
	if strings.Contains(j3, "Thought for") {
		t.Fatalf("no turn, no receipt:\n%s", j3)
	}
	// Below the live-verb floor: success still closes with ✓ Done, but
	// the ◆ Thought receipt is skipped (spec §2.20).
	m4 := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m4.running = true
	m4.turnStart = time.Now().Add(-500 * time.Millisecond)
	nm4, _ := m4.Update(agentDoneMsg{NoDone: true})
	j4 := stripANSI(strings.Join(nm4.(Model).lines, "\n"))
	if strings.Contains(j4, "Thought for") {
		t.Fatalf("sub-floor turn must skip the receipt:\n%s", j4)
	}
}

func TestAtDropdownSelectedKeepsMatchSpans(t *testing.T) {
	// The selected @-row must keep the bold match spans: the
	// accentSelect background is composed per highlight run, not via
	// the plain truncMiddle path (which drops the spans).
	rows := matchFiles([]string{"internal/provider/stream.go"}, "stream")
	if len(rows) == 0 {
		t.Fatal("matchFiles must match stream.go for query stream")
	}
	got := atDropdown(rows, 0, 60)
	if stripped := stripANSI(got); !strings.Contains(stripped, "internal/provider/stream.go") {
		t.Fatalf("selected row must carry the full path: %q", stripped)
	}
	// Bold assertion only when the lipgloss profile emits SGR at all
	// (plain test runs default to Ascii, which strips styling).
	if strings.Contains(highlight(rows[0].path, rows[0].idx), "\x1b[") {
		if !strings.Contains(got, "\x1b[1m") {
			t.Fatalf("selected row dropped the bold match spans:\n%q", got)
		}
	}
}

func TestVerbTickRearmsWhileRunning(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.running = true
	if _, cmd := m.Update(verbTickMsg{}); cmd == nil {
		t.Fatal("tick must re-arm while a turn runs")
	}
	m.running = false
	if _, cmd := m.Update(verbTickMsg{}); cmd != nil {
		t.Fatal("tick must die when idle")
	}
}

func TestHistoryRecallRing(t *testing.T) {
	// Shell contract: Up walks back, Down walks forward, the live draft
	// is stashed at the bottom and restored verbatim.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.pushHistory("first")
	m.pushHistory("second")
	m.ta.SetValue("draft")
	up := func() Model {
		nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
		m = nm.(Model)
		return m
	}
	down := func() Model {
		nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = nm.(Model)
		return m
	}
	if got := up().ta.Value(); got != "second" {
		t.Fatalf("Up must recall last, got %q", got)
	}
	if got := up().ta.Value(); got != "first" {
		t.Fatalf("Up must walk back, got %q", got)
	}
	up() // top of ring: holds, keeps the entry
	if got := m.ta.Value(); got != "first" {
		t.Fatalf("top must hold position, got %q", got)
	}
	if got := down().ta.Value(); got != "second" {
		t.Fatalf("Down must walk forward, got %q", got)
	}
	if got := down().ta.Value(); got != "draft" {
		t.Fatalf("bottom must restore the exact draft, got %q", got)
	}
	// Down past the bottom declines (transcript keeps the key).
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = nm.(Model)
	if got := m.ta.Value(); got != "draft" {
		t.Fatalf("past-bottom Down must leave the draft, got %q", got)
	}
	// New typing abandons the position (blink ticks must not).
	m.ta.SetValue("second")
	m.refreshPickers()
	up()
	m.ta.SetValue("second!")
	m.refreshPickers()
	if m.histIdx != len(m.hist) {
		t.Fatal("typing must abandon history navigation")
	}
	// Consecutive duplicates collapse; the ring caps.
	m.pushHistory("x")
	m.pushHistory("x")
	if len(m.hist) == 0 || m.hist[len(m.hist)-1] != "x" {
		t.Fatalf("history tail wrong: %v", m.hist)
	}
	for i := 0; i < maxHistoryEntries+10; i++ {
		m.pushHistory(strings.Repeat("c", 3) + string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	if len(m.hist) != maxHistoryEntries {
		t.Fatalf("ring must cap at %d, got %d", maxHistoryEntries, len(m.hist))
	}
}

func TestHistoryEmptyFallsThroughToScroll(t *testing.T) {
	// No entries: Up declines so transcript scrolling still gets the key.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	for i := 0; i < 50; i++ {
		m.append(strings.Repeat("l", 10))
	}
	m.ta.SetValue("")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	after := nm.(Model)
	if after.stick {
		t.Fatal("empty history must fall through to transcript scroll (unpin)")
	}
}

func TestComposerCapsHeight(t *testing.T) {
	// Long drafts grow the box only up to maxComposerRows; the status
	// bar stays pinned because fitViewport steals the rows.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.ta.SetValue(strings.Repeat("x\n", 30))
	m.syncComposer()
	if m.ta.Height() != maxComposerRows {
		t.Fatalf("composer must cap at %d rows, got %d", maxComposerRows, m.ta.Height())
	}
}

func TestTurnBoundaryPadding(t *testing.T) {
	// First query on a fresh session: no leading gap (the splash ends
	// on its own air). Every later query opens with exactly one blank
	// row, so a prompt never sits flush on the previous receipt.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.ta.SetValue("hello")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after := nm.(Model)
	n := len(after.lines)
	if !strings.Contains(stripANSI(after.lines[n-2]), "→ hello") {
		t.Fatalf("first echo missing, tail:\n%s", stripANSI(strings.Join(after.lines[n-3:], "\n")))
	}
	if strings.TrimSpace(stripANSI(after.lines[n-3])) == "" &&
		strings.TrimSpace(stripANSI(after.lines[n-4])) == "" {
		t.Fatal("first query must not add a boundary gap on top of the splash")
	}
	// Second turn: one blank row between the previous close and the echo.
	after.running = false
	after.ta.SetValue("build it")
	nm2, _ := after.Update(tea.KeyMsg{Type: tea.KeyEnter})
	lines2 := nm2.(Model).lines
	idx := -1
	for i, ln := range lines2 {
		if strings.Contains(stripANSI(ln), "→ build it") {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("second echo missing")
	}
	if idx < 3 || strings.TrimSpace(stripANSI(lines2[idx-1])) != "" ||
		strings.TrimSpace(stripANSI(lines2[idx-2])) != "" {
		t.Fatalf("one blank row must open the turn, got %q then %q",
			stripANSI(lines2[idx-1]), stripANSI(lines2[idx-2]))
	}
	if !strings.Contains(stripANSI(lines2[idx-3]), "→ hello") {
		t.Fatalf("gap must sit directly after the previous turn, got %q", stripANSI(lines2[idx-3]))
	}
}

func TestRenderThinkingShape(t *testing.T) {
	// First line carries ◇, the rest indent — the quiet-gutter voice,
	// never the ◆ timing receipt.
	got := renderThinking("first\nsecond")
	lines := strings.Split(got, "\n")
	if len(lines) != 2 || !strings.HasPrefix(stripANSI(lines[0]), "◇ first") {
		t.Fatalf("head wrong: %q", got)
	}
	if !strings.HasPrefix(stripANSI(lines[1]), "  second") {
		t.Fatalf("body must indent: %q", got)
	}
	if strings.Contains(got, "◆") {
		t.Fatalf("thinking glyph must stay hollow: %q", got)
	}
	// Blank runs collapse; long traces clamp with an announced notice.
	if got := renderThinking("a\n\n\n\nb"); strings.Contains(got, "\n\n\n") {
		t.Fatalf("blank runs must collapse: %q", got)
	}
	var big strings.Builder
	for i := 0; i < maxThinkingLines+10; i++ {
		big.WriteString("r\n")
	}
	out := renderThinking(big.String())
	if n := len(strings.Split(out, "\n")); n != maxThinkingLines+1 {
		t.Fatalf("clamp to %d + notice, got %d lines", maxThinkingLines, n)
	}
	if !strings.Contains(out, "session log keeps the full text") {
		t.Fatalf("clamp must announce the log: %q", stripANSI(out))
	}
}

func TestThinkingEventRendersDim(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	base := len(m.lines)
	m.renderEvent(agent.Event{Kind: "thinking", Text: "considering x"})
	got := m.lines[base:]
	if len(got) != 1 || !strings.Contains(stripANSI(got[0]), "◇ considering x") {
		t.Fatalf("thinking must render one dim ◇ line: %v", got)
	}
}

func TestResumeShowsThinkingAsScrollbackOnly(t *testing.T) {
	log := `{"ts":"2026-09-05T00:00:00Z","type":"user","data":{"content":"hi"}}
{"ts":"2026-09-05T00:00:01Z","type":"thinking","data":{"content":"why x"}}
{"ts":"2026-09-05T00:00:02Z","type":"assistant","data":{"content":"hello"}}
`
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(log), 0o644)
	lines, msgs, err := loadSessionFile(p, 78)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range lines {
		if strings.Contains(stripANSI(l), "◇") && strings.Contains(stripANSI(l), "why x") {
			found = true
		}
	}
	if !found {
		t.Fatalf("past thinking must restore as scrollback: %v", lines)
	}
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "why x") {
			t.Fatalf("past thinking must not re-enter context: %q", msg.Content)
		}
	}
}

func pasteViaUpdate(m Model, text string) Model {
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text), Paste: true})
	return nm.(Model)
}

func TestPasteCollapseToken(t *testing.T) {
	// A 10-line paste collapses: the box shows only the token, the body
	// waits off-screen.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	body := "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10"
	m = pasteViaUpdate(m, body)
	if got := m.ta.Value(); got != "[Pasted text #1 +10 lines]" {
		t.Fatalf("box must hold only the token, got %q", got)
	}
	if len(m.pasteSegs) != 1 || m.pasteSegs[0].body != body {
		t.Fatalf("body must wait off-screen: %+v", m.pasteSegs)
	}
}

func TestPasteSmallStaysInline(t *testing.T) {
	// Below threshold (default 4 lines / 1000 chars): raw inline text,
	// no token machinery involved.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m = pasteViaUpdate(m, "line one\nline two")
	if got := m.ta.Value(); got != "line one\nline two" {
		t.Fatalf("small paste must insert inline, got %q", got)
	}
	if len(m.pasteSegs) != 0 {
		t.Fatalf("no segments for inline pastes: %+v", m.pasteSegs)
	}
}

func TestPasteSubmitSubstitutes(t *testing.T) {
	// Submit sends the real payload (history, model) while the
	// transcript echo keeps the placeholder form.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	body := "a\nb\nc\nd\ne"
	m = pasteViaUpdate(m, body)
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after := nm.(Model)
	if !after.running {
		t.Fatal("submit must start the agent turn")
	}
	joined := stripANSI(strings.Join(after.lines, "\n"))
	if !strings.Contains(joined, "→ [Pasted text #1 +5 lines]") {
		t.Fatalf("echo must keep the placeholder form:\n%s", joined)
	}
	if strings.Contains(joined, "a\nb\nc") {
		t.Fatalf("raw body must not dominate scrollback:\n%s", joined)
	}
	if len(after.hist) == 0 || after.hist[len(after.hist)-1] != body {
		t.Fatalf("history must hold what ran: %v", after.hist)
	}
	if len(after.pasteSegs) != 0 {
		t.Fatal("segments must not outlive their entry")
	}
}

func TestPasteBackspaceUnit(t *testing.T) {
	// Backspace right after a token removes the whole unit, body and all.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m = pasteViaUpdate(m, "a\nb\nc\nd")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	after := nm.(Model)
	if after.ta.Value() != "" {
		t.Fatalf("token must delete as one unit, box=%q", after.ta.Value())
	}
	if len(after.pasteSegs) != 0 {
		t.Fatal("body must go with its token")
	}
}

func TestPasteOrphanNotice(t *testing.T) {
	// A token destroyed mid-edit strands its body: report it at submit,
	// never silently drop it from the turn.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m = pasteViaUpdate(m, "a\nb\nc\nd")
	m.ta.SetValue("[Pasted")
	m.refreshPickers()
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after := nm.(Model)
	joined := stripANSI(strings.Join(after.lines, "\n"))
	if !strings.Contains(joined, "edited away and dropped") {
		t.Fatalf("orphaned body must be announced:\n%s", joined)
	}
}

func TestPasteCollapseEnv(t *testing.T) {
	// 0 disables collapsing entirely; other values set the line floor.
	t.Setenv("TILDE_PASTE_LINES", "0")
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m = pasteViaUpdate(m, "a\nb\nc\nd\ne\nf")
	if len(m.pasteSegs) != 0 || !strings.Contains(m.ta.Value(), "a\nb") {
		t.Fatalf("disabled collapse must insert inline: box=%q segs=%v", m.ta.Value(), m.pasteSegs)
	}
	t.Setenv("TILDE_PASTE_LINES", "2")
	m2 := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m2 = pasteViaUpdate(m2, "one\ntwo")
	if len(m2.pasteSegs) != 1 {
		t.Fatalf("threshold 2 must collapse 2 lines: box=%q", m2.ta.Value())
	}
	t.Setenv("TILDE_PASTE_LINES", "bogus")
	m3 := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m3 = pasteViaUpdate(m3, "a\nb\nc")
	if len(m3.pasteSegs) != 0 {
		t.Fatalf("bad values must fall back to default 4: box=%q", m3.ta.Value())
	}
}

func TestPasteSubmitCapsPayload(t *testing.T) {
	// The input cap binds the expanded payload: a 20k-char paste
	// submits truncated with the standing notice.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	var b strings.Builder
	for i := 0; i < 5; i++ {
		b.WriteString(strings.Repeat("y", 4000))
		b.WriteByte('\n')
	}
	m = pasteViaUpdate(m, b.String())
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after := nm.(Model)
	joined := stripANSI(strings.Join(after.lines, "\n"))
	if !strings.Contains(joined, "truncated to 16,000 chars") {
		t.Fatalf("over-cap payload must announce truncation:\n%s", joined)
	}
	if n := len([]rune(after.hist[len(after.hist)-1])); n != maxInputChars {
		t.Fatalf("history must hold the capped payload, got %d runes", n)
	}
}

func TestCopyKeybindCopiesLastResponse(t *testing.T) {
	// Ctrl+Y pushes the latest assistant prose (raw markdown, not the
	// Glamour rendering) via the stubbed writer and toasts receipt.
	old := clipboardWrite
	defer func() { clipboardWrite = old }()
	var got string
	clipboardWrite = func(s string) error { got = s; return nil }
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.renderEvent(agent.Event{Kind: "assistant", Text: "Hello **tilde**"})
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	after := nm.(Model)
	if got != "Hello **tilde**" {
		t.Fatalf("clipboard must get raw prose, got %q", got)
	}
	if after.toast == "" || !strings.Contains(after.toast, "Copied") {
		t.Fatalf("copy must toast receipt, got %q", after.toast)
	}
	// A second reply re-arms the source.
	after.renderEvent(agent.Event{Kind: "assistant", Text: "second"})
	got = ""
	nm2, _ := after.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	_ = nm2
	if got != "second" {
		t.Fatalf("copy must track the latest reply, got %q", got)
	}
}

func TestCopyKeybindEmptyAndError(t *testing.T) {
	old := clipboardWrite
	defer func() { clipboardWrite = old }()
	old52 := osc52Write
	defer func() { osc52Write = old52 }()
	// No response yet: honest notice, no clipboard touch.
	called := false
	clipboardWrite = func(string) error { called = true; return nil }
	osc52Write = func(string) bool { return true }
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if called {
		t.Fatal("empty copy must not touch the clipboard")
	}
	if toast := nm.(Model).toast; !strings.Contains(toast, "nothing to copy") {
		t.Fatalf("empty copy must say so, got %q", toast)
	}
	// Helper-binary failure but a live terminal: OSC 52 carries it —
	// the terminal sets its own clipboard, no xclip needed.
	clipboardWrite = func(string) error { return errors.New("exec: xclip: not found") }
	var oscGot string
	osc52Write = func(s string) bool { oscGot = s; return true }
	m2 := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m2.renderEvent(agent.Event{Kind: "assistant", Text: "hi"})
	nm2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	after2 := nm2.(Model)
	if oscGot != "hi" || !strings.Contains(after2.toast, "OSC 52") {
		t.Fatalf("fallback must ship via OSC 52, got %q payload=%q", after2.toast, oscGot)
	}
	// Both refuse: amber toast, never fake success.
	osc52Write = func(string) bool { return false }
	m3 := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m3.renderEvent(agent.Event{Kind: "assistant", Text: "hi"})
	nm3, _ := m3.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	after3 := nm3.(Model)
	if !after3.toastAmber || !strings.Contains(after3.toast, "clipboard unavailable") {
		t.Fatalf("total failure must toast amber with the fix, got %q amber=%v", after3.toast, after3.toastAmber)
	}
}

func TestCopySlashTranscript(t *testing.T) {
	// /copy ships the transcript (or one line) as plain text — styling
	// stripped — via the clipboard, with OSC 52 as the fallback.
	old := clipboardWrite
	defer func() { clipboardWrite = old }()
	old52 := osc52Write
	defer func() { osc52Write = old52 }()
	var got string
	clipboardWrite = func(s string) error { got = s; return nil }
	osc52Write = func(string) bool {
		t.Fatal("OSC 52 must not fire while the clipboard works")
		return false
	}
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.append("plain \x1b[31mred\x1b[0m end")
	m.append("second line")
	// The transcript also carries the welcome banner, so assert on the
	// appended tail rather than the whole payload.
	slashCmd := m.runSlash("/copy", "", slashRow{})
	if !strings.HasSuffix(got, "plain red end\nsecond line") || strings.ContainsRune(got, '\x1b') {
		t.Fatalf("/copy must strip ANSI and keep the appended lines, got %q", got)
	}
	if !strings.Contains(m.toast, "Copied transcript") {
		t.Fatalf("/copy must toast receipt, got %q", m.toast)
	}
	// The toast must carry its dismiss tick — a /copy toast that never
	// clears would stick on screen for good.
	if slashCmd == nil {
		t.Fatal("/copy must return the toast dismiss command")
	}
	gotTick := false
	for _, msg := range execCmdChain(t, slashCmd) {
		if _, ok := msg.(toastTickMsg); ok {
			gotTick = true
		}
	}
	if !gotTick {
		t.Fatal("/copy's command chain must contain the toast dismiss tick")
	}
	// A line number copies exactly that visible line (1-based; the last
	// appended line here).
	got = ""
	m.runSlash("/copy", fmt.Sprintf("%d", len(m.lines)), slashRow{})
	if got != "second line" {
		t.Fatalf("/copy <last> must copy only the last line, got %q", got)
	}
	// Clipboard broken: OSC 52 fallback ships the same plain text.
	clipboardWrite = func(string) error { return errors.New("no helper") }
	var oscGot string
	osc52Write = func(s string) bool { oscGot = s; return true }
	m.runSlash("/copy", "", slashRow{})
	if !strings.HasSuffix(oscGot, "plain red end\nsecond line") || strings.ContainsRune(oscGot, '\x1b') {
		t.Fatalf("/copy fallback must ship plain text via OSC 52, got %q", oscGot)
	}
	// Out-of-range: appended error, clipboard untouched. Runs last —
	// its transcript notice would otherwise ride into later payloads.
	clipboardWrite = func(string) error {
		t.Fatal("out-of-range /copy must not touch the clipboard")
		return nil
	}
	osc52Write = func(string) bool {
		t.Fatal("out-of-range /copy must not fall back to OSC 52")
		return false
	}
	before := len(m.lines)
	m.runSlash("/copy", "9999", slashRow{})
	added := strings.Join(m.lines[before:], "\n")
	if !strings.Contains(added, "the transcript has") {
		t.Fatalf("out-of-range /copy must explain itself in the transcript, got %q", added)
	}
}

// TestCopyExpandsPasteTokens pins §2.21's copy/display split: the
// transcript *displays* the collapsed token, but every copy path —
// drag-select and /copy — carries the real stored body. Bodies are
// keyed to the echo's physical line, so a second, shorter paste in a
// later turn (renumbered to #1 again) can never cross-wire.
func TestCopyExpandsPasteTokens(t *testing.T) {
	old := clipboardWrite
	defer func() { clipboardWrite = old }()
	old52 := osc52Write
	defer func() { osc52Write = old52 }()
	var got string
	clipboardWrite = func(s string) error { got = s; return nil }
	osc52Write = func(string) bool { return false }
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	// First turn: a large paste collapses to token #1, then submits.
	body1 := strings.Repeat("alpha line\n", 6)
	m.insertPaste(body1)
	m.ta.InsertString(" first")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m1 := nm.(Model)
	// Second turn: another large paste renumbers to #1 again — the
	// first body must stay bound to the first echo's line.
	m1.running = false // the first submit left a turn running (see TestTurnBoundaryPadding)
	body2 := strings.Repeat("gamma line\n", 6)
	m1.insertPaste(body2)
	m1.ta.InsertString(" second")
	nm2, _ := m1.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := nm2.(Model)
	// Display keeps the collapsed form: both echoes show token #1.
	found := 0
	for _, ln := range m2.lines {
		if strings.Contains(stripANSI(ln), "[Pasted text #1 +6 lines]") {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("both echoes must display the collapsed token, found %d", found)
	}
	m2.runSlash("/copy", "", slashRow{})
	for _, want := range []string{"alpha", body2[:10], " first", " second"} {
		if !strings.Contains(got, strings.TrimRight(want, "\n")) {
			t.Fatalf("/copy must expand paste bodies, missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[Pasted text #") {
		t.Fatalf("copy must never leak token text:\n%s", got)
	}
}

// TestDragSelectEdgeAutoscroll pins the multi-screenful drag: pressing
// at the top edge with the viewport scrolled up, then dragging to the
// bottom edge, autoscrolls and extends the head to the transcript tail
// — a selection is transcript-absolute, not confined to one screen.
func TestDragSelectEdgeAutoscroll(t *testing.T) {
	old := clipboardWrite
	defer func() { clipboardWrite = old }()
	old52 := osc52Write
	defer func() { osc52Write = old52 }()
	clipboardWrite = func(string) error { return nil }
	osc52Write = func(string) bool { return true }
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	after := nm.(Model)
	// Fill well past one screenful.
	for i := 0; i < 60; i++ {
		after.append(fmt.Sprintf("row %02d", i))
	}
	total := len(after.lines)
	// Scroll up 3, press at the top edge, drag to the bottom edge.
	for i := 0; i < 3; i++ {
		nm, _ = after.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
		after = nm.(Model)
	}
	y0 := after.vp.YOffset
	if y0 == 0 {
		t.Fatal("test setup: viewport should have scrolled up")
	}
	nm, _ = after.Update(tea.MouseMsg{X: 5, Y: 0, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	press := nm.(Model)
	if !press.selActive || press.selAnchor != y0 {
		t.Fatalf("press at top edge must anchor at YOffset %d, got %+v", y0, press)
	}
	ext := press
	for i := 0; i < total; i++ {
		nm, _ = ext.Update(tea.MouseMsg{X: 5, Y: ext.vp.Height - 1, Action: tea.MouseActionMotion})
		ext = nm.(Model)
		if ext.selHead == total-1 {
			break
		}
	}
	if ext.selHead != total-1 {
		t.Fatalf("edge drag must autoscroll the head to the tail %d, got %d", total-1, ext.selHead)
	}
	if !ext.selMoved {
		t.Fatal("edge drag must register as a real selection")
	}
}

func TestViewHasNoTrailingPadding(t *testing.T) {
	// The viewport pads short lines to full width with spaces — those
	// would ride into terminal drag-select copies. The render boundary
	// strips them: no rendered line may end in whitespace (empty lines
	// are truly empty). Box borders and padded status segments end in
	// visible glyphs, never spaces.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	after := nm.(Model)
	after.append("hi")
	after.append("  indented")
	after.append("")
	for i, ln := range strings.Split(after.View(), "\n") {
		plain := stripANSI(ln)
		if plain == "" {
			continue
		}
		if strings.HasSuffix(plain, " ") || strings.HasSuffix(plain, "\t") {
			t.Fatalf("line %d carries trailing padding: %q", i, plain)
		}
	}
}

func TestDoubleEscCancelsRunningTurn(t *testing.T) {
	// First Esc at the tail arms (hint toast, turn untouched); the
	// second inside the window cancels via the context.
	m := New(newTestLoop(), mode.Build, t.TempDir(), "ollama/m", 32000)
	m.running = true
	cancelled := false
	ctx, cancel := context.WithCancel(context.Background())
	_ = ctx
	m.cancel = func() { cancelled = true; cancel() }
	m.vp.GotoBottom()
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	after := nm.(Model)
	if cancelled {
		t.Fatal("first Esc must only arm, never cancel")
	}
	if !strings.Contains(after.toast, "Esc again") {
		t.Fatalf("first Esc must hint the second, got %q", after.toast)
	}
	nm2, _ := after.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !cancelled {
		t.Fatal("second Esc inside the window must cancel the turn")
	}
	_ = nm2
	// Stale arm expires: an Esc from long ago re-arms instead of firing.
	m2 := New(newTestLoop(), mode.Build, t.TempDir(), "ollama/m", 32000)
	m2.running = true
	fired := false
	_, cancel2 := context.WithCancel(context.Background())
	m2.cancel = func() { fired = true; cancel2() }
	m2.vp.GotoBottom()
	m2.escArmedAt = time.Now().Add(-time.Hour)
	nm3, _ := m2.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if fired {
		t.Fatal("stale arm must expire back to a hint")
	}
	if !strings.Contains(nm3.(Model).toast, "Esc again") {
		t.Fatal("stale arm must re-arm with a hint")
	}
}

func TestEscScrolledUpReturnsToTail(t *testing.T) {
	// Reading history keeps priority: Esc goes home first, arms nothing.
	m := New(newTestLoop(), mode.Build, t.TempDir(), "ollama/m", 32000)
	for i := 0; i < 50; i++ {
		m.append(strings.Repeat("l", 10))
	}
	m.running = true
	fired := false
	_, cancel := context.WithCancel(context.Background())
	m.cancel = func() { fired = true; cancel() }
	m.stick = false
	m.vp.ScrollUp(5)
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	after := nm.(Model)
	if !after.stick || fired || !after.escArmedAt.IsZero() {
		t.Fatal("scrolled-up Esc must return to tail without arming")
	}
}

func TestCtrlCNoLongerCancels(t *testing.T) {
	// Running: guidance toast, turn untouched. Idle: quit.
	m := New(newTestLoop(), mode.Build, t.TempDir(), "ollama/m", 32000)
	m.running = true
	fired := false
	_, cancel := context.WithCancel(context.Background())
	m.cancel = func() { fired = true; cancel() }
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	after := nm.(Model)
	if fired || !after.running {
		t.Fatal("running Ctrl+C must not cancel anymore")
	}
	if !strings.Contains(after.toast, "Esc twice") {
		t.Fatalf("running Ctrl+C must point at double-Esc, got %q", after.toast)
	}
	m2 := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	_, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("idle Ctrl+C must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("idle Ctrl+C must return tea.Quit")
	}
}

func TestQuitCommand(t *testing.T) {
	// /quit is in the palette and exits, stopping a live turn first.
	found := false
	for _, r := range filterSlash("/") {
		if strings.HasPrefix(r.cmd, "/quit") {
			found = true
		}
	}
	if !found {
		t.Fatal("/quit must be in the palette")
	}
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	cmd := m.runSlash("/quit", "", slashRow{})
	if cmd == nil {
		t.Fatal("/quit must return a command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("/quit must quit")
	}
	m2 := New(newTestLoop(), mode.Build, t.TempDir(), "ollama/m", 32000)
	m2.running = true
	fired := false
	_, cancel := context.WithCancel(context.Background())
	m2.cancel = func() { fired = true; cancel() }
	cmd2 := m2.runSlash("/quit", "", slashRow{})
	if _, ok := cmd2().(tea.QuitMsg); !ok {
		t.Fatal("/quit while running must still quit")
	}
	if !fired {
		t.Fatal("/quit must stop the live turn first")
	}
}

func TestArrowsScrollWhenUnpinned(t *testing.T) {
	// Reading history owns ↑↓ even with prompt history present: wheel
	// motion arriving as arrow keys (tmux alternate-scroll et al.) must
	// scroll, never cycle prompts or touch the box. Native cell-motion
	// tracking (tea.WithMouseCellMotion) keeps wheel ticks as MouseMsg,
	// so this path matters for arrow-only stacks.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.pushHistory("cmd1")
	m.pushHistory("cmd2")
	for i := 0; i < 50; i++ {
		m.append(strings.Repeat("l", 10))
	}
	m.ta.SetValue("draft")
	m.stick = false
	m.vp.ScrollUp(5)
	y0 := m.vp.YOffset
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	after := nm.(Model)
	if after.ta.Value() != "draft" {
		t.Fatalf("scrolled-up Up must not recall, box=%q", after.ta.Value())
	}
	if after.vp.YOffset != y0-1 {
		t.Fatalf("scrolled-up Up must scroll one row, off=%d was %d", after.vp.YOffset, y0)
	}
	if after.histIdx != len(after.hist) {
		t.Fatal("scrolling must not move the history position")
	}
}

func TestUpRecallsFromFirstRowOnly(t *testing.T) {
	// Multiline draft, cursor on row 0: Up recalls. Cursor below row 0:
	// Up edits (moves up a row), box untouched.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.pushHistory("cmd1")
	m.ta.SetValue("aaa\nbbb")
	m.ta.CursorUp() // row 1 → row 0
	if m.ta.Line() != 0 {
		t.Fatalf("setup: cursor must sit on row 0, got %d", m.ta.Line())
	}
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := nm.(Model).ta.Value(); got != "cmd1" {
		t.Fatalf("row-0 Up must recall, box=%q", got)
	}
	m2 := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m2.pushHistory("cmd1")
	m2.ta.SetValue("aaa\nbbb") // cursor stays on row 1
	nm2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyUp})
	after2 := nm2.(Model)
	if after2.ta.Value() != "aaa\nbbb" {
		t.Fatalf("inner-row Up must edit, not recall: box=%q", after2.ta.Value())
	}
	if after2.ta.Line() != 0 {
		t.Fatalf("inner-row Up must move up one row, row=%d", after2.ta.Line())
	}
	if after2.histIdx != len(after2.hist) {
		t.Fatal("editing must not move the history position")
	}
}

func TestSubmitSnapsToBottom(t *testing.T) {
	// Submitting re-pins the viewport: the turn just started is what
	// the user will want to watch.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	for i := 0; i < 50; i++ {
		m.append(strings.Repeat("l", 10))
	}
	m.stick = false
	m.vp.ScrollUp(5)
	m.ta.SetValue("hi")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after := nm.(Model)
	if !after.stick || !after.vp.AtBottom() {
		t.Fatal("submit must re-pin to the live tail")
	}
}

func TestScrollModeWheelScrollsFromTail(t *testing.T) {
	// TILDE_ARROWS=scroll: empty-box Up scrolls the transcript (the
	// wheel-as-arrows case) instead of recalling; the ring stays alive
	// for non-empty Up and navigating Down.
	t.Setenv("TILDE_ARROWS", "scroll")
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.pushHistory("cmd1")
	m.pushHistory("cmd2")
	for i := 0; i < 50; i++ {
		m.append(strings.Repeat("l", 10))
	}
	m.ta.SetValue("")
	y0 := m.vp.YOffset
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	after := nm.(Model)
	if after.ta.Value() != "" {
		t.Fatalf("scroll-mode empty Up must not recall, box=%q", after.ta.Value())
	}
	if after.vp.YOffset != y0-1 {
		t.Fatalf("scroll-mode empty Up must scroll, off=%d was %d", after.vp.YOffset, y0)
	}
	if after.histIdx != len(after.hist) {
		t.Fatal("scrolling must not move the history position")
	}
	// Non-empty Up still recalls (draft completion needs no new keys).
	after.vp.GotoBottom()
	after.stick = true
	after.ta.SetValue("par")
	nm2, _ := after.Update(tea.KeyMsg{Type: tea.KeyUp})
	after2 := nm2.(Model)
	if after2.ta.Value() != "cmd2" {
		t.Fatalf("scroll-mode nonempty Up must recall, box=%q", after2.ta.Value())
	}
	// Navigating Down still walks back to the stashed draft.
	nm3, _ := after2.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := nm3.(Model).ta.Value(); got != "par" {
		t.Fatalf("scroll-mode Down must walk to the draft, box=%q", got)
	}
}

func TestScrollModeInvalidDefaultsHistory(t *testing.T) {
	// Unparseable values fail open to the shell contract.
	t.Setenv("TILDE_ARROWS", "bogus")
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.pushHistory("cmd1")
	m.ta.SetValue("")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := nm.(Model).ta.Value(); got != "cmd1" {
		t.Fatalf("invalid mode must default to recall, box=%q", got)
	}
}
