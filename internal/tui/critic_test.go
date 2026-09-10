package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tilde/internal/mode"
)

// Palette must advertise /critic per the spec §2.4 command list.
func TestCriticPaletteEntry(t *testing.T) {
	found := false
	for _, r := range slashCommands {
		if strings.HasPrefix(r.cmd, "/critic") {
			found = true
		}
	}
	if !found {
		t.Fatal("palette missing /critic")
	}
	rows := filterSlash("/critic")
	if len(rows) == 0 || !strings.HasPrefix(rows[0].cmd, "/critic") {
		t.Fatalf("/critic must be top match for its filter, got %v", rows)
	}
	if !strings.Contains(helpView(200), "/critic") {
		t.Fatal("help view must list /critic")
	}
}

// Deterministic: same input always yields the same score + findings.
func TestCriticScoringDeterministic(t *testing.T) {
	diff := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new // TODO: cleanup\n"
	a, b := scoreCriticDiff(diff), scoreCriticDiff(diff)
	if a.score != b.score || len(a.findings) != len(b.findings) {
		t.Fatalf("nondeterministic: %+v vs %+v", a, b)
	}
	if a.score >= 100 {
		t.Fatalf("TODO leftover must deduct, got %d", a.score)
	}
	named := false
	for _, f := range a.findings {
		if f.name == "debug leftover" {
			named = true
		}
	}
	if !named {
		t.Fatalf("expected named debug-leftover finding, got %+v", a.findings)
	}
}

// Numeric score + threshold + named findings on a secret-bearing diff.
func TestCriticSecretScoresBelowThreshold(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n+++ b/a.go\n+api_key= \"AKIA-SECRET\"\n"
	res := scoreCriticDiff(diff)
	if res.pass || res.score >= criticPassThreshold {
		t.Fatalf("secret diff must fail the threshold, got %+v", res)
	}
	found := false
	for _, f := range res.findings {
		if f.name == "possible secret" && f.penalty > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected named possible-secret finding, got %+v", res.findings)
	}
}

// A clean small diff passes with no deductions.
func TestCriticCleanDiffPasses(t *testing.T) {
	diff := "diff --git a/a_test.go b/a_test.go\n--- a/a_test.go\n+++ b/a_test.go\n@@ -1 +1 @@\n-old\n+new\n"
	res := scoreCriticDiff(diff)
	if !res.pass || res.score != 100 {
		t.Fatalf("clean test diff must pass at 100, got %+v", res)
	}
	if len(res.findings) != 0 {
		t.Fatalf("clean diff must carry no findings, got %+v", res.findings)
	}
}

// Bounded output: findings never exceed criticMaxFindings.
func TestCriticFindingsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/many.go b/many.go\n")
	for i := 0; i < 600; i++ {
		b.WriteString("+line with a very long tail that keeps going past one hundred and twenty runes to force the long-line rule on every line\n")
	}
	b.WriteString("+password= hunter2\n+TODO: fix me\n")
	res := scoreCriticDiff(b.String())
	if len(res.findings) > criticMaxFindings {
		t.Fatalf("findings must be bounded at %d, got %d", criticMaxFindings, len(res.findings))
	}
}

// Fail-loud empty diff: /critic on a clean tree names the empty state.
func TestCriticCommandEmptyDiff(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-qb", "main")
	git("config", "user.email", "t@t.t")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "d.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "init")

	loop, _, _, _ := newUndoLoop(t, root)
	m := New(loop, mode.Plan, root, "ollama/m", 32000)
	m.runSlash("/critic", "", slashRow{})
	last := stripANSI(strings.Join(m.lines, "\n"))
	if !strings.Contains(last, "nothing to review") {
		t.Fatalf("empty diff must fail loud, got:\n%s", last)
	}
}

// /critic on a dirty tree renders the numeric score + threshold line.
func TestCriticCommandScoresDirtyTree(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-qb", "main")
	git("config", "user.email", "t@t.t")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "d.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "init")
	if err := os.WriteFile(filepath.Join(root, "d.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	loop, _, _, _ := newUndoLoop(t, root)
	m := New(loop, mode.Plan, root, "ollama/m", 32000)
	m.runSlash("/critic", "", slashRow{})
	joined := stripANSI(strings.Join(m.lines, "\n"))
	if !strings.Contains(joined, "critic:") || !strings.Contains(joined, "/100") {
		t.Fatalf("critic must render a numeric score, got:\n%s", joined)
	}
	if !strings.Contains(joined, "threshold") {
		t.Fatalf("critic must name the threshold, got:\n%s", joined)
	}
}
