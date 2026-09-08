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
	// lipgloss Width covers content+padding; the border adds 2, so
	// Width(width-2) lands the box exactly on the viewport width.
	inner := max(width-2, 20)
	title := lipgloss.NewStyle().Foreground(fg).Render("Welcome to tilde.")
	body := lipgloss.NewStyle().Foreground(fgMuted).Render(strings.Join([]string{
		"tilde can read, edit, and delete files, and run shell commands you",
		"approve or that fall inside the active sandbox policy. Use in trusted",
		"environments only. Sandboxed via bubblewrap + network egress denied by",
		"default on Linux. See policies.yaml to review current rules.",
		"",
		// Cloud list derives from the registry (CloudIDs) so it can never
		// go stale when a provider is added — the splash is not the place
		// for a second hardcoded source of truth.
		"Local models run via Ollama. On a machine without a GPU, /login <pro-",
		"vider> arms a cloud key (" + strings.Join(provider.CloudIDs(), ", ") + ") and /model",
		"<provider/model> switches mid-session.",
	}, "\n"))
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(borderIdle).Padding(0, 1).Width(inner).
		Render(title + "\n\n" + body)
	return strings.Split(box, "\n")
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
	out := []string{
		header,
		"",
	}
	out = append(out, splashPanel(width)...)
	left := fmt.Sprintf("  Budget: %s", budgetStr)
	right := fmt.Sprintf("Sandbox: %s", sandbox.StatusLine())
	gap := width - runeLen(left) - runeLen(right)
	if gap < 1 {
		gap = 1
	}
	out = append(out,
		"",
		left+strings.Repeat(" ", gap)+right,
		"",
	)
	return out
}
