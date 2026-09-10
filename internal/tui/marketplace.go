package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"tilde/internal/hooks"
	"tilde/internal/marketplace"
	"tilde/internal/mcp"
	"tilde/internal/plugin"
	"tilde/internal/skills"
)

var marketplaceTabs = marketplace.Kinds

type marketplaceAction string

const (
	marketplaceActionInstall marketplaceAction = "install"
	marketplaceActionEnable  marketplaceAction = "enable"
	marketplaceActionDisable marketplaceAction = "disable"
	marketplaceActionRemove  marketplaceAction = "remove"
	marketplaceActionUpdate  marketplaceAction = "update"
)

func (m *Model) openMarketplace() {
	if m.running {
		m.append("✗ an agent turn is running — Esc twice, then /plugins.")
		return
	}
	ix, err := skills.Scan(m.root)
	if err != nil {
		m.append("✗ cannot scan project skills: " + err.Error() + " — check .tilde/skills/ permissions and retry /plugins.")
	}
	if builtins, berr := skills.Bundled(); berr == nil {
		for _, sk := range builtins {
			ix.AddBuiltin(sk)
		}
	}
	home, _ := os.UserHomeDir()
	pluginDir := filepath.Join(home, ".tilde", "plugins")
	userCfg, _ := mcp.LoadFile(filepath.Join(home, ".tilde", "mcp.json"))
	projectCfg, _ := mcp.LoadFile(filepath.Join(m.root, ".tilde", "mcp.json"))
	merged := mcp.Merge(userCfg, projectCfg)
	var mcpNames []string
	for name := range merged {
		mcpNames = append(mcpNames, name)
	}
	sort.Strings(mcpNames)
	reg, errs := marketplace.Discover(m.root, pluginDir, ix, mcpNames)
	for _, e := range errs {
		m.append("⚠ " + e.Error())
	}
	// Hook configs are registry metadata only. They are never executed by
	// opening this browser, which keeps discovery outside the action boundary.
	addHookRows(&reg, filepath.Join(m.root, ".tilde", "hooks.yaml"), "project", true)
	addHookRows(&reg, filepath.Join(home, ".tilde", "hooks.yaml"), "user", true)
	m.marketplaceItems = reg
	m.marketplaceCursor, m.marketplaceQuery = 0, ""
	m.marketplaceTab = 0
	m.marketplacePending = nil
	m.marketplaceDetail = nil
	m.marketplaceAction = ""
	m.marketplaceOpen = true
}

func addHookRows(reg *marketplace.Registry, path, scope string, installed bool) {
	cfg, err := hooks.LoadFile(path)
	if err != nil {
		return
	}
	counts := map[string]int{}
	add := func(phase, tool string) {
		base := phase + ":" + tool
		counts[base]++
		name := scope + "/" + base
		if counts[base] > 1 {
			name = fmt.Sprintf("%s#%d", base, counts[base])
			name = scope + "/" + name
		}
		reg.Add(marketplace.Item{Kind: marketplace.Hooks, Name: name, Scope: scope, Source: path, Description: fmt.Sprintf("%s hook for %s", phase, tool), Installed: installed})
	}
	for tool, commands := range cfg.Before {
		for range commands {
			add("before", tool)
		}
	}
	for tool, commands := range cfg.After {
		for range commands {
			add("after", tool)
		}
	}
	for range cfg.SessionStart {
		add("session_start (reserved — not executed)", "session")
	}
	for range cfg.SessionEnd {
		add("session_end (reserved — not executed)", "session")
	}
}

