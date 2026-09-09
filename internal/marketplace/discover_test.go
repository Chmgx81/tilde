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
	if reg.Len() != 0 || len(errs) != 1 {
		t.Fatalf("catalog escape was not rejected: items=%d errors=%v", reg.Len(), errs)
	}
}
