package marketplace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"tilde/internal/plugin"
	"tilde/internal/skills"

	"gopkg.in/yaml.v3"
)

// Discover builds the unified local registry. Discovery is read-only and
// never starts MCP servers, runs hooks, or loads skill bodies.
func Discover(root, pluginDir string, ix *skills.Index, mcpNames []string) (Registry, []error) {
	var r Registry
	var errs []error
	for _, p := range []struct{ path, scope string }{
		{filepath.Join(root, ".tilde", "marketplace", "catalog.yaml"), "workspace"},
	} {
		loadCatalog(&r, &errs, p.path, p.scope, root)
	}
	if ix != nil {
		for _, sk := range ix.List() {
			r.Add(Item{Kind: Skills, Name: sk.Name, Version: sk.Version, Scope: sk.Scope,
				Description: sk.Description, Source: sk.Path, Installed: true, Verified: sk.Verified})
		}
	}
	if pluginDir != "" {
		entries, err := os.ReadDir(pluginDir)
		if err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("plugins: %w", err))
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(pluginDir, entry.Name())
			m, err := plugin.LoadManifest(dir)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			r.Add(Item{Kind: Plugins, Name: m.Name, Version: m.Version, Scope: "workspace",
				Description: m.Description, Source: dir, Installed: true, Verified: plugin.Verify(dir), Skills: append([]string(nil), m.Skills...)})
			for _, rel := range m.Skills {
				path := filepath.Join(dir, filepath.FromSlash(rel))
				sk, err := skills.ParseFile(path, "workspace")
				if err != nil {
					errs = append(errs, err)
					continue
				}
				r.Add(Item{Kind: Skills, Name: sk.Name, Version: sk.Version, Scope: "workspace", Description: sk.Description, Source: path, Installed: true, Verified: plugin.Verify(dir)})
			}
		}
	}
	for _, name := range mcpNames {
		r.Add(Item{Kind: MCPServers, Name: name, Scope: "workspace", Installed: true})
	}
	return r, errs
}

type catalog struct {
	Items []struct {
		Name        string `yaml:"name"`
		Version     string `yaml:"version"`
		Description string `yaml:"description"`
		Source      string `yaml:"source"`
	} `yaml:"items"`
}

func loadCatalog(r *Registry, errs *[]error, path, scope, projectRoot string) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		*errs = append(*errs, fmt.Errorf("marketplace catalog: %w", err))
		return
	}
	var c catalog
	if err := yaml.Unmarshal(data, &c); err != nil {
		*errs = append(*errs, fmt.Errorf("marketplace catalog %s: %w", path, err))
		return
	}
	for _, item := range c.Items {
		if item.Name == "" || item.Version == "" || item.Source == "" {
			*errs = append(*errs, fmt.Errorf("marketplace catalog %s: item requires name, version, and local source", path))
			continue
		}
		if strings.Contains(item.Source, "://") {
			*errs = append(*errs, fmt.Errorf("marketplace catalog %s: remote source %q refused; install local, reviewed packages", path, item.Source))
			continue
		}
		source := item.Source
		if !filepath.IsAbs(source) {
			source = filepath.Join(projectRoot, source)
		}
		m, err := plugin.LoadManifest(source)
		if err != nil {
			*errs = append(*errs, err)
			continue
		}
		if m.Name != item.Name || m.Version != item.Version {
			*errs = append(*errs, fmt.Errorf("marketplace catalog %s: source manifest identity does not match %s", path, item.Name))
			continue
		}
		r.Add(Item{Kind: Marketplace, Name: item.Name, Version: item.Version, Scope: scope, Description: item.Description, Source: item.Source, InstallPath: source, Installable: true, Skills: append([]string(nil), m.Skills...)})
	}
}
