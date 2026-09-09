package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Subagent / parallel-exploration rows (spec §2.19). Render-state only:
// a spawn_explore/spawn_work tool_call appends one dim `⋮` running row,
// and its matching tool_result appends the `│` completed form with a
// right-aligned [done]/[failed] status. No focus model, no toggles —
// the same rationale as the read-only grouping (no focus model exists
// to hang a toggle on; Tab cycles modes, Space types).
//
// Matching is positional FIFO: the loop emits call then result strictly
// back to back for a dispatched call (execApproved / ordered flushBatch),
// so the result immediately following a spawn call is its own. Denials
// (mode gate, policy tier, ask-declined, doom nudge) emit a tool_result
// with NO preceding tool_call — with nothing pending those fall through
// to the normal result path, never misattributed here.
//
// Call-time task text is a known limit: the tool_call event carries only
// shortArgs (path/command/pattern), so a spawn_explore call arrives with
// no task text and spawn_work with at most its worktree path. The running
// row shows what is known (type · model, plus path when present);
// per-task names surface on completion, parsed from the result body the
// tools already format as `│ <task> [done|failed]` lines (explore) or the
// single session path (work). apply_work/discard_work are deliberately
// NOT grouped here — no cross-call path matching, they render as ordinary
// ● call lines (minimal: group by the spawn call only).
//
// Parent synthesis needs no work: model prose arrives as an assistant
// event and renders below the finished batch naturally (verified in
// TestSubagentSynthesisBelowBatch).

// subSpawn is one dispatched spawn awaiting its result.
type subSpawn struct {
	typ    string // explore | work
	detail string // call-time remainder (worktree path for spawn_work, "" for explore)
	model  string // parent model at dispatch — per-row attribution per spec §2.19
}

// isSubagentSpawn reports the two spawn verbs that open a subagent row.
// apply_work/discard_work are excluded by design (see above).
func isSubagentSpawn(verb string) bool {
	return verb == "spawn_explore" || verb == "spawn_work"
}

// subagentType maps a spawn verb to its row type word.
func subagentType(verb string) string {
	if verb == "spawn_work" {
		return "work"
	}
	return "explore"
}

// subDone is one completed row parsed from (or inferred for) a result.
type subDone struct {
	task   string
	failed bool
}

// subagentWidth is the visible column [done]/[failed] docks against:
// the viewport width minus the wrap guard in append (which wraps at
// Width-1). Falls back to the 78-column reference width.
func (m *Model) subagentWidth() int {
	if m.vp.Width > 1 {
		return m.vp.Width - 1
	}
	return 77
}

// pushSubagentRun records a spawn dispatch and appends its dim running row:
// `⋮ <type> [<detail>] · <model>` (model omitted when unknown).
func (m *Model) pushSubagentRun(verb, text string) {
	_, rest := splitVerb(text)
	detail := strings.TrimSpace(rest)
	typ := subagentType(verb)
	m.subPending = append(m.subPending, subSpawn{typ: typ, detail: detail, model: m.model})
	m.append(renderSubagentRunning(typ, detail, m.model))
}

// renderSubagentRunning styles one still-running row: `⋮` in fg-dim,
// the whole row dim — it is pending, not yet a result.
func renderSubagentRunning(typ, detail, model string) string {
	label := typ
	if detail != "" {
		label += " " + detail
	}
	if model != "" {
		label += " · " + model
	}
	return lipgloss.NewStyle().Foreground(fgDim).Render("⋮ " + label)
}

// completeSubagentRun pops the matching spawn and appends one `│` row
// per finished task, then renders the remaining body (summaries, headers)
// through the normal result path — findings are never dropped.
func (m *Model) completeSubagentRun(body string) {
	if len(m.subPending) == 0 {
		m.append(renderToolResult(body))
		return
	}
	sp := m.subPending[0]
	m.subPending = m.subPending[1:]
	rows, rest := splitSubagentResult(body, sp)
	for _, r := range rows {
		m.append(renderSubagentDone(r.task, sp.typ, sp.model, r.failed, m.subagentWidth()))
	}
	if strings.TrimSpace(rest) != "" {
		m.append(renderToolResult(rest))
	}
}

// splitSubagentResult pulls per-task `│ <task> [done|failed]` lines out
// of a spawn result body (the shape ExploreTool.Exec already emits) and
// returns the leftover body. With no per-task lines (spawn_work's single
// session, or an error such as a blown subagent budget) it returns one
// row for the spawn itself — failed when the body reads as a failure —
// and keeps the whole body for normal rendering.
func splitSubagentResult(body string, sp subSpawn) ([]subDone, string) {
	var rows []subDone
	var rest []string
	for _, ln := range strings.Split(body, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "│") {
			state := ""
			switch {
			case strings.HasSuffix(t, "[done]"):
				state = "[done]"
			case strings.HasSuffix(t, "[failed]"):
				state = "[failed]"
			}
			if state != "" {
				mid := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, "│"), state))
				rows = append(rows, subDone{task: mid, failed: state == "[failed]"})
				continue
			}
		}
		rest = append(rest, ln)
	}
	if len(rows) == 0 {
		return []subDone{{task: sp.detail, failed: strings.Contains(body, "failed")}}, body
	}
	return rows, strings.Join(rest, "\n")
}

// renderSubagentDone styles one finished row: `│` dim, the task in full
// brightness (it is the headline, per §1.4), type · model muted, and the
// status right-aligned — [done] in success, [failed] in danger (the same
// right-aligned-status convention as [installed] in §2.17).
func renderSubagentDone(task, typ, model string, failed bool, width int) string {
	status := "[done]"
	st := lipgloss.NewStyle().Foreground(success)
	if failed {
		status = "[failed]"
		st = lipgloss.NewStyle().Foreground(danger)
	}
	dim := lipgloss.NewStyle().Foreground(fgDim)
	mut := lipgloss.NewStyle().Foreground(fgMuted)
	full := lipgloss.NewStyle().Foreground(fg)
	suffix := typ
	if model != "" {
		suffix += " · " + model
	}
	var mid, midPlain string
	if task != "" {
		mid, midPlain = full.Render(task)+mut.Render("  "+suffix), task+"  "+suffix
	} else {
		mid, midPlain = mut.Render(suffix), suffix
	}
	pad := width - len([]rune("│ "+midPlain)) - len([]rune(status))
	if pad < 1 {
		pad = 1
	}
	return dim.Render("│ ") + mid + strings.Repeat(" ", pad) + st.Render(status)
}
