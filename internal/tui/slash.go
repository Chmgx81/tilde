package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tilde/internal/mode"
	"tilde/internal/provider"
	"tilde/internal/sandbox"
	"tilde/internal/tools"
)

type slashRow struct{ cmd, desc string }

// slashCommands is the palette: descriptions in a fixed-width column so
// they align regardless of command name length.
var slashCommands = []slashRow{
	{"/model <model>", "Set the current model"},
	{"/mode <plan|build|auto>", "Set mode explicitly (same as Tab)"},
	{"/skills", "Browse and load a skill"},
	{"/compact [focus]", "Summarize older turns to reclaim context"},
	{"/clear", "Start a new session"},
	{"/copy [n]", "Copy transcript (or line n) to the clipboard"},
	{"/sandbox", "Show current policy tier and overrides"},
	{"/diff", "Show working-tree diff for review"},
	{"/undo [n]", "Revert the last n mutating steps"},
	{"/sessions", "List and resume a past session"},
	{"/help", "Full keybinding + command reference"},
	{"/quit", "Exit tilde"},
}

// rankSlash scores a palette row against the typed filter: prefix
// matches outrank substring matches; equal ranks keep the palette's
// order so /mode never hides behind /model when the user typed /mode.
func rankSlash(r slashRow, name string) int {
	cmd := strings.ToLower(strings.Fields(r.cmd)[0])
	switch {
	case name == "":
		return 0
	case cmd == "/"+name:
		return 0
	case strings.HasPrefix(cmd, "/"+name):
		return 1
	default:
		return 2
	}
}
func filterSlash(query string) []slashRow {
	q := strings.ToLower(strings.TrimPrefix(query, "/"))
	fields := strings.Fields(q)
	name := ""
	if len(fields) > 0 {
		name = fields[0]
	}
	type ranked struct {
		row   slashRow
		score int
		nlen  int // command-name length: /mode beats /model on ties
	}
	var out []ranked
	for _, r := range slashCommands {
		cmd := strings.ToLower(r.cmd)
		if name == "" || strings.Contains(cmd, name) {
			out = append(out, ranked{r, rankSlash(r, name), len(strings.Fields(cmd)[0])})
		}
	}
	// Rank first; among equals, shorter command name wins — but only
	// when the user actually typed a filter. A bare "/" must show the
	// palette's canonical order (stable sort keeps it).
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score < out[j].score
		}
		if name != "" && out[i].nlen != out[j].nlen {
			return out[i].nlen < out[j].nlen
		}
		return false
	})
	rows := make([]slashRow, len(out))
	for i, r := range out {
		rows[i] = r.row
	}
	return rows
}

// parseSlash splits "/cmd rest of args" into its parts.
func parseSlash(line string) (cmd, args string) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return "", ""
	}
	parts := strings.SplitN(line[1:], " ", 2)
	cmd = "/" + strings.ToLower(parts[0])
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	return cmd, args
}

