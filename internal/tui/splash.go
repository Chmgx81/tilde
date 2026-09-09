package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"tilde/internal/provider"
	"tilde/internal/sandbox"
)

// splashPanel renders the welcome block as a neutrally-bordered panel
// (RoundedBorder like composer/confirm/handoff, border-idle per §1.1).
// Title is plain fg text, prose is fg-muted — no glyph, no bold (§§1.2/1.4).
func splashPanel(width int) []string {
	// At the hard floor, preserve a truthful, usable status card instead of
	// letting long explanatory prose escape the terminal edge. The normal
	// panel returns as soon as the terminal has enough room for its contract.
	if width < 32 {
		inner := max(width-2, 18)
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(borderIdle).Padding(0, 1).Width(inner).
			Render("Welcome to tilde.\n\nsandbox protected\npress ? for help")
		lines := strings.Split(box, "\n")
		for i := range lines {
			lines[i] = hardCut(lines[i], width)
		}
		return lines
	}
	// lipgloss Width covers content+padding; the border adds 2, so
	// Width(width-2) lands the box exactly on the viewport width.
	inner := max(width-2, 20)
	title := lipgloss.NewStyle().Foreground(fg).Render("Welcome to tilde.")
	// Wrap prose at words rather than splitting a command placeholder in the
	// middle (`<provider>` used to become `pro-` / `vider>`). The card is
	// responsive: the same copy reads naturally at 80 columns and uses the
	// available width on larger terminals instead of leaving a narrow island
	// of text inside a wide border.
	paragraphs := []string{
		"tilde can read, edit, and delete files, and run shell commands you approve or that fall inside the active sandbox policy. Use in trusted environments only. Sandboxed via bubblewrap + network egress denied by default on Linux. See policies.yaml to review current rules.",
		// Cloud list derives from the registry so it cannot go stale when a
		// provider is added.
		"Local models run via Ollama. On a machine without a GPU, /login <provider> arms a cloud key (" + strings.Join(provider.CloudIDs(), ", ") + ") and /model <provider/model> switches mid-session.",
	}
	body := lipgloss.NewStyle().Foreground(fgMuted).Render(wrapSplashParagraphs(paragraphs, max(width-6, 1)))
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(borderIdle).Padding(0, 1).Width(inner).
		Render(title + "\n\n" + body)
	return strings.Split(box, "\n")
}

func wrapSplashParagraphs(paragraphs []string, width int) string {
	if width < 1 {
		width = 1
	}
	var out []string
	for i, paragraph := range paragraphs {
		if i > 0 {
			out = append(out, "")
		}
		words := strings.Fields(paragraph)
		line := ""
		for _, word := range words {
			if line == "" {
				line = word
				continue
			}
			if len([]rune(line))+1+len([]rune(word)) <= width {
				line += " " + word
				continue
			}
			out = append(out, line)
			line = word
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// splashLines renders the welcome screen (§2.1): safety notice as prose
// (read once, as language — not a glyph'd line), then model, budget, and
// sandbox state. It deliberately ends there: the composer placeholder and
// the hint bar are ever-present chrome rendered below the viewport, so
// echoing their wording as transcript lines would show both twice on the
// first frame. Mode is plain text in the mode accent (§2.1 correction
// 2026-09-06 — no ○ glyph: ○ means "pending" per §1.2, and Plan is active).
func splashLines(root, modelName string, budget, width int) []string {
	budgetStr := "32.0k tokens"
	if budget > 0 {
		if budget >= 1000 {
			budgetStr = fmt.Sprintf("%.1fk tokens", float64(budget)/1000)
		} else {
			budgetStr = fmt.Sprintf("%d tokens", budget)
		}
	}
	// Session always starts in Plan; the status bar below the composer
	// already carries Mode and Model on every frame, so the splash keeps
	// only what exists nowhere else: the Budget ceiling and the Sandbox
	// enforcement state (spec §2.1). One line, space-between: Budget docks
	// left, Sandbox docks right, the gap computed from the live width —
	// never a fixed pad (a fixed pad strands Sandbox mid-line on wide
	// screens and overflows narrow ones).
	// Context header: centered across the frame (the one centered line in
	// the UI — a title, not a log event, so the §1.3 gutter rule yields).
	// Too-long paths fall back to the plain left form rather than
	// truncating location information.
	headerText := fmt.Sprintf("%s · tilde %s", root, appVersion)
	header := "  " + headerText
	if w := runeLen(headerText); w < width {
		header = strings.Repeat(" ", (width-w)/2) + headerText
	}
	header = hardCut(header, max(width, 1))
	out := []string{
		header,
		"",
	}
	out = append(out, splashPanel(width)...)
	left := fmt.Sprintf("  Budget: %s", budgetStr)
	right := fmt.Sprintf("Sandbox: %s", sandbox.StatusLine())
	// The sandbox diagnostic can be longer than the available status row
	// (notably when bubblewrap is missing). Keep the row inside the terminal
	// width instead of allowing an exceptional startup state to break layout.
	right = truncANSI(right, max(width-runeLen(left)-1, 1))
	line := left + " " + right
	if width >= 32 {
		gap := width - runeLen(left) - runeLen(right)
		if gap < 1 {
			gap = 1
		}
		line = left + strings.Repeat(" ", gap) + right
	}
	out = append(out,
		"",
		hardCut(line, max(width, 1)),
		"",
	)
	return out
}
