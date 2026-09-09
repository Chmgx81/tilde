package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"tilde/internal/marketplace"
	"tilde/internal/mode"
)

func TestMarketplaceBrowserHasUnifiedTabsAndDeduplicatedSkills(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".tilde", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: local-review\ndescription: Review local changes safely\n---\nBody\n"
	if err := os.WriteFile(filepath.Join(dir, "local-review.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(newTestLoop(), mode.Plan, root, "ollama/m", 32000)
	m.openMarketplace()
	if !m.marketplaceOpen || len(marketplaceTabs) != 5 {
		t.Fatalf("marketplace not open: %+v", marketplaceTabs)
	}
	m.marketplaceTab = 3
	rows := m.marketplaceItems.Items(marketplace.Skills, "local-review")
	if len(rows) != 1 || rows[0].Name != "local-review" {
		t.Fatalf("unexpected skill rows: %+v", rows)
	}
	if strings.Contains(m.marketplaceView(), "local-review local-review") {
		t.Fatal("duplicate registry row")
	}
}

func TestMarketplaceBrowserDiscoversConfiguredHooksWithoutExecutingThem(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".tilde"), 0o755); err != nil {
		t.Fatal(err)
	}
	hooks := "before:\n  write_file:\n    - echo guard\nafter:\n  write_file:\n    - echo observe\n"
	if err := os.WriteFile(filepath.Join(root, ".tilde", "hooks.yaml"), []byte(hooks), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(newTestLoop(), mode.Plan, root, "ollama/m", 32000)
	m.openMarketplace()
	rows := m.marketplaceItems.Items(marketplace.Hooks, "project/")
	if len(rows) != 2 {
		t.Fatalf("expected two configured hook rows, got %+v", rows)
	}
	for _, row := range rows {
		if row.Source != filepath.Join(root, ".tilde", "hooks.yaml") || !row.Installed {
			t.Fatalf("unexpected hook row: %+v", row)
		}
	}
}

func TestMarketplaceInstallRequiresConfirmation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "browser-review")
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tilde-plugin.yaml"), []byte("name: browser-review\nversion: 1.0.0\ndescription: browser review\nskills:\n  - skills/review.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "review.md"), []byte("---\nname: review\ndescription: review\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".tilde", "marketplace"), 0o755); err != nil {
		t.Fatal(err)
	}
	catalog := "items:\n  - name: browser-review\n    version: 1.0.0\n    source: " + dir + "\n"
	if err := os.WriteFile(filepath.Join(root, ".tilde", "marketplace", "catalog.yaml"), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(newTestLoop(), mode.Plan, root, "ollama/m", 32000)
	m.openMarketplace()
	m.marketplaceTab = 2
	_, _ = m.updateMarketplace(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	if m.marketplacePending == nil {
		t.Fatal("install action bypassed confirmation")
	}
}
