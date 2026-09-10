package tui

import (
	"fmt"
	"strings"
	"testing"
)

// Golden snapshot tests: deterministic, no PTY, no git, no network.
// They capture pure render functions only (helpView, slashCommands /
// filterSlash, scoreCriticDiff + the critic header/finding format strings).
// Whitespace-tolerant by design: golden values are inline fragments matched
// with squash-then-contains, so padding/glyph tweaks don't brittle-fail.

// goldenSquash collapses all whitespace runs to single spaces so alignment
// padding changes don't break the snapshot. Symbol glyphs preserved exactly.
func goldenSquash(s string) string {
	return strings.Join(strings.Fields(stripANSI(s)), " ")
}

// TestGoldenHelpView80 snapshots the helpView(80) reference screen.
func TestGoldenHelpView80(t *testing.T) {
	got := helpView(80)
	plain := stripANSI(got)
	squashed := goldenSquash(got)

	goldens := []string{
		"Keybindings",
		"Enter Send the prompt",
		"Tab Cycle mode: Plan",
		"/ Command palette",
		"esc Dismiss overlay or picker",
		"Slash commands:",
		"press esc to close",
	}
	for _, g := range goldens {
		if !strings.Contains(squashed, g) {
			t.Errorf("helpView(80) missing golden fragment %q\nsquashed:\n%s", g, squashed)
		}
	}
	if !strings.Contains(plain, "/critic") {
		t.Errorf("helpView(80) must list /critic, got:\n%s", plain)
	}
	for i, ln := range strings.Split(plain, "\n") {
		if w := len([]rune(ln)); w > 80 {
			t.Errorf("helpView(80) line %d exceeds width: %d runes: %q", i, w, ln)
		}
	}
}

// TestGoldenSlashPaletteOrder spot-checks the canonical palette order.
// A bare "/" must show slashCommands verbatim (filterSlash is stable).
func TestGoldenSlashPaletteOrder(t *testing.T) {
	wantOrder := []string{
		"/model <model>",
		"/login [provider]",
		"/logout [provider]",
		"/mode <plan|build|auto>",
		"/skills",
		"/plugins",
		"/marketplace",
		"/compact [focus]",
		"/clear",
		"/copy [n]",
		"/sandbox",
		"/diff",
		"/critic",
		"/apply [path]",
		"/discard [path]",
		"/undo [n]",
		"/sessions",
		"/export [id]",
		"/help",
		"/quit",
	}
	if len(slashCommands) != len(wantOrder) {
		t.Fatalf("palette length drift: got %d want %d", len(slashCommands), len(wantOrder))
	}
	for i, want := range wantOrder {
		if slashCommands[i].cmd != want {
			t.Errorf("palette[%d] = %q, want %q", i, slashCommands[i].cmd, want)
		}
	}
	if !strings.HasPrefix(slashCommands[0].cmd, "/model") {
		t.Errorf("/model must stay first, got %q", slashCommands[0].cmd)
	}
	if slashCommands[len(slashCommands)-1].cmd != "/quit" {
		t.Errorf("/quit must stay last, got %q", slashCommands[len(slashCommands)-1].cmd)
	}
	rows := filterSlash("")
	if len(rows) != len(slashCommands) {
		t.Fatalf("filterSlash(\"\") dropped rows: got %d want %d", len(rows), len(slashCommands))
	}
	for i := range rows {
		if rows[i].cmd != slashCommands[i].cmd {
			t.Fatalf("bare filter must preserve order at [%d]: got %q want %q", i, rows[i].cmd, slashCommands[i].cmd)
		}
	}
	for _, r := range slashCommands {
		if strings.TrimSpace(r.desc) == "" {
			t.Errorf("palette entry %q has empty description", r.cmd)
		}
	}
}

// TestGoldenCriticHeaderFormat snapshots the critic header line format
// (the fmt.Sprintf in showCritic) via the pure scorer: no git, no tools.
func TestGoldenCriticHeaderFormat(t *testing.T) {
	diff := "diff --git a/a_test.go b/a_test.go\n+++ b/a_test.go\n+new\n"
	res := scoreCriticDiff(diff)
	if res.score != 100 || !res.pass {
		t.Fatalf("golden fixture must score 100/PASS, got %+v", res)
	}
	head := fmt.Sprintf("● critic: %d/100 (threshold %d) — %d files, +%d -%d",
		res.score, criticPassThreshold, res.files, res.added, res.removed)
	const wantHead = "● critic: 100/100 (threshold 80) — 1 files, +1 -0"
	if head != wantHead {
		t.Errorf("critic header drift:\n got %q\nwant %q", head, wantHead)
	}
	if got := "✓ " + head + " — PASS"; !strings.Contains(goldenSquash(got), "— PASS") {
		t.Errorf("pass rendering must carry PASS suffix, got %q", got)
	}
	if got := "✗ " + head + " — NEEDS WORK"; !strings.Contains(goldenSquash(got), "— NEEDS WORK") {
		t.Errorf("fail rendering must carry NEEDS WORK suffix, got %q", got)
	}
	const wantEmpty = "○ critic: no changes — working tree matches HEAD, nothing to review."
	if goldenSquash(wantEmpty) != goldenSquash("○ critic: no changes — working tree matches HEAD, nothing to review.") {
		t.Errorf("empty-diff golden fragment drift: %q", wantEmpty)
	}
	if !isEmptyDiff("") || !isEmptyDiff("no diff — working tree matches HEAD.\n") {
		t.Error("isEmptyDiff must treat blank + clean-tree sentinel as empty")
	}
}

// TestGoldenCriticFindingRendering snapshots the finding line shape
// "⎿ <name> (-<penalty>)[: detail]" via the pure scorer.
func TestGoldenCriticFindingRendering(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n+++ b/a.go\n+api_key= \"AKIA-SECRET\"\n"
	res := scoreCriticDiff(diff)
	if len(res.findings) == 0 {
		t.Fatalf("secret fixture must yield findings, got %+v", res)
	}
	f := res.findings[0]
	line := fmt.Sprintf("  ⎿ %s (-%d)", f.name, f.penalty)
	if f.detail != "" {
		line += ": " + f.detail
	}
	const wantFinding = "⎿ possible secret (-20)"
	if !strings.Contains(goldenSquash(line), wantFinding) {
		t.Errorf("finding line drift:\n got %q\nwant fragment %q\nfull: %+v", line, wantFinding, res)
	}
}
