package plugin

// Bundled starter plugins shipped inside the tilde binary (like bundled
// skills): tiny, reviewed, offline-installable. They back the Marketplace
// tab's seed catalog so a fresh machine has something to install with one
// keypress — not an empty shelf.
//
// Sources use the embedded://plugin/<name> scheme. Install and DryRun
// materialize the embedded tree to a temp dir first, so validation,
// staging, lockfile pinning, and rollback all run the exact same code as
// directory installs. Nothing here executes on load.

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed bundled/*
var bundledFS embed.FS

// EmbeddedScheme marks a bundled-plugin source.
const EmbeddedScheme = "embedded://plugin/"

// BundledNames lists starter plugins baked into the binary, sorted.
func BundledNames() []string {
	entries, err := bundledFS.ReadDir("bundled")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// BundledManifest loads and validates one starter plugin's manifest.
// It materializes the embedded tree and runs the standard LoadManifest
// (parse + validate), so bundled plugins face the same checks as
// directory installs.
func BundledManifest(name string) (*Manifest, error) {
	dir, err := materializeBundled(name)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	return LoadManifest(dir)
}

// materializeBundled copies one starter plugin to a temp dir for the
// standard directory-install path. Caller removes the dir.
func materializeBundled(name string) (string, error) {
	if !nameRe.MatchString(name) || strings.Contains(name, "/") {
		return "", fmt.Errorf("plugin: bad bundled name %q", name)
	}
	if _, err := fs.Stat(bundledFS, "bundled/"+name+"/"+ManifestFileName); err != nil {
		return "", fmt.Errorf("plugin: no bundled plugin %q", name)
	}
	dir, err := os.MkdirTemp("", "tilde-bundled-plugin-*")
	if err != nil {
		return "", err
	}
	root := "bundled/" + name
	err = fs.WalkDir(bundledFS, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := bundledFS.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// resolveSrc maps an embedded://plugin/<name> source to a temp dir holding
// the same tree, or returns srcDir unchanged for plain paths. When staged
// is true the caller must remove the returned dir.
func resolveSrc(srcDir string) (dir string, staged bool, err error) {
	name, ok := strings.CutPrefix(srcDir, EmbeddedScheme)
	if !ok {
		return srcDir, false, nil
	}
	dir, err = materializeBundled(name)
	if err != nil {
		return "", false, err
	}
	return dir, true, nil
}