func (m *Model) updateMarketplace(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.marketplaceItems.Items(marketplaceTabs[m.marketplaceTab], m.marketplaceQuery)
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		if m.marketplaceQuery != "" {
			m.marketplaceQuery = ""
			m.marketplaceCursor = 0
			return m, nil
		}
		m.marketplaceOpen = false
		return m, nil
	case tea.KeyLeft:
		m.marketplaceTab = (m.marketplaceTab - 1 + len(marketplaceTabs)) % len(marketplaceTabs)
		m.marketplaceCursor = 0
		return m, nil
	case tea.KeyRight, tea.KeyTab:
		m.marketplaceTab = (m.marketplaceTab + 1) % len(marketplaceTabs)
		m.marketplaceCursor = 0
		return m, nil
	case tea.KeyUp:
		if len(rows) > 0 {
			m.marketplaceCursor = (m.marketplaceCursor - 1 + len(rows)) % len(rows)
		}
		return m, nil
	case tea.KeyDown:
		if len(rows) > 0 {
			m.marketplaceCursor = (m.marketplaceCursor + 1) % len(rows)
		}
		return m, nil
	case tea.KeyBackspace:
		if m.marketplaceQuery != "" {
			r := []rune(m.marketplaceQuery)
			m.marketplaceQuery = string(r[:len(r)-1])
			m.marketplaceCursor = 0
		}
		return m, nil
	case tea.KeyRunes:
		key := string(msg.Runes)
		if key == "i" && len(rows) > 0 && m.marketplaceCursor < len(rows) {
			item := rows[m.marketplaceCursor]
			if item.Kind != marketplace.Marketplace || !item.Installable {
				m.append("✗ " + item.Name + " is not an installable local marketplace package")
				return m, nil
			}
			m.requestMarketplaceAction(item, marketplaceActionInstall)
			return m, nil
		}
		if key == "/" {
			m.marketplaceQuery = ""
		} else {
			m.marketplaceQuery += key
		}
		m.marketplaceCursor = 0
		return m, nil
	case tea.KeyEnter:
		if len(rows) > 0 && m.marketplaceCursor < len(rows) {
			item := rows[m.marketplaceCursor]
			if item.Kind == marketplace.Skills {
				m.loadSkillByName(item.Name)
			} else {
				copy := item
				m.marketplaceDetail = &copy
			}
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) requestMarketplaceAction(item marketplace.Item, action marketplaceAction) {
	copy := item
	m.marketplacePending = &copy
	m.marketplaceAction = action
}

func (m *Model) updateMarketplaceDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	item := m.marketplaceDetail
	if item == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.marketplaceDetail = nil
		return m, nil
	case tea.KeyRunes:
		switch strings.ToLower(string(msg.Runes)) {
		case "i":
			if item.Kind == marketplace.Marketplace && item.Installable && !item.Installed {
				m.requestMarketplaceAction(*item, marketplaceActionInstall)
			}
		case "e":
			if item.Kind == marketplace.Plugins && item.Installed {
				action := marketplaceActionEnable
				if item.Enabled {
					action = marketplaceActionDisable
				}
				m.requestMarketplaceAction(*item, action)
			}
		case "r":
			if item.Kind == marketplace.Plugins && item.Installed {
				m.requestMarketplaceAction(*item, marketplaceActionRemove)
			}
		case "u":
			if item.Kind == marketplace.Plugins && item.Installed && item.UpdatePath != "" {
				m.requestMarketplaceAction(*item, marketplaceActionUpdate)
			}
		}
	}
	return m, nil
}

func (m *Model) updateMarketplaceConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.marketplacePending == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.marketplacePending = nil
		m.marketplaceAction = ""
		return m, nil
	case tea.KeyEnter:
		return m.executeMarketplacePending()
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyRunes:
		switch strings.ToLower(msg.String()) {
		case "y":
			return m.executeMarketplacePending()
		case "n":
			m.marketplacePending = nil
			m.marketplaceAction = ""
			return m, nil
		}
	}
	return m, nil
}

func (m *Model) executeMarketplacePending() (tea.Model, tea.Cmd) {
	item := m.marketplacePending
	action := m.marketplaceAction
	m.marketplacePending = nil
	m.marketplaceAction = ""
	if item == nil {
		return m, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		m.append("✗ cannot locate plugin home: " + err.Error())
		return m, nil
	}
	pluginHome := filepath.Join(home, ".tilde", "plugins")
	switch action {
	case marketplaceActionInstall:
		if _, err := plugin.Install(item.InstallPath, pluginHome); err != nil {
			m.append("✗ plugin install failed: " + safeMarketplaceText(err.Error()))
			return m, nil
		}
		m.append("✓ Installed " + safeMarketplaceText(item.Name) + " v" + safeMarketplaceText(item.Version) + " — reopen /plugins to inspect its skills")
	case marketplaceActionEnable, marketplaceActionDisable:
		enabled := action == marketplaceActionEnable
		var err error
		if enabled {
			err = plugin.Enable(pluginHome, item.Name)
		} else {
			err = plugin.Disable(pluginHome, item.Name)
		}
		if err != nil {
			m.append("✗ plugin state change failed: " + safeMarketplaceText(err.Error()))
			return m, nil
		}
		state := "disabled"
		if enabled {
			state = "enabled"
		}
		m.append("✓ " + safeMarketplaceText(item.Name) + " " + state)
	case marketplaceActionRemove:
		if !safeInstalledPluginPath(pluginHome, item.InstallPath) {
			m.append("✗ refusing to remove an install outside the plugin home")
			return m, nil
		}
		if err := plugin.Remove(pluginHome, item.Name); err != nil {
			m.append("✗ plugin remove failed: " + safeMarketplaceText(err.Error()))
			return m, nil
		}
		m.append("✓ Removed " + safeMarketplaceText(item.Name))
	case marketplaceActionUpdate:
		if item.UpdatePath == "" {
			m.append("✗ no local marketplace source is available for update")
			return m, nil
		}
		if _, err := plugin.Upgrade(item.UpdatePath, pluginHome); err != nil {
			m.append("✗ plugin update failed: " + safeMarketplaceText(err.Error()))
			return m, nil
		}
		m.append("✓ Updated " + safeMarketplaceText(item.Name) + " to v" + safeMarketplaceText(item.Version))
	default:
		return m, nil
	}
	m.refreshMarketplaceSelection(item.Kind, item.Name)
	return m, nil
}

