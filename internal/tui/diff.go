package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Diff + new-file result formatting (spec §2.11). The tools layer
// (internal/tools/diff.go) generates payloads; this file only styles
// them. Payload shapes, all as the FIRST result line:
//
//	edit/overwrite:  +12 -3 [optional note]
//	new file:        +34 (new file: some/path.go)
//
// An edit body is ---/+++ plus one @@ hunk; a new-file body is raw
// content lines. Anything else falls through to the generic renderer
// in wrap.go — this file never sees it.

var (
	diffStatRe    = regexp.MustCompile(`^\+(\d+) -(\d+)(.*)$`)
	newFileStatRe = regexp.MustCompile(`^\+(\d+) \(new file: (.+)\)$`)
	hunkRe        = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
)

// diffRule is the thin rule beneath a diff path header (spec §2.11:
// a rule, never a box — a diff is dense enough already).
const diffRule = "─────────────────────"

// splitDiffStat parses an edit stat line: +adds -dels plus any trailing
// note (e.g. a fuzzy-match warning the model must verify).
func splitDiffStat(line string) (stat string, ok bool) {
	if diffStatRe.MatchString(line) {
		return line, true
	}
	return "", false
}

// splitNewFileStat parses a new-file stat line into count + path.
func splitNewFileStat(line string) (n int, path string, ok bool) {
	m := newFileStatRe.FindStringSubmatch(line)
	if m == nil {
		return 0, "", false
	}
	var count int
	fmt.Sscanf(m[1], "%d", &count)
	return count, m[2], true
}

// renderDiffPayload styles edit-hunk lines: a bold fg path with a thin
// rule (paired from ---/+++), muted @@ headers, and a numbered body —
// 4-column right-aligned gutter, +/- marker, one space, then code, so
// code always starts at the same column for context, additions, and
// removals. Additions are green text, removals red text, context dim;
// never background fill. A dangling --- (no +++) or a hunk with no @@
// header falls back to muted lines rather than disappearing.
func renderDiffPayload(lines []string) []string {
	green := lipgloss.NewStyle().Foreground(success)
	red := lipgloss.NewStyle().Foreground(danger)
	mut := lipgloss.NewStyle().Foreground(fgMuted)
	bold := lipgloss.NewStyle().Foreground(fg).Bold(true)
	dim := lipgloss.NewStyle().Foreground(fgDim)
	out := make([]string, 0, len(lines)+2)
	var pendingPath string
	flushPending := func() {
		if pendingPath != "" {
			out = append(out, "  "+mut.Render("--- "+pendingPath))
			pendingPath = ""
		}
	}
	oldLn, newLn := 0, 0
	numbered := false
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "diff --git "):
			flushPending()
			path := ln
			if i := strings.LastIndex(ln, " b/"); i >= 0 {
				path = ln[i+3:]
			}
			out = append(out, "  "+bold.Render(path), "  "+dim.Render(diffRule))
		case strings.HasPrefix(ln, "--- "):
			flushPending()
			pendingPath = strings.TrimSpace(strings.TrimPrefix(ln, "--- "))
		case strings.HasPrefix(ln, "+++ "):
			path := strings.TrimSpace(strings.TrimPrefix(ln, "+++ "))
			if i := strings.Index(path, "\t"); i >= 0 {
				path = path[:i]
			}
			if pendingPath != "" {
				// Paired ---/+++: one bold header + rule, both
				// markers consumed — the path renders once.
				out = append(out, "  "+bold.Render(path), "  "+dim.Render(diffRule))
				pendingPath = ""
			} else {
				out = append(out, "  "+mut.Render(ln))
			}
		case hunkRe.MatchString(ln):
			flushPending()
			m := hunkRe.FindStringSubmatch(ln)
			fmt.Sscanf(m[1], "%d", &oldLn)
			fmt.Sscanf(m[3], "%d", &newLn)
			numbered = true
			out = append(out, "  "+mut.Render(ln))
		case numbered && strings.HasPrefix(ln, "-") && !strings.HasPrefix(ln, "---"):
			out = append(out, "  "+red.Render(fmt.Sprintf("%4d - %s", oldLn, ln[1:])))
			oldLn++
		case numbered && strings.HasPrefix(ln, "+") && !strings.HasPrefix(ln, "+++"):
			out = append(out, "  "+green.Render(fmt.Sprintf("%4d + %s", newLn, ln[1:])))
			newLn++
		case numbered && strings.HasPrefix(ln, " "):
			// Context renders the NEW-side number: it is the post-edit
			// file the user can jump to, and it keeps continuity with
			// the additions above (changed -/+ pairs already share
			// their position number per the spec example).
			out = append(out, "  "+dim.Render(fmt.Sprintf("%4d   %s", newLn, strings.TrimPrefix(ln, " "))))
			oldLn++
			newLn++
		case strings.HasPrefix(ln, "index "):
			out = append(out, "  "+mut.Render(ln))
		default:
			// Notes, truncation markers, anything unclaimed: a plain
			// ⎿ line — visible, never silently dropped.
			flushPending()
			out = append(out, "  "+mut.Render("⎿ "+ln))
		}
	}
	flushPending()
	return out
}

// renderNewFilePayload styles a new-file body (spec §2.11): a header
// naming the file and its length, the thin rule, then plain fg lines
// with the same gutter column as the edit diff but NO +/- marker —
// every line is unambiguously new, so markers would be noise and green
// would collide with real additions later. Trailing notice lines (the
// truncation marker) render as ⎿ lines, unnumbered.
func renderNewFilePayload(path string, n int, lines []string) []string {
	bold := lipgloss.NewStyle().Foreground(fg).Bold(true)
	dim := lipgloss.NewStyle().Foreground(fgDim)
	mut := lipgloss.NewStyle().Foreground(fgMuted)
	out := make([]string, 0, len(lines)+2)
	out = append(out, "  "+bold.Render(fmt.Sprintf("%s (new file, %d lines)", path, n)),
		"  "+dim.Render(diffRule))
	highlighted := highlightSourceLines(path, lines)
	for i, ln := range highlighted {
		if i >= len(lines) {
			break
		}
		if ln == "" || strings.HasPrefix(ln, "[") {
			// Blank or bracketed notice (the truncation marker):
			// a ⎿ line, never a numbered "line" of the file.
			if strings.TrimSpace(ln) == "" {
				continue
			}
			out = append(out, "  "+mut.Render("⎿ "+ln))
			continue
		}
		gutter := lipgloss.NewStyle().Foreground(fgDim).Render(fmt.Sprintf("%4d   ", i+1))
		out = append(out, "  "+gutter+ln)
	}
	return out
}
