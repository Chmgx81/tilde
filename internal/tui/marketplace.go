package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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

func (m Model) updateMarketplace(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		if msg.String() == "i" && len(rows) > 0 && m.marketplaceCursor < len(rows) {
			item := rows[m.marketplaceCursor]
			if !item.Installable {
				m.append("✗ " + item.Name + " is not an installable local marketplace package")
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
			return m, nil
		}
		if msg.String() == "/" {
			m.marketplaceQuery = ""
		} else {
			m.marketplaceQuery += msg.String()
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
		if !item.Installed {
			status = "[install]"
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
		line := fmt.Sprintf("  %s %-28s (%s) %s", map[bool]string{true: "›", false: " "}[i == m.marketplaceCursor], label, detail, status)
		if m.vp.Width > 0 {
			line = truncANSI(line, m.vp.Width)
		}
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

func tabOffset(n int) int {
	widths := []int{0, 8, 17, 31, 40}
	if n < 0 || n >= len(widths) {
		return 0
	}
	return widths[n]
}