// slashDropdown renders the inline palette beneath the composer.
// Descriptions stay right of a dynamic-width command column (longest
// currently visible) and trim to the composer width instead of
// wrapping — a dropdown row never wraps.
func slashDropdown(items []slashRow, cursor, width int) string {
	if len(items) == 0 {
		return lipgloss.NewStyle().Foreground(fgDim).Render("  no matching command")
	}
	cmdWidth := 0
	for _, r := range items {
		if n := len([]rune(r.cmd)); n > cmdWidth {
			cmdWidth = n
		}
	}
	var b strings.Builder
	for i, r := range items {
		avail := width - 2 - cmdWidth - 2 // leading 2 + gap; may go negative on very narrow terms
		row := "  " + padRunesRight(r.cmd, cmdWidth)
		if avail > 0 {
			row += "  " + truncMiddle(r.desc, avail)
		}
		if i == cursor {
			row = lipgloss.NewStyle().Background(accentSelect).Render("→" + row[1:])
		} else {
			row = " " + lipgloss.NewStyle().Foreground(fgMuted).Render(row[1:])
		}
		b.WriteString(row)
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// runSlash executes a palette command. It returns a tea.Cmd for the
// async cases (compact) and nil otherwise.
func (m *Model) runSlash(cmd, args string, selected slashRow) tea.Cmd {
	// ↑↓ + Enter runs the highlighted row, keeping any typed arguments.
	if selected.cmd != "" {
		name := strings.Fields(selected.cmd)[0]
		if !strings.HasPrefix(strings.ToLower(cmd), strings.ToLower(name)) {
			cmd = name
		}
	}
	switch cmd {
	case "/mode":
		if m.running {
			m.append("✗ an agent turn is running — Esc twice, then /mode.")
			return nil
		}
		m.handleModeCmd(args)
		return nil
	case "/clear":
		if m.running {
			m.append("✗ an agent turn is running — Esc twice, then /clear.")
			return nil
		}
		m.lines = nil
		m.vp.SetContent("")
		m.pasteEcho = nil   // echo line keys died with the transcript
		m.selActive = false // an in-flight drag has nothing to anchor to
		m.splashN = 0
		m.vp.GotoBottom()
		m.loop.SetMsgs(nil)
		m.atLoaded = false // file list may have changed; rescan on next @
		m.atFiles = nil
		return nil
	case "/copy":
		return m.copyLines(args)
	case "/compact":
		if m.running {
			m.append("✗ an agent turn is running — Esc twice, then /compact.")
			return nil
		}
		return m.compactCmd(args)
	case "/sandbox":
		m.append("● sandbox: " + sandbox.StatusLine())
		m.append("  ⎿ ask: write_file · edit_file · shell_command · git_worktree_* (destructive shell denied; read-only tools allow)")
		if m.loop.Reg != nil && m.loop.Reg.Hooks != nil {
			n := len(m.loop.Reg.Hooks.Before) + len(m.loop.Reg.Hooks.After)
			m.append(fmt.Sprintf("  ⎿ hooks: %d configured (project + user)", n))
		}
		return nil
	case "/diff":
		m.showDiff()
		return nil
	case "/undo":
		if m.running {
			m.append("✗ an agent turn is running — Esc twice, then /undo.")
			return nil
		}
		m.doUndo(args)
		return nil
	case "/help":
		if m.running {
			m.append("✗ an agent turn is running — Esc twice, then /help.")
			return nil
		}
		m.helpOpen = true
		return nil
	case "/quit":
		// Seamless exit: stop a running turn first so no orphaned work
		// outlives the UI, then quit. No confirm and no running-guard —
		// /quit is explicit.
		if m.cancel != nil {
			m.cancel()
		}
		return tea.Quit
	case "/sessions":
		if m.running {
			m.append("✗ an agent turn is running — Esc twice, then /sessions.")
			return nil
		}
		m.OpenResume()
		return nil
	case "/model":
		if m.running {
			m.append("✗ an agent turn is running — Esc twice, then /model.")
			return nil
		}
		return m.switchModel(args)
	case "/skills":
		m.openSkills()
		return nil
	default:
		m.append(fmt.Sprintf("✗ unknown command %q — type / to browse the palette.", cmd))
		return nil
	}
}

// runningBackground lists in-flight shell tasks across the registry's
// Shell tools (normally one). Undo refuses while any are live.
func runningBackground(m *Model) []string {
	if m.loop.Reg == nil {
		return nil
	}
	var ids []string
	for _, n := range m.loop.Reg.Names() {
		t, ok := m.loop.Reg.Get(n)
		if !ok {
			continue
		}
		if sh, ok := t.(*tools.Shell); ok && sh.Tasks != nil {
			ids = append(ids, sh.Tasks.RunningIDs()...)
		}
	}
	return ids
}

func pluralize(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// showDiff renders the working-tree diff for review (/diff). Human
// surface only — it never enters model context.
func (m *Model) showDiff() {
	if m.loop.Reg == nil {
		m.append("✗ no tool registry attached.")
		return
	}
	t, ok := m.loop.Reg.Get("git_diff")
	if !ok {
		m.append("✗ git_diff tool not registered.")
		return
	}
	out, err := t.Exec(context.Background(), map[string]any{})
	if err != nil {
		// First line only: git usage errors are multi-line and would
		// smear the one-event-one-line transcript discipline.
		m.append("✗ " + firstLine(err.Error()))
		return
	}
	m.append(lipgloss.NewStyle().Foreground(fgMuted).Render("● diff (working tree vs HEAD)"))
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, "diff --git ") {
			m.append("● " + strings.TrimPrefix(ln, "diff --git "))
			continue
		}
		if isDiffLine(ln) {
			m.append(renderDiffBlock([]string{ln})[0])
			continue
		}
		// --stat summary and stray lines: muted context, never dropped.
		if strings.TrimSpace(ln) != "" {
			m.append(lipgloss.NewStyle().Foreground(fgMuted).Render("  " + ln))
		}
	}
}

// doUndo reverts the last n mutating steps (/undo [n], default 1).
func (m *Model) doUndo(args string) {
	if m.loop.Reg == nil || m.loop.Reg.Undo == nil {
		m.append("✗ undo is unavailable in this session.")
		return
	}
	// A running background task may still be writing: restoring under it
	// loses updates either way. Kill or wait first — the message says how.
	if ids := runningBackground(m); len(ids) > 0 {
		m.append(fmt.Sprintf("✗ background %s still running — kill %s (shell_poll kill) or wait, then /undo.",
			pluralize(len(ids), "task", "tasks"), strings.Join(ids, ", ")))
		return
	}
	n := 1
	if strings.TrimSpace(args) != "" {
		var err error
		n, err = strconv.Atoi(strings.Fields(args)[0])
		if err != nil {
			m.append(fmt.Sprintf("✗ bad count %q: send /undo [n] with n >= 1, e.g. /undo 2.", args))
			return
		}
	}
	var seen *tools.SeenMap
	if t, ok := m.loop.Reg.Get("read_file"); ok {
		if rf, ok := t.(*tools.ReadFile); ok {
			seen = rf.Seen
		}
	}
	report, err := m.loop.Reg.Undo.Undo(n, func(p string) {
		if seen != nil {
			seen.Mark(p)
		}
	})
	if err != nil {
		m.append("✗ " + err.Error())
		return
	}
	for _, ln := range strings.Split(report, "\n") {
		m.append(ln)
	}
	if m.loop.Log != nil {
		_ = m.loop.Log.Append("system", map[string]any{"undo": report})
	}
}

// compactCmd runs CompactNow off the render thread.
func (m *Model) compactCmd(focus string) tea.Cmd {
	loop := m.loop
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		marker, _ := loop.CompactNow(ctx, focus)
		return compactDoneMsg{Marker: marker}
	}
}

