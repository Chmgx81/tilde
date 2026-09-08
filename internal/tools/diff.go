package tools

import (
	"fmt"
	"strings"
)

// Diff generation for edit/write results (spec §2.11, TUI renders in
// internal/tui/diff.go). Generation lives here because only the tool
// holds both file states; the payload is plain text so the model,
// the session log, and headless JSON all read the same diff the
// transcript renders — one source, never a display-only fork.

// diffContext is the context padding around a changed span; diffCap
// bounds the whole hunk body and newFileCap bounds a new-file body, so
// a giant change can't flood the transcript (the TUI's 400-line cap
// backstops both).
const diffContext = 3
const diffCap = 100
const newFileCap = 200

// SpanDiff builds an edit-result payload: a `+N -M` stat line (line
// counts, plus any trailing note), the ---/+++ pair, one @@ hunk with
// context, then the body. before/after are full file line slices; the
// changed span is [start, start+oldCount) in before, replaced by
// newCount lines at the same point in after. Context comes from
// before (identical in after by construction — a single replacement).
// lineSplit drops one trailing empty element so a trailing newline
// doesn't invent a phantom line in the counts.
func SpanDiff(path string, before, after []string, start, oldCount, newCount int, note string) string {
	stat := fmt.Sprintf("+%d -%d%s", newCount, oldCount, note)
	oldStart, newStart := start+1, start+1
	cStart := start - diffContext
	if cStart < 0 {
		cStart = 0
	}
	cEnd := start + oldCount + diffContext
	if cEnd > len(before) {
		cEnd = len(before)
	}
	var b strings.Builder
	b.WriteString(stat)
	b.WriteByte('\n')
	b.WriteString("--- ")
	b.WriteString(path)
	b.WriteByte('\n')
	b.WriteString("+++ ")
	b.WriteString(path)
	b.WriteByte('\n')
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
	body := 0
	flush := func(prefix, line string) bool {
		if body >= diffCap {
			return false
		}
		b.WriteString(prefix)
		b.WriteString(line)
		b.WriteByte('\n')
		body++
		return true
	}
	truncated := false
	// Leading context.
	for i := cStart; i < start; i++ {
		if !flush(" ", before[i]) {
			truncated = true
			break
		}
	}
	// Removed block.
	if !truncated {
		for i := 0; i < oldCount; i++ {
			if !flush("-", before[start+i]) {
				truncated = true
				break
			}
		}
	}
	// Added block.
	if !truncated {
		afterStart := start
		for i := 0; i < newCount; i++ {
			if !flush("+", after[afterStart+i]) {
				truncated = true
				break
			}
		}
	}
	// Trailing context.
	if !truncated {
		for i := start + oldCount; i < cEnd; i++ {
			if !flush(" ", before[i]) {
				truncated = true
				break
			}
		}
	}
	if truncated {
		b.WriteString("[diff truncated in transcript: review the full change by reading the file]\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// lineSplit splits content into lines, dropping one trailing empty
// element so "a\n" counts one line, not two.
func lineSplit(s string) []string {
	lines := strings.Split(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// spanStart returns the 0-based line index where span begins in
// content (-1 when absent — the caller already validated presence,
// so this is a fallback, never a decision).
func spanStart(content, span string) int {
	i := strings.Index(content, span)
	if i < 0 {
		return -1
	}
	return strings.Count(content[:i], "\n")
}
