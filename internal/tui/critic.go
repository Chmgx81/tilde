package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Critic self-review (spec §2.4: score-gated self-review of the
// working-tree diff). Deterministic offline rubric only — no network,
// no model calls, no new dependencies. Input is the raw git_diff tool
// output (fenced); scoring is a pure function of that text so results
// are reproducible run to run.

// criticPassThreshold is the score at or above which the diff passes.
const criticPassThreshold = 80

// criticMaxFindings bounds the rendered finding list; the transcript
// stays scannable no matter how noisy the diff is.
const criticMaxFindings = 8

// criticFinding is one named deduction with its penalty.
type criticFinding struct {
	name    string
	detail  string
	penalty int
}

// criticResult is the deterministic outcome of scoring a diff.
type criticResult struct {
	score    int
	pass     bool
	files    int
	added    int
	removed  int
	findings []criticFinding
}

// scoreCriticDiff scores fenced or raw git_diff output. Pure and
// deterministic: same input always yields the same score + findings.
func scoreCriticDiff(out string) criticResult {
	body := unfence(out)
	files, added, removed, addedLines := scanDiffLines(body)
	score := 100
	var findings []criticFinding
	deduct := func(name, detail string, penalty int) {
		score -= penalty
		findings = append(findings, criticFinding{name: name, detail: detail, penalty: penalty})
	}

	total := added + removed
	switch {
	case total > 1000:
		deduct("large diff", "over 1000 changed lines — split the change", 20)
	case total > 400:
		deduct("large diff", "over 400 changed lines — consider splitting", 15)
	}
	if files > 8 {
		deduct("many files", "over 8 files touched — widened blast radius", 15)
	} else if files > 5 {
		deduct("many files", "over 5 files touched", 10)
	}

	secrets, debug, long := 0, 0, 0
	var secretEx, debugEx, longEx string
	for _, ln := range addedLines {
		low := strings.ToLower(ln)
		if secretEx == "" && looksSecret(low) {
			secretEx = strings.TrimSpace(ln)
		}
		if debugEx == "" && looksDebugLeftover(low) {
			debugEx = strings.TrimSpace(ln)
		}
		if longEx == "" && len([]rune(ln)) > 120 {
			longEx = strings.TrimSpace(ln)
		}
		if looksSecret(low) {
			secrets++
		}
		if looksDebugLeftover(low) {
			debug++
		}
		if len([]rune(ln)) > 120 {
			long++
		}
	}
	if secrets > 0 {
		deduct("possible secret", truncMiddle(secretEx, 80), 20)
	}
	if debug > 0 {
		deduct("debug leftover", truncMiddle(debugEx, 80), 10)
	}
	if long > 0 {
		deduct("long lines", "added lines over 120 runes — wrap them", 5)
	}
	if removed > 50 && removed > added*2 {
		deduct("deletion-heavy", "removals dominate additions — verify scope", 5)
	}
	if added > 0 && touchesCode(addedLines, body) && !touchesTests(body) {
		deduct("missing tests", "code changed with no test file in the diff", 10)
	}

	if score < 0 {
		score = 0
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].penalty != findings[j].penalty {
			return findings[i].penalty > findings[j].penalty
		}
		return findings[i].name < findings[j].name
	})
	if len(findings) > criticMaxFindings {
		findings = findings[:criticMaxFindings]
	}
	return criticResult{
		score: score, pass: score >= criticPassThreshold,
		files: files, added: added, removed: removed, findings: findings,
	}
}

