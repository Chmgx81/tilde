package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// wrapLine soft-wraps one already-styled transcript line to width w
// (ANSI sequences are carried across breaks; visible width is what
// counts). w <= 0 or short lines return unchanged. Wrapping here keeps
// the viewport's scroll geometry in sync with what the terminal shows —
// without it, the terminal's own soft-wrap desynchronizes scrolling.
func wrapLine(s string, w int) string {
	if w <= 0 || ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Wrap(s, w, "")
}

// maxToolResultLines bounds how many lines of a multi-line tool result
// land in the transcript; the session log always keeps everything, and
// the notice says so. Model-visible context is unaffected — this is
// presentation only.
const maxToolResultLines = 400

// maxThinkingLines bounds a reasoning trace in the transcript; the
// session log always keeps the full text for audit, and the notice says
// so. Model-visible context is unaffected — this is presentation only.
const maxThinkingLines = 60

// renderThinking styles a model reasoning trace (spec §2.10): dim
// throughout, ◇ on the first line, plain 2-space indent after — the
// same quiet-gutter language as grouped children. Blank-run collapse
// matches trimRendered (one blank max); truncation is announced, never
// silent. Callers append through m.append, so width wrapping applies.
func renderThinking(text string) string {
	dim := lipgloss.NewStyle().Foreground(fgDim)
	var lines []string
	blanks := 0
	for _, ln := range strings.Split(strings.Trim(text, "\n"), "\n") {
		if strings.TrimSpace(ln) == "" {
			blanks++
			if blanks > 1 {
				continue
			}
			lines = append(lines, "")
			continue
		}
		blanks = 0
		lines = append(lines, ln)
	}
	truncated := false
	if len(lines) > maxThinkingLines {
		lines = lines[:maxThinkingLines]
		truncated = true
	}
	var out []string
	for i, ln := range lines {
		if ln == "" {
			out = append(out, "")
			continue
		}
		if i == 0 {
			out = append(out, dim.Render("◇ "+ln))
			continue
		}
		out = append(out, dim.Render("  "+ln))
	}
	if truncated {
		out = append(out, dim.Render("  … thinking truncated in transcript — the session log keeps the full text."))
	}
	return strings.Join(out, "\n")
}

// renderToolResult styles a tool result (spec §2.10): the ⎿ line for the
// one-line case, or a bounded, styled block for multi-line output. Diff
// runs inside the output get full diff highlighting; everything else is
// muted. Truncation is announced, never silent.
func renderToolResult(text string) string {
	body := strings.TrimRight(text, "\n")
	if body == "" {
		return lipgloss.NewStyle().Foreground(fgDim).Render("  ⎿ (no output)")
	}
	lines := strings.Split(body, "\n")
	if len(lines) == 1 && !isDiffLine(body) {
		return lipgloss.NewStyle().Foreground(fgDim).Render("  ⎿ ") +
			lipgloss.NewStyle().Foreground(fgMuted).Render(body)
	}
	truncated := false
	if len(lines) > maxToolResultLines {
		lines = lines[:maxToolResultLines]
		truncated = true
	}
	var out []string
	i := 0
	for i < len(lines) {
		if isDiffLine(lines[i]) {
			j := i
			for j < len(lines) && isDiffLine(lines[j]) {
				j++
			}
			out = append(out, renderDiffBlock(lines[i:j])...)
			i = j
			continue
		}
		out = append(out, lipgloss.NewStyle().Foreground(fgMuted).Render("  ⎿ "+lines[i]))
		i++
	}
	if truncated {
		out = append(out, lipgloss.NewStyle().Foreground(fgDim).Render(
			"  ⎿ … truncated in transcript — the session log keeps the full output."))
	}
	return strings.Join(out, "\n")
}

// isDiffLine reports whether ln belongs to a unified-diff run: hunk
// headers, +/-/space body lines, or the ---/+++ file pair.
func isDiffLine(ln string) bool {
	switch {
	case strings.HasPrefix(ln, "@@"),
		strings.HasPrefix(ln, "+++"), strings.HasPrefix(ln, "---"),
		strings.HasPrefix(ln, "diff --git "),
		strings.HasPrefix(ln, "index "),
		ln == " ",
		strings.HasPrefix(ln, "+"), strings.HasPrefix(ln, "-"):
		return true
	}
	return false
}

// renderDiffBlock styles a unified-diff run (spec §2.11): bold fg path
// header with a thin rule beneath, hunk headers muted, additions green /
// removals red as text (never background fill), context dim.
func renderDiffBlock(lines []string) []string {
	green := lipgloss.NewStyle().Foreground(success)
	red := lipgloss.NewStyle().Foreground(danger)
	mut := lipgloss.NewStyle().Foreground(fgMuted)
	dim := lipgloss.NewStyle().Foreground(fgDim)
	bold := lipgloss.NewStyle().Foreground(fg).Bold(true)
	out := make([]string, 0, len(lines)+4)
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "diff --git "):
			path := ln
			if i := strings.LastIndex(ln, " b/"); i >= 0 {
				path = ln[i+3:]
			}
			out = append(out, "  "+bold.Render(path), "  "+dim.Render("─────────────────────"))
		case strings.HasPrefix(ln, "index "), strings.HasPrefix(ln, "--- "),
			strings.HasPrefix(ln, "+++ "), strings.HasPrefix(ln, "@@"):
			out = append(out, "  "+mut.Render(ln))
		case strings.HasPrefix(ln, "-"):
			out = append(out, "  "+red.Render(ln))
		case strings.HasPrefix(ln, "+"):
			out = append(out, "  "+green.Render(ln))
		default:
			out = append(out, "  "+dim.Render(strings.TrimPrefix(ln, " ")))
		}
	}
	return out
}
