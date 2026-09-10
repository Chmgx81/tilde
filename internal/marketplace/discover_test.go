package marketplace

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCatalogPlugin(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "name: " + name + "\nversion: 1.2.3\ndescription: reviewed local package\nskills:\n  - skills/review.md\n"
	if err := os.WriteFile(filepath.Join(dir, "tilde-plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "review.md"), []byte("---\nname: "+name+"-skill\ndescription: review\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDiscoverReadsCodexStyleJSONCatalog(t *testing.T) {
	root := t.TempDir()
	dir := writeCatalogPlugin(t, root, "browser-review")
	if err := os.MkdirAll(filepath.Join(root, ".agents", "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"name":"local","plugins":[{"name":"browser-review","version":"1.2.3","description":"browser checks","source":{"source":"local","path":"browser-review"}}]}`
	if err := os.WriteFile(filepath.Join(root, ".agents", "plugins", "marketplace.json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, errs := Discover(root, "", nil, nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected discovery errors: %v", errs)
	}
	items := reg.Items(Marketplace, "browser")
	if len(items) != 1 || items[0].Name != "browser-review" || items[0].InstallPath != dir || !items[0].Installable {
		t.Fatalf("unexpected marketplace item: %+v", items)
	}
}

func TestDiscoverRejectsCatalogPathEscape(t *testing.T) {
	root := t.TempDir()
	external := writeCatalogPlugin(t, t.TempDir(), "outside-plugin")
	if err := os.MkdirAll(filepath.Join(root, ".tilde", "marketplace"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := "items:\n  - name: outside-plugin\n    version: 1.2.3\n    source: " + external + "\n"
	if err := os.WriteFile(filepath.Join(root, ".tilde", "marketplace", "catalog.yaml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, errs := Discover(root, "", nil, nil)
	// Seed catalog entries (Scope bundled) are always present; the
	// malicious catalog item itself must contribute nothing.
	nonSeed := 0
	for _, it := range reg.Items(Marketplace, "") {
		if it.Scope != "bundled" {
			nonSeed++
		}
	}
	if nonSeed != 0 || len(errs) != 1 {
		t.Fatalf("catalog escape was not rejected: non-seed=%d errors=%v", nonSeed, errs)
	}
}

func TestSeedCatalogListsStarters(t *testing.T) {
	reg, errs := Discover(t.TempDir(), t.TempDir(), nil, nil)
	if len(errs) != 0 {
		t.Fatalf("seed discover must be error-free: %v", errs)
	}
	var names []string
	for _, it := range reg.Items(Marketplace, "") {
		if it.Scope != "bundled" {
			continue
		}
		names = append(names, it.Name)
		if !it.Installable || it.Installed {
			t.Errorf("seed entry %q must be installable and not installed", it.Name)
		}
		if it.InstallPath == "" {
			t.Errorf("seed entry %q needs an install path", it.Name)
		}
	}
	if len(names) < 2 {
		t.Fatalf("Marketplace tab needs seed entries, got %v", names)
	}
}
