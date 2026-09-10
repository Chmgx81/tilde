package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// updatePicker routes navigation keys when the slash palette or the @
// picker is open. Typing keys fall through (false) so the composer keeps
// filtering. NOTE: the updated model is returned — callers must use it,
// or every mutation here is silently discarded.
func (m Model) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.Type {
	case tea.KeyUp:
		moved := false
		if m.slashOpen && len(m.slashItems) > 1 {
			m.slashCursor = (m.slashCursor - 1 + len(m.slashItems)) % len(m.slashItems)
			moved = true
		}
		if m.atOpen && len(m.atItems) > 1 {
			m.atCursor = (m.atCursor - 1 + len(m.atItems)) % len(m.atItems)
			moved = true
		}
		// An empty (or single-row) list has no cursor movement to own —
		// fall through so transcript scroll still works.
		return m, nil, moved
	case tea.KeyDown:
		moved := false
		if m.slashOpen && len(m.slashItems) > 1 {
			m.slashCursor = (m.slashCursor + 1) % len(m.slashItems)
			moved = true
		}
		if m.atOpen && len(m.atItems) > 1 {
			m.atCursor = (m.atCursor + 1) % len(m.atItems)
			moved = true
		}
		return m, nil, moved
	case tea.KeyEsc:
		// Dismiss for the current text. A bare flag would be re-opened
		// by the next background refresh (cursor blink); pinning the
		// exact text keeps it shut until the user types again.
		m.dismissed = m.ta.Value()
		m.slashOpen, m.atOpen = false, false
		return m, nil, true
	case tea.KeyCtrlJ:
		// Newline belongs to the composer, not the picker: dismiss
		// first so the filter is never stranded across a line break,
		// then let the composer own the key like the no-picker path.
		m.dismissed = m.ta.Value()
		m.slashOpen, m.atOpen = false, false
		m.ta.InsertString("\n")
		m.syncComposer()
		m.refreshPickers()
		return m, nil, true
	case tea.KeyEnter:
		if m.running {
			return m, nil, true // a turn owns the loop; Enter waits its turn
		}
		goal := strings.TrimSpace(m.ta.Value())
		if m.slashOpen {
			if len(m.slashItems) == 0 {
				// Empty filter is the picker's state, not the user's
				// error — say so instead of "unknown command".
				m.ta.Reset()
				m.dismissed = ""
				m.slashOpen, m.atOpen = false, false
				m.shellArmed = false
				m.refreshPlaceholder()
				m.append("✗ no command matches — type / to browse, esc to cancel.")
				return m, nil, true
			}
			sel := m.slashItems[min(m.slashCursor, len(m.slashItems)-1)]
			m.ta.Reset()
			m.dismissed = ""
			m.slashOpen, m.atOpen = false, false
			m.shellArmed = false
			m.refreshPlaceholder()
			cmd, args := parseSlash(goal)
			slashCmd := m.runSlash(cmd, args, sel)
			return m, slashCmd, true
		}
		if m.atOpen && len(m.atItems) > 0 {
			// NOTE: accept BEFORE any Reset — the value is the query.
			m.acceptAt(m.atItems[min(m.atCursor, len(m.atItems)-1)].path)
			m.dismissed = ""
			return m, nil, true
		}
		return m, nil, false
	}
	return m, nil, false
}

// refreshPickers re-filters both pickers from the composer text after
// every message — including background ones like cursor blink. Cursor
// positions survive refreshes while the query is unchanged, so navigation
// never snaps back; a picker dismissed with Esc stays shut for the exact
// text it was dismissed on.
func (m *Model) refreshPickers() {
	val := m.ta.Value()
	// New typing abandons history navigation (blink ticks carry the
	// same text and must not): the position survives only while the box
	// still shows the recalled entry.
	if m.histIdx < len(m.hist) && val != m.hist[m.histIdx] {
		m.histIdx = len(m.hist)
		m.histDraft = ""
	}
	// Shell disarm runs even for dismissed text: an empty box is never a
	// shell command, picker state aside.
	if m.shellArmed && val == "" {
		m.shellArmed = false
		m.refreshPlaceholder()
	}
	if val == m.dismissed {
		m.slashOpen, m.atOpen = false, false
		return
	}
	// Shell mode is derived, never lifted: a leading "!" arms the shell
	// hint row below (same slot as the / and @ pickers), but the bang
	// stays in the box — stripping it on keypress made "!" feel dead.
	// (Semantics unchanged: a leading "!" always meant shell escape.)
	if armed := strings.HasPrefix(val, "!"); armed != m.shellArmed {
		m.shellArmed = armed
		m.refreshPlaceholder()
	}
	if strings.HasPrefix(val, "/") && !strings.Contains(val, "\n") {
		m.slashItems = filterSlash(val)
		if m.slashFilter != val {
			m.slashFilter, m.slashCursor = val, 0
		}
		if m.slashCursor >= len(m.slashItems) {
			m.slashCursor = 0
		}
		m.slashOpen = true
		m.atOpen = false
		return
	}
	m.slashOpen = false
	if q, ok := activeAtQuery(val); ok {
		if !m.atLoaded {
			m.atFiles = walkFiles(m.root)
			m.atLoaded = true
		}
		if m.atQuery != q {
			m.atQuery, m.atCursor = q, 0
		}
		m.atItems = matchFiles(m.atFiles, q)
		if m.atCursor >= len(m.atItems) {
			m.atCursor = 0
		}
		// Stay open on zero matches: the dropdown then shows the
		// empty state instead of vanishing mid-query (same contract
		// as the slash picker's "no command matches" message).
		m.atOpen = true
		return
	}
	m.atOpen = false
}

// acceptAt replaces the live @query with the chosen path. The path lands
// as plain text so it survives editing the rest of the line.
func (m *Model) acceptAt(path string) {
	val := m.ta.Value()
	at := strings.LastIndex(val, "@"+m.atQuery)
	if at < 0 {
		return
	}
	m.ta.SetValue(val[:at] + "@" + path + " " + val[at+len("@"+m.atQuery):])
	m.atOpen = false
}