// showCritic renders the score-gated self-review of the working-tree
// diff (/critic). It reuses the git_diff tool plumbing like /diff, then
// scores deterministically offline. Human surface only — it never enters
// model context. Output is bounded to criticMaxFindings findings.
func (m *Model) showCritic() {
	if m.loop.Reg == nil {
		m.append("✗ no tool registry attached.")
		return
	}
	t, ok := m.loop.Reg.Get("git_diff")
	if !ok {
		m.append("✗ git_diff tool not registered.")
		return
	}
	out, err := t.Exec(context.Background(), map[string]any{})
	if err != nil {
		m.append("✗ " + firstLine(err.Error()))
		return
	}
	if isEmptyDiff(out) {
		m.append(lipgloss.NewStyle().Foreground(fgMuted).Render("○ critic: no changes — working tree matches HEAD, nothing to review."))
		return
	}
	res := scoreCriticDiff(out)
	head := fmt.Sprintf("● critic: %d/100 (threshold %d) — %d files, +%d -%d",
		res.score, criticPassThreshold, res.files, res.added, res.removed)
	if res.pass {
		m.append(lipgloss.NewStyle().Foreground(success).Render("✓ " + head + " — PASS"))
	} else {
		m.append(lipgloss.NewStyle().Foreground(danger).Render("✗ " + head + " — NEEDS WORK"))
	}
	if len(res.findings) == 0 {
		m.append(lipgloss.NewStyle().Foreground(fgMuted).Render("  ⎿ no findings — clean diff."))
		return
	}
	for _, f := range res.findings {
		line := fmt.Sprintf("  ⎿ %s (-%d)", f.name, f.penalty)
		if f.detail != "" {
			line += ": " + f.detail
		}
		m.append(lipgloss.NewStyle().Foreground(fgMuted).Render(line))
	}
}

// isEmptyDiff reports the fail-loud empty case: git_diff's clean-tree
// sentinel or truly blank output.
func isEmptyDiff(out string) bool {
	t := strings.TrimSpace(unfence(out))
	if t == "" {
		return true
	}
	return strings.Contains(t, "no diff — working tree matches HEAD.")
}

// unfence strips the untrusted-output fence so the scorer sees diff text.
func unfence(s string) string {
	var kept []string
	for _, ln := range strings.Split(s, "\n") {
		if strings.HasPrefix(ln, "--- begin untrusted output") ||
			strings.HasPrefix(ln, "--- end untrusted output") {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}

// scanDiffLines counts files and +/- body lines, collecting added lines
// for heuristic checks. Header lines (---/+++/diff --git/index/@@) and
// the --stat summary never count as content.
func scanDiffLines(body string) (files, added, removed int, addedLines []string) {
	for _, ln := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(ln, "diff --git "):
			files++
		case strings.HasPrefix(ln, "@@"),
			strings.HasPrefix(ln, "--- "),
			strings.HasPrefix(ln, "+++ "),
			strings.HasPrefix(ln, "index "):
			continue
		case strings.HasPrefix(ln, "+") && !strings.HasPrefix(ln, "+++"):
			added++
			addedLines = append(addedLines, ln[1:])
		case strings.HasPrefix(ln, "-") && !strings.HasPrefix(ln, "---"):
			removed++
		}
	}
	return files, added, removed, addedLines
}

func looksSecret(low string) bool {
	for _, sub := range []string{"password=", "passwd=", "secret=", "api_key=", "apikey=", "api-key=", "aws_secret", "bearer ", "private_key", "client_secret"} {
		if strings.Contains(low, sub) {
			return true
		}
	}
	if strings.Contains(low, "token=") || strings.Contains(low, "token:") {
		return true
	}
	return false
}

func looksDebugLeftover(low string) bool {
	for _, sub := range []string{"todo", "fixme", "xxx", "hack:", "console.log", "fmt.println", "println(", "pprint", "breakpoint()", "debugger", "pry", "todo:"} {
		if strings.Contains(low, sub) {
			return true
		}
	}
	return false
}

// touchesCode reports whether the diff body names a source file and
// carries added lines (a heuristic over common extensions).
func touchesCode(addedLines []string, body string) bool {
	if len(addedLines) == 0 || !strings.Contains(body, "diff --git ") {
		return false
	}
	for _, ext := range []string{".go", ".ts", ".js", ".py", ".rs", ".java", ".rb", ".c", ".cpp", ".sh"} {
		if strings.Contains(body, ext) {
			return true
		}
	}
	return false
}

// touchesTests reports whether the diff body names a test file.
func touchesTests(body string) bool {
	low := strings.ToLower(body)
	for _, sub := range []string{"_test.go", ".test.", "_test.py", "test_", "/test/", "__tests__", ".spec."} {
		if strings.Contains(low, sub) {
			return true
		}
	}
	return false
}
