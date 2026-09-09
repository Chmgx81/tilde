package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
)

// tildeMarkdownStyle maps Glamour's elements onto the spec §1.1 tokens:
// prose in fg, strong/headings bold fg, code muted — and crucially no
// background fills anywhere (spec §2.11's no-fill rule applies to prose
// code blocks too, not just diffs).
// tildeChroma maps syntax tokens onto the transcript palette (review
// feedback 2026-09-08: palette-synced highlighting, never raw
// high-contrast ANSI fallbacks). Roles follow the rest of the UI:
// strings green like additions (new data), numbers amber like
// attention, errors red, comments dim, keywords bold fg. Everything
// else stays in fg/muted so the framework never outshouts the code.
// Background is deliberately zero — code blocks stay transparent (no
// full-width tinted slabs breaking scrollback momentum). Glamour
// resolves the lexer from the fence info string, so .py/.rs/.go/.json
// .yaml highlighting comes free; unknown languages fall back to the
// muted CodeBlock style with content intact (never lost).
func tildeChroma() *ansi.Chroma {
	fgStr, mutStr, dimStr := string(fg), string(fgMuted), string(fgDim)
	greenStr, amberStr, redStr := string(success), string(amber), string(danger)
	bold := true
	prim := func(c string) ansi.StylePrimitive { return ansi.StylePrimitive{Color: &c} }
	primBold := func(c string) ansi.StylePrimitive { return ansi.StylePrimitive{Color: &c, Bold: &bold} }
	return &ansi.Chroma{
		Text:                prim(fgStr),
		Error:               prim(redStr),
		Comment:             prim(dimStr),
		CommentPreproc:      prim(dimStr),
		Keyword:             primBold(fgStr),
		KeywordReserved:     primBold(fgStr),
		KeywordNamespace:    primBold(fgStr),
		KeywordType:         primBold(fgStr),
		Operator:            prim(fgStr),
		Punctuation:         prim(mutStr),
		Name:                prim(fgStr),
		NameBuiltin:         prim(fgStr),
		NameTag:             prim(fgStr),
		NameAttribute:       prim(mutStr),
		NameClass:           primBold(fgStr),
		NameConstant:        prim(fgStr),
		NameDecorator:       prim(mutStr),
		NameException:       prim(redStr),
		NameFunction:        primBold(fgStr),
		NameOther:           prim(fgStr),
		Literal:             prim(fgStr),
		LiteralNumber:       prim(amberStr),
		LiteralDate:         prim(amberStr),
		LiteralString:       prim(greenStr),
		LiteralStringEscape: prim(greenStr),
		GenericDeleted:      prim(redStr),
		GenericEmph:         prim(fgStr),
		GenericInserted:     prim(greenStr),
		GenericStrong:       primBold(fgStr),
		GenericSubheading:   primBold(fgStr),
	}
}

func tildeMarkdownStyle() ansi.StyleConfig {
	fgStr, mutStr := string(fg), string(fgMuted)
	bold := true
	return ansi.StyleConfig{
		Document: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &fgStr}},
		Text:     ansi.StylePrimitive{Color: &fgStr},
		Strong:   ansi.StylePrimitive{Color: &fgStr, Bold: &bold},
		Emph:     ansi.StylePrimitive{Color: &fgStr},
		H1:       ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &fgStr, Bold: &bold}},
		H2:       ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &fgStr, Bold: &bold}},
		H3:       ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &fgStr, Bold: &bold}},
		Code:     ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &mutStr}},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &mutStr}},
			Chroma:     tildeChroma(),
		},
		Link: ansi.StylePrimitive{Color: &mutStr, Underline: &bold},
		Item: ansi.StylePrimitive{Color: &fgStr},
	}
}

// newMarkdownRenderer builds a renderer for width, or nil on failure.
// Callers fall back to raw text — styling must never eat content.
func newMarkdownRenderer(width int) *glamour.TermRenderer {
	if width <= 0 {
		width = 78
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(tildeMarkdownStyle()),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil
	}
	return r
}

// trailPad matches a line-ending run of padding spaces and SGR sequences
// (Glamour pads wrapped lines to the full wrap width with styled spaces).
var trailPad = regexp.MustCompile(`(?:\x1b\[[0-9;]*m| )+$`)

// trimLinePad strips Glamour's end-of-line padding: trailing whitespace
// bloats scrollback, breaks copy-paste, and defeats width checks. A
// closing reset is preserved when the stripped run carried one, so color
// never bleeds into the next line; whitespace-only lines collapse empty.
func trimLinePad(ln string) string {
	loc := trailPad.FindStringIndex(ln)
	if loc == nil {
		return ln
	}
	tail, rest := ln[loc[0]:], ln[:loc[0]]
	if rest == "" {
		return ""
	}
	if strings.Contains(tail, "\x1b[0") {
		rest += "\x1b[0m"
	}
	return rest
}

// renderMarkdownText is the one-shot variant for paths without a Model
// (session resume rebuilds scrollback outside any live model).
func renderMarkdownText(text string, width int) string {
	text = stripANSI(text)
	r := newMarkdownRenderer(width)
	if r == nil {
		return text
	}
	out, err := r.Render(text)
	if err != nil {
		return text
	}
	return trimRendered(out)
}

// renderMarkdown renders one assistant message through Glamour (spec
// stack: Bubble Tea + Lip Gloss + Glamour). Output is pre-wrapped to
// width so the transcript's wrapLine passes it through untouched.
// Any render failure returns the raw text — styling must never eat
// content.
func (m *Model) renderMarkdown(text string, width int) string {
	text = stripANSI(text)
	r := m.mdRenderer(width)
	if r == nil {
		return text
	}
	out, err := r.Render(text)
	if err != nil {
		return text
	}
	return trimRendered(out)
}

// trimRendered trims the newlines Glamour appends, the per-line padding
// it pads wrapped lines with, and the blank padding lines it emits around
// blocks (a leading/trailing "" element would otherwise append as a
// phantom blank row in the transcript — the loose leading under plain
// replies). Internal single spacing is preserved.
func trimRendered(out string) string {
	lines := strings.Split(strings.Trim(out, "\n"), "\n")
	for i, ln := range lines {
		lines[i] = trimLinePad(ln)
	}
	for len(lines) > 0 && stripANSI(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && stripANSI(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	// Normalize paragraph spacing: runs of 2+ blank lines collapse to
	// one — a triple newline reads as a hole in the transcript, not as
	// emphasis. Single blank lines (real paragraph breaks) pass through.
	var kept []string
	blanks := 0
	for _, ln := range lines {
		if stripANSI(ln) == "" {
			blanks++
			if blanks > 1 {
				continue
			}
		} else {
			blanks = 0
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}

// mdRenderer returns the cached Glamour renderer for width, rebuilding
// when the viewport width changes. Nil only if Glamour itself fails to
// construct (then callers fall back to raw text — content is never lost).
func (m *Model) mdRenderer(width int) *glamour.TermRenderer {
	if width <= 0 {
		width = 78
	}
	if m.mdR != nil && m.mdW == width {
		return m.mdR
	}
	r := newMarkdownRenderer(width)
	if r == nil {
		return nil
	}
	m.mdR, m.mdW = r, width
	return r
}