func safeInstalledPluginPath(pluginHome, installPath string) bool {
	if installPath == "" {
		return false
	}
	home, err := filepath.Abs(pluginHome)
	if err != nil {
		return false
	}
	path, err := filepath.Abs(installPath)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(home, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && filepath.Dir(rel) == "."
}

func (m *Model) refreshMarketplaceSelection(kind marketplace.Kind, name string) {
	tab := 0
	for i, candidate := range marketplaceTabs {
		if candidate == kind {
			tab = i
			break
		}
	}
	m.openMarketplace()
	m.marketplaceTab = tab
	rows := m.marketplaceItems.Items(kind, "")
	for i, row := range rows {
		if row.Name == name {
			m.marketplaceCursor = i
			break
		}
	}
}

func (m *Model) loadSkillByName(name string) {
	for _, sk := range m.marketplaceItems.Items(marketplace.Skills, "") {
		if sk.Name == name {
			actual, err := skills.ParseFile(sk.Source, sk.Scope)
			if err != nil {
				// The row exists but its source won't load (permissions,
				// moved file): name the reason instead of claiming the
				// skill doesn't exist.
				m.append("✗ cannot load skill " + name + ": " + err.Error())
				return
			}
			m.loadSkill(actual)
			return
		}
	}
	m.append("✗ skill not found: " + name)
}

func (m Model) marketplaceView() string {
	rows := m.marketplaceItems.Items(marketplaceTabs[m.marketplaceTab], m.marketplaceQuery)
	var b strings.Builder
	width := m.vp.Width
	if width <= 0 {
		width = 80
	}
	b.WriteString(truncANSI("  Hooks   Plugins   Marketplace   Skills   MCP Servers", width))
	b.WriteByte('\n')
	b.WriteString(truncANSI("  "+strings.Repeat(" ", tabOffset(m.marketplaceTab))+"^", width))
	b.WriteByte('\n')
	b.WriteString(truncANSI("  / to search                                      Workspace ▾", width))
	b.WriteByte('\n')
	if m.marketplaceQuery != "" {
		b.WriteString(truncANSI("  /"+safeMarketplaceText(m.marketplaceQuery), width))
		b.WriteByte('\n')
	}
	for i, item := range rows {
		status := "[installed]"
		statusStyle := lipgloss.NewStyle().Foreground(success)
		if !item.Installed {
			status = "[install]"
			statusStyle = lipgloss.NewStyle().Foreground(accentSelect)
		}
		if item.Kind == marketplace.Plugins && item.Installed && !item.Enabled {
			status = "[disabled]"
			statusStyle = lipgloss.NewStyle().Foreground(amber)
		}
		if item.Verified {
			status = "[verified]"
		}
		if item.Collision {
			status = "[collision]"
			statusStyle = lipgloss.NewStyle().Foreground(amber)
		}
		label := safeMarketplaceText(item.Name)
		if item.Version != "" {
			label += " v" + safeMarketplaceText(item.Version)
		}
		detail := safeMarketplaceText(item.Scope)
		if detail == "" {
			detail = safeMarketplaceText(item.Source)
		}
		statusWidth := ansi.StringWidth(status)
		prefix := fmt.Sprintf("  %s ", map[bool]string{true: "›", false: " "}[i == m.marketplaceCursor])
		available := max(width-ansi.StringWidth(prefix)-statusWidth-3, 12)
		left := max(available*2/3, 8)
		right := max(available-left-1, 8)
		line := prefix + padRunesRight(truncANSI(label, left), left) + " " +
			padRunesRight(truncANSI("("+detail+")", right), right)
		line = line + strings.Repeat(" ", max(width-ansi.StringWidth(line)-statusWidth, 1)) + statusStyle.Render(status)
		if i == m.marketplaceCursor {
			line = lipgloss.NewStyle().Foreground(fgOnSelect).Background(accentSelect).Render(line)
		} else {
			line = lipgloss.NewStyle().Foreground(fgMuted).Render(line)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if len(rows) == 0 {
		b.WriteString("  no matching items")
		b.WriteByte('\n')
	}
	b.WriteString(truncANSI("  ←→ tabs · ↑↓ select · enter open · i install · type to filter · / clears · esc close", width))
	return b.String()
}

// titleMarketplaceAction capitalises the first rune for a confirm
// prompt header (e.g. "install" → "Install"). Marketplace actions are a
// fixed ASCII enum, so a single-rune title is exact and avoids the
// deprecated strings.Title.
func titleMarketplaceAction(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (m Model) marketplaceConfirmView() string {
	item := m.marketplacePending
	if item == nil {
		return m.marketplaceView()
	}
	var b strings.Builder
	action := string(m.marketplaceAction)
	if action == "" {
		action = string(marketplaceActionInstall)
	}
	width := m.vp.Width
	if width <= 0 {
		width = 80
	}
	b.WriteString(truncANSI(titleMarketplaceAction(action)+" marketplace item?", width))
	b.WriteString("\n\n")
	b.WriteString(marketplaceWrappedField("  ", safeMarketplaceText(item.Name)+" v"+safeMarketplaceText(item.Version), width))
	b.WriteByte('\n')
	b.WriteString(marketplaceWrappedField("  source: ", safeMarketplaceText(item.Source), width))
	b.WriteByte('\n')
	b.WriteString(marketplaceWrappedField("  scope: ", safeMarketplaceText(item.Scope), width))
	if item.Description != "" {
		b.WriteByte('\n')
		b.WriteString(marketplaceWrappedField("  ", safeMarketplaceText(item.Description), width))
	}
	if action == string(marketplaceActionInstall) || action == string(marketplaceActionUpdate) {
		b.WriteByte('\n')
		b.WriteString(marketplaceWrappedField("  ", "This changes the hash-pinned local plugin installation.", width))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(truncANSI("  y/enter "+action+" · n/esc cancel", width))
	return b.String()
}

func (m Model) marketplaceDetailView() string {
	item := m.marketplaceDetail
	if item == nil {
		return m.marketplaceView()
	}
	width := m.vp.Width
	if width <= 0 {
		width = 80
	}
	var b strings.Builder
	title := safeMarketplaceText(item.Name)
	if item.Version != "" {
		title += " v" + safeMarketplaceText(item.Version)
	}
	b.WriteString(truncANSI(title, width))
	b.WriteString("\n\n")
	b.WriteString(marketplaceWrappedField("kind: ", safeMarketplaceText(string(item.Kind)), width))
	b.WriteByte('\n')
	b.WriteString(marketplaceWrappedField("scope: ", safeMarketplaceText(item.Scope), width))
	b.WriteByte('\n')
	b.WriteString(marketplaceWrappedField("source: ", safeMarketplaceText(item.Source), width))
	b.WriteByte('\n')
	b.WriteString(truncANSI(fmt.Sprintf("installed: %t · verified: %t · enabled: %t", item.Installed, item.Verified, item.Enabled), width))
	if item.Description != "" {
		b.WriteString("\n\n")
		b.WriteString(marketplaceWrappedField("", safeMarketplaceText(item.Description), width))
	}
	for _, d := range item.Diagnostics {
		b.WriteString("\n\n")
		b.WriteString(marketplaceWrappedField("warning: ", safeMarketplaceText(d), width))
	}
	if len(item.Skills) > 0 {
		b.WriteString("\n\n")
		b.WriteString(marketplaceWrappedField("skills: ", safeMarketplaceText(strings.Join(item.Skills, ", ")), width))
	}
	b.WriteString("\n\n")
	b.WriteString(truncANSI("enter/esc back", width))
	if item.Kind == marketplace.Plugins && item.Installed {
		actions := " · e enable/disable · r remove"
		if item.UpdatePath != "" {
			actions += " · u update"
		}
		b.WriteString(truncANSI(actions, width))
	}
	if item.Kind == marketplace.Marketplace && item.Installable && !item.Installed {
		b.WriteString(" · i install")
	}
	return b.String()
}

// marketplaceWrappedField keeps detail and confirmation overlays inside the
// frame even when a remote catalog supplies a very long path, description, or
// diagnostic. Continuations are intentionally plain: this is metadata, not a
// table, and wrapping is safer than allowing centerFrame to widen the app.
func marketplaceWrappedField(label, value string, width int) string {
	if width < 1 {
		width = 1
	}
	return wrapLine(label+value, width)
}

func safeMarketplaceText(s string) string {
	s = ansi.Strip(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			if r == '\t' {
				b.WriteRune(' ')
			}
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func tabOffset(n int) int {
	widths := []int{0, 8, 17, 31, 40}
	if n < 0 || n >= len(widths) {
		return 0
	}
	return widths[n]
}