// switchModel repoints the Ollama backend live.
func (m *Model) switchModel(name string) tea.Cmd {
	if name == "" {
		m.append("● current model: " + m.model + " — usage: /model <name>")
		return nil
	}
	if o, ok := m.loop.Prov.(*provider.Ollama); ok {
		o.Model = name
		m.model = o.Name()
		m.append("● model switched to " + m.model)
		if m.loop.Log != nil {
			_ = m.loop.Log.Append("system", map[string]any{"model": m.model})
		}
		return nil
	}
	m.append("✗ /model only supports the Ollama backend in this build.")
	return nil
}

func (m *Model) handleModeCmd(s string) tea.Cmd {
	before := m.curMode
	switch strings.ToLower(s) {
	case "plan":
		m.curMode = mode.Plan
	case "build":
		m.curMode = mode.Build
	case "auto":
		m.curMode = mode.Auto
	default:
		m.append("✗ unknown mode " + s + " — use plan|build|auto")
		return nil
	}
	m.loop.SetMode(m.curMode)
	m.refreshPlaceholder()
	if before == m.curMode {
		return nil
	}
	// Toast registers the transition as an event, then collapses back
	// into the status bar — it never lingers as chrome.
	return m.setToast(fmt.Sprintf("⏵ Mode: %s → %s", before, m.curMode))
}
