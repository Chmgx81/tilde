package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"tilde/internal/marketplace"
	"tilde/internal/mcp"
	"tilde/internal/plugin"
	"tilde/internal/skills"
)

var marketplaceTabs = marketplace.Kinds

func (m *Model) openMarketplace() {
	if m.running {
		m.append("✗ an agent turn is running — Esc twice, then /plugins.")
		return
	}
	ix, err := skills.Scan(m.root)
	if err != nil {
		m.append("✗ " + err.Error())
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
	// Hooks are registry metadata only. They are never executed by opening
	// this browser, which keeps discovery outside the action boundary.
	for _, dir := range []struct{ path, scope string }{
		{filepath.Join(home, ".tilde", "hooks"), "user"},
		{filepath.Join(m.root, ".tilde", "hooks"), "project"},
	} {
		entries, _ := os.ReadDir(dir.path)
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml") {
				continue
			}
			reg.Add(marketplace.Item{Kind: marketplace.Hooks, Name: strings.TrimSuffix(strings.TrimSuffix(entry.Name(), ".yaml"), ".yml"), Scope: dir.scope, Source: filepath.Join(dir.path, entry.Name()), Installed: true})
		}
	}
	m.marketplaceItems = reg
	m.marketplaceCursor, m.marketplaceQuery = 0, ""
	m.marketplaceTab = 0
	m.marketplaceOpen = true
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
			if !item.Installable {
				m.append("✗ " + item.Name + " is not an installable local marketplace package")
				return m, nil
			}
			copy := item
			m.marketplacePending = &copy
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
			} else if item.Kind == marketplace.Plugins {
				m.append(fmt.Sprintf("● %s v%s — skills: %s", item.Name, item.Version, strings.Join(item.Skills, ", ")))
			}
		}
		return m, nil
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
		return m, nil
	case tea.KeyEnter:
		return m.installMarketplacePending()
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyRunes:
		switch strings.ToLower(msg.String()) {
		case "y":
			return m.installMarketplacePending()
		case "n":
			m.marketplacePending = nil
			return m, nil
		}
	}
	return m, nil
}

func (m *Model) installMarketplacePending() (tea.Model, tea.Cmd) {
	item := m.marketplacePending
	m.marketplacePending = nil
	if item == nil {
		return m, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		m.append("✗ cannot locate plugin home: " + err.Error())
		return m, nil
	}
	if _, err := plugin.Install(item.InstallPath, filepath.Join(home, ".tilde", "plugins")); err != nil {
		m.append("✗ plugin install failed: " + err.Error())
		return m, nil
	}
	m.append("✓ Installed " + item.Name + " v" + item.Version + " — reopen /plugins to inspect its skills")
	m.openMarketplace()
	// Land on the installed package's skill list so the install action has a
	// useful, deterministic next step instead of returning to the catalog.
	m.marketplaceTab = 3 // Skills
	for _, sk := range m.marketplaceItems.Items(marketplace.Skills, "") {
		if strings.Contains(filepath.Clean(sk.Source), filepath.Clean(item.Name)) {
			m.marketplaceQuery = sk.Name
			break
		}
	}
	return m, nil
}

func (m *Model) loadSkillByName(name string) {
	for _, sk := range m.marketplaceItems.Items(marketplace.Skills, "") {
		if sk.Name == name {
			if actual, err := skills.ParseFile(sk.Source, sk.Scope); err == nil {
				m.loadSkill(actual)
				return
			}
		}
	}
	m.append("✗ skill not found: " + name)
}

func (m Model) marketplaceView() string {
	rows := m.marketplaceItems.Items(marketplaceTabs[m.marketplaceTab], m.marketplaceQuery)
	var b strings.Builder
	b.WriteString("  Hooks   Plugins   Marketplace   Skills   MCP Servers\n")
	b.WriteString("  " + strings.Repeat(" ", tabOffset(m.marketplaceTab)) + "^\n")
	b.WriteString("  / to search                                      Workspace ▾\n")
	if m.marketplaceQuery != "" {
		fmt.Fprintf(&b, "  /%s\n", m.marketplaceQuery)
	}
	for i, item := range rows {
		status := "[installed]"
		statusStyle := lipgloss.NewStyle().Foreground(success)
		if !item.Installed {
			status = "[install]"
			statusStyle = lipgloss.NewStyle().Foreground(accentSelect)
		}
		if item.Verified {
			status = "[verified]"
		}
		label := item.Name
		if item.Version != "" {
			label += " v" + item.Version
		}
		detail := item.Scope
		if detail == "" {
			detail = item.Source
		}
		width := m.vp.Width
		if width <= 0 {
			width = 80
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
			line = lipgloss.NewStyle().Background(accentSelect).Render(line)
		} else {
			line = lipgloss.NewStyle().Foreground(fgMuted).Render(line)
		}
		b.WriteString(line + "\n")
	}
	if len(rows) == 0 {
		b.WriteString("  no matching items\n")
	}
	b.WriteString("  ←→ tabs · ↑↓ select · enter open · / search · esc close")
	return b.String()
}

func (m Model) marketplaceConfirmView() string {
	item := m.marketplacePending
	if item == nil {
		return m.marketplaceView()
	}
	var b strings.Builder
	b.WriteString("Install marketplace package?\n\n")
	fmt.Fprintf(&b, "  %s v%s\n", item.Name, item.Version)
	fmt.Fprintf(&b, "  source: %s\n", item.Source)
	fmt.Fprintf(&b, "  scope: %s\n", item.Scope)
	if item.Description != "" {
		fmt.Fprintf(&b, "  %s\n", item.Description)
	}
	b.WriteString("\n  This copies a reviewed local package into ~/.tilde/plugins.\n")
	b.WriteString("  y/enter install · n/esc cancel")
	return b.String()
}

func tabOffset(n int) int {
	widths := []int{0, 8, 17, 31, 40}
	if n < 0 || n >= len(widths) {
		return 0
	}
	return widths[n]
}
