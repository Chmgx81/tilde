package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
