package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"tilde/internal/mode"
	"tilde/internal/tools"
)

// Plan-mode view (spec §2.9): the read-only banner plus the Update-Todos
// block. The banner is the only full-width bordered element that appears
// mid-transcript — it interrupts the scan pattern on purpose, because
// "nothing will change right now" is exactly the reassurance a cautious
// user is scanning for. It renders once per session start and once per
// demotion into Plan, never once per Plan turn (that would be noise).

// planBannerTitle is the one-line mode+descriptor (§1.6: exactly the mode
// name plus one descriptor word, never a paragraph).
const planBannerTitle = "Plan · read-only"

// planBannerBody is the banner's reassurance line, verbatim per the §2.9
// mockup.
const planBannerBody = "tilde is researching. No files will be changed in this mode."

// planBanner builds the full-width banner box at width w (square corners,
// amber border — the read-only/caution token per §1.1). Lines are cut to w
// so a narrow terminal can never push the box past the frame.
func planBanner(w int) []string {
	if w < 20 {
		w = 20
	}
	bSt := lipgloss.NewStyle().Foreground(borderPlan)
	titleSt := lipgloss.NewStyle().Foreground(borderPlan).Bold(true)
	bodySt := lipgloss.NewStyle().Foreground(fg)

	top := bSt.Render("┌ ") + titleSt.Render(planBannerTitle) +
		bSt.Render(" "+strings.Repeat("─", max(w-runeLen(planBannerTitle)-4, 2))+"┐")
	mid := bSt.Render("│ ") + bodySt.Render(planBannerBody) +
		bSt.Render(strings.Repeat(" ", max(w-runeLen(planBannerBody)-3, 0))+"│")
	bot := bSt.Render("└" + strings.Repeat("─", max(w-2, 2)) + "┘")
	out := []string{top, mid, bot}
	for i, ln := range out {
		out[i] = hardCut(ln, w)
	}
	return out
}

// appendPlanBanner adds one banner box to the transcript at the live
// viewport width (78 when no resize has been seen yet, e.g. in tests).
func (m *Model) appendPlanBanner() {
	w := m.vp.Width
	if w <= 0 {
		w = 78
	}
	for _, ln := range planBanner(w) {
		m.append(ln)
	}
	m.planBannerShown = true
}

// removePlanBanner removes the current-mode banner when leaving Plan. The
// banner is live mode chrome, not a historical transcript event: retaining it
// after switching to Build/Auto makes the screen claim the session is still
// read-only. Only remove the recognizable three-line box, never user text
// that happens to mention the title.
func (m *Model) removePlanBanner() {
	var out []string
	for i := 0; i < len(m.lines); i++ {
		line := strings.TrimSpace(stripANSI(m.lines[i]))
		if strings.Contains(line, planBannerTitle) && strings.HasPrefix(line, "┌") && i+2 < len(m.lines) {
			next := strings.TrimSpace(stripANSI(m.lines[i+1]))
			bottom := strings.TrimSpace(stripANSI(m.lines[i+2]))
			if strings.HasPrefix(next, "│") && strings.HasPrefix(bottom, "└") {
				i += 2
				continue
			}
		}
		out = append(out, m.lines[i])
	}
	if len(out) == len(m.lines) {
		m.planBannerShown = false
		return
	}
	wasBottom := m.vp.AtBottom()
	m.lines = out
	m.vp.SetContent(strings.Join(m.lines, "\n"))
	if wasBottom {
		m.vp.GotoBottom()
	}
	m.planBannerShown = false
}

// freshLines rebuilds a pristine-session transcript: splash plus, when the
// session opens in Plan (the always-cautious default, spec §2.1), the
// read-only banner. Used at New time and on resize-while-pristine.
func (m *Model) freshLines(width int) []string {
	out := splashLines(m.root, m.model, m.budget, width)
	if m.curMode == mode.Plan {
		out = append(out, planBanner(width)...)
	}
	return out
}

// todoDigest is the per-session revision key for the Update-Todos block
// (spec §2.9 "revision, not duplication"): id + checkbox state + text per
// item, so any completion, addition, or rewording revises but an unchanged
// state never reprints.
func todoDigest(items []tools.TodoItem) string {
	var b strings.Builder
	for _, it := range items {
		done := "0"
		if it.Done {
			done = "1"
		}
		fmt.Fprintf(&b, "%d:%s:%s\n", it.ID, done, it.Text)
	}
	return b.String()
}

// todoActiveIndex is the bold row (§1.4: bold only for the active todo):
// the first unchecked item, or -1 when there is none (empty or all done).
func todoActiveIndex(items []tools.TodoItem) int {
	for i, it := range items {
		if !it.Done {
			return i
		}
	}
	return -1
}

// renderTodoBlock renders the full current list (§2.9 mockup): a `● Update
// Todos` header with one 2-space-indented row per item, `☑`/`□` per §1.2
// and never a percentage bar. Glyphs carry §1.2's colors (☑ success, □
// muted); text is fg-dim for resolved items, fg-muted for pending ones,
// and the active item (first unchecked) alone is bold fg per §1.4.
func renderTodoBlock(items []tools.TodoItem) string {
	mut := lipgloss.NewStyle().Foreground(fgMuted)
	head := mut.Render("● ") + lipgloss.NewStyle().Foreground(fg).Render("Update Todos")
	doneMark := lipgloss.NewStyle().Foreground(success).Render("☑")
	todoMark := lipgloss.NewStyle().Foreground(fgMuted).Render("□")
	doneText := lipgloss.NewStyle().Foreground(fgDim)
	todoText := lipgloss.NewStyle().Foreground(fgMuted)
	activeText := lipgloss.NewStyle().Foreground(fg).Bold(true)
	active := todoActiveIndex(items)
	out := []string{head}
	for i, it := range items {
		mark, style := todoMark, todoText
		if it.Done {
			mark, style = doneMark, doneText
		} else if i == active {
			style = activeText
		}
		out = append(out, "  "+mark+" "+style.Render(it.Text))
	}
	return strings.Join(out, "\n")
}

// todoSnapshot reads the live TodoManager through the loop's registry
// (TUI holds loop → loop.Reg.Get("todo_write") → TodoWrite{Mgr}).
// ok=false when anything on that chain is missing — the caller then
// renders no block rather than parsing result text.
func (m *Model) todoSnapshot() ([]tools.TodoItem, bool) {
	if m.loop == nil || m.loop.Reg == nil {
		return nil, false
	}
	t, ok := m.loop.Reg.Get("todo_write")
	if !ok {
		return nil, false
	}
	tw, ok := t.(*tools.TodoWrite)
	if !ok || tw == nil || tw.Mgr == nil {
		return nil, false
	}
	return tw.Mgr.Snapshot(), true
}

// maybeAppendTodoBlock posts the §2.9 block after a todo_write result
// lands: the raw result stays (audit trail), and the structured block
// follows it — unless the manager is unreachable (no block, no crash) or
// holds nothing, or its digest matches the last rendered block (an
// unchanged reprint is a bug per §2.9, so it is skipped).
func (m *Model) maybeAppendTodoBlock() {
	if !m.todoPending {
		return
	}
	m.todoPending = false
	items, ok := m.todoSnapshot()
	if !ok || len(items) == 0 {
		return
	}
	if d := todoDigest(items); d == m.lastTodoDigest {
		return
	} else {
		m.lastTodoDigest = d
	}
	m.append(renderTodoBlock(items))
}
