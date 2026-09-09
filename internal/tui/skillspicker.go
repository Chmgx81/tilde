package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"tilde/internal/provider"
	"tilde/internal/skills"
)

// openSkills rescans both skill dirs and opens the picker. Rescanning on
// every open is what makes mid-session installs work with no restart.
func (m *Model) openSkills() {
	if m.running {
		m.append("✗ an agent turn is running — Esc twice, then /skills.")
		return
	}
	ix, err := skills.Scan(m.root)
	if err != nil {
		m.append("✗ " + err.Error())
	}
	items := ix.List()
	if len(items) == 0 {
		m.append("● No skills installed · add NAME.md to .tilde/skills/ or ~/.tilde/skills/.")
		return
	}
	m.skillsItems, m.skillsCursor, m.skillsQuery, m.skillsOpen = items, 0, "", true
}

// updateSkills drives the picker: ↑↓ select, typing filters, Backspace
// edits the filter, enter loads, esc clears the filter first, then closes.
func (m Model) updateSkills(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit // the picker never traps quit
	case tea.KeyUp:
		rows := m.filteredSkills()
		if len(rows) > 0 {
			m.skillsCursor = (m.skillsCursor - 1 + len(rows)) % len(rows)
		}
		return m, nil
	case tea.KeyDown:
		rows := m.filteredSkills()
		if len(rows) > 0 {
			m.skillsCursor = (m.skillsCursor + 1) % len(rows)
		}
		return m, nil
	case tea.KeyEsc:
		if m.skillsQuery != "" {
			m.skillsQuery = ""
			m.skillsCursor = 0
			return m, nil
		}
		m.skillsOpen = false
		return m, nil
	case tea.KeyEnter:
		rows := m.filteredSkills()
		if m.skillsCursor >= 0 && m.skillsCursor < len(rows) {
			m.loadSkill(rows[m.skillsCursor])
		}
		return m, nil
	case tea.KeyBackspace:
		if m.skillsQuery != "" {
			r := []rune(m.skillsQuery)
			m.skillsQuery = string(r[:len(r)-1])
			m.skillsCursor = 0
		}
		return m, nil
	case tea.KeyRunes:
		if msg.String() == "/" {
			// "/" focuses search = reset the filter (§2.7 footer).
			// Appending it to the query would break filtering instead.
			m.skillsQuery = ""
			m.skillsCursor = 0
			return m, nil
		}
		m.skillsQuery += msg.String()
		m.skillsCursor = 0
		return m, nil
	}
	return m, nil
}

func (m Model) filteredSkills() []skills.Skill {
	q := strings.ToLower(m.skillsQuery)
	if q == "" {
		return m.skillsItems
	}
	var out []skills.Skill
	for _, sk := range m.skillsItems {
		if strings.Contains(strings.ToLower(sk.Name+" "+sk.Description), q) {
			out = append(out, sk)
		}
	}
	return out
}

// loadSkill activates a skill: full body enters live context (not the
// prompt — that only ever carried the one-liner), with an audit line in
// the transcript and the session log.
func (m *Model) loadSkill(sk skills.Skill) {
	m.loop.AppendMsg(provider.Message{Role: "system",
		Content: "[Skill loaded: " + sk.Name + "]\n" + sk.Body})
	m.append("● Loaded skill: " + sk.Name + " (" + sk.Scope + ")")
	if m.loop.Log != nil {
		_ = m.loop.Log.Append("system", map[string]any{"skill_loaded": sk.Name})
	}
	m.skillsOpen = false
}

// skillsView renders the picker: numbered rows, source tags right-aligned
// and dim (provenance metadata, lowest priority on the row).
func (m Model) skillsView() string {
	rows := m.filteredSkills()
	var proj, user int
	for _, sk := range m.skillsItems {
		if sk.Scope == "project" {
			proj++
		} else {
			user++
		}
	}
	var b strings.Builder
	// Header docks the counts right at the transcript width — a header
	// row never wraps either.
	counts := fmt.Sprintf("%d project · %d user", proj, user)
	pad := m.vp.Width - len([]rune("  Skills")) - len([]rune(counts))
	if pad < 1 {
		pad = 1
	}
	b.WriteString(truncANSI(fmt.Sprintf("  Skills%s%s", strings.Repeat(" ", pad), counts), m.vp.Width))
	b.WriteString("\n")
	if m.skillsQuery != "" {
		fmt.Fprintf(&b, "  /%s\n", m.skillsQuery)
	}
	left := make([]string, len(rows))
	maxLeft, maxScope := 0, 0
	for i, sk := range rows {
		name, description, scope := ansi.Strip(sk.Name), ansi.Strip(sk.Description), ansi.Strip(sk.Scope)
		num := fmt.Sprintf("%d. %-22s", i+1, name)
		left[i] = "  " + num + "  " + truncMiddle(description, 44)
		if w := ansi.StringWidth(left[i]); w > maxLeft {
			maxLeft = w
		}
		if w := ansi.StringWidth(scope); w > maxScope {
			maxScope = w
		}
	}
	// Rows never exceed the transcript width: shrink the text column
	// first so the scope column stays aligned instead of wrapping.
	if m.vp.Width > 0 {
		if budget := m.vp.Width - maxScope - 4; budget < maxLeft {
			maxLeft = max(budget, 10)
		}
		for i := range left {
			left[i] = truncANSI(left[i], maxLeft)
		}
	}
	for i, sk := range rows {
		scope := ansi.Strip(sk.Scope)
		row := padRunesRight(left[i], maxLeft) + "  " + padRunesLeft(scope, maxScope)
		if i == m.skillsCursor {
			row = lipgloss.NewStyle().Foreground(fgOnSelect).Background(accentSelect).Render("→" + row[1:])
		} else {
			lp := padRunesRight(left[i], maxLeft)
			sc := padRunesLeft(scope, maxScope)
			row = " " + lipgloss.NewStyle().Foreground(fgMuted).Render(lp[1:]) +
				"  " + lipgloss.NewStyle().Foreground(fgDim).Render(sc)
		}
		b.WriteString(row)
		b.WriteString("\n")
	}
	if len(rows) == 0 {
		b.WriteString("  no skills match — backspace to clear the filter\n")
	}
	b.WriteString(truncANSI("  ↑↓ select · enter load · / search · esc cancel", m.vp.Width))
	return b.String()
}

// padRunesRight pads s to exactly w terminal cells. Display width is not the
// same as bytes or runes for CJK, emoji, and combining marks.
func padRunesRight(s string, w int) string {
	if d := w - ansi.StringWidth(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// padRunesLeft is padRunesRight for right-aligned columns (scope tags).
func padRunesLeft(s string, w int) string {
	if d := w - ansi.StringWidth(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}
