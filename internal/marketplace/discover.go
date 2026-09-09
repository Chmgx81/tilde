package marketplace

import (
	"encoding/json"
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
	for _, p := range []struct {
		path, scope string
		json        bool
	}{
		{filepath.Join(root, ".tilde", "marketplace", "catalog.yaml"), "workspace", false},
		{filepath.Join(root, ".tilde", "marketplace", "catalog.json"), "workspace", true},
		{filepath.Join(root, ".agents", "plugins", "marketplace.json"), "workspace", true},
		{filepath.Join(root, ".claude-plugin", "marketplace.json"), "workspace", true},
		{filepath.Join(root, ".cursor-plugin", "marketplace.json"), "workspace", true},
	} {
		if p.json {
			loadJSONCatalog(&r, &errs, p.path, p.scope, root)
		} else {
			loadCatalog(&r, &errs, p.path, p.scope, root)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		loadCatalog(&r, &errs, filepath.Join(home, ".tilde", "marketplace", "catalog.yaml"), "user", home)
		loadJSONCatalog(&r, &errs, filepath.Join(home, ".agents", "plugins", "marketplace.json"), "user", home)
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

type jsonCatalog struct {
	Name    string `json:"name"`
	Plugins []struct {
		Name        string          `json:"name"`
		Version     string          `json:"version"`
		Description string          `json:"description"`
		Keywords    []string        `json:"keywords"`
		Source      json.RawMessage `json:"source"`
		Policy      string          `json:"policy"`
	} `json:"plugins"`
}

func loadJSONCatalog(r *Registry, errs *[]error, path, scope, base string) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		*errs = append(*errs, fmt.Errorf("marketplace catalog: %w", err))
		return
	}
	var c jsonCatalog
	if err := json.Unmarshal(data, &c); err != nil {
		*errs = append(*errs, fmt.Errorf("marketplace catalog %s: %w", path, err))
		return
	}
	for _, item := range c.Plugins {
		if item.Name == "" {
			*errs = append(*errs, fmt.Errorf("marketplace catalog %s: plugin requires name", path))
			continue
		}
		source, localPath := jsonSource(item.Source, base)
		installable := localPath != "" && item.Policy != "NOT_AVAILABLE"
		if localPath != "" {
			if !catalogPathAllowed(base, localPath) {
				*errs = append(*errs, fmt.Errorf("marketplace catalog %s: local source escapes catalog root: %s", path, localPath))
				continue
			}
			m, err := plugin.LoadManifest(localPath)
			if err != nil {
				*errs = append(*errs, err)
				continue
			}
			if m.Name != item.Name || (item.Version != "" && m.Version != item.Version) {
				*errs = append(*errs, fmt.Errorf("marketplace catalog %s: source manifest identity does not match %s", path, item.Name))
				continue
			}
			if item.Version == "" {
				item.Version = m.Version
			}
		}
		if item.Version == "" {
			item.Version = "catalog"
		}
		r.Add(Item{Kind: Marketplace, Name: item.Name, Version: item.Version, Scope: scope, Description: item.Description, Source: source, InstallPath: localPath, Installable: installable, Skills: nil})
	}
}

// catalogPathAllowed prevents a catalog from turning a discovery-only read
// into an install of an arbitrary path. Symlink resolution is included so a
// path that lexically sits under the catalog root cannot escape through a
// linked directory.
func catalogPathAllowed(base, candidate string) bool {
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	baseReal, err := filepath.EvalSymlinks(baseAbs)
	if err != nil {
		return false
	}
	candidateReal, err := filepath.EvalSymlinks(candidateAbs)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(baseReal, candidateReal)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "."
}

func jsonSource(raw json.RawMessage, base string) (display, localPath string) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		display = s
		if !strings.Contains(s, "://") && s != "" {
			localPath = s
			if !filepath.IsAbs(localPath) {
				localPath = filepath.Join(base, localPath)
			}
		}
		return
	}
	var obj struct {
		Source string `json:"source"`
		Path   string `json:"path"`
		URL    string `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		display = obj.URL
		if display == "" {
			display = obj.Source
		}
		if display == "local" {
			display = obj.Path
		}
		if obj.Source == "local" || (obj.Path != "" && obj.URL == "") {
			localPath = obj.Path
			if !filepath.IsAbs(localPath) {
				localPath = filepath.Join(base, localPath)
			}
		}
	}
	return
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
		if !catalogPathAllowed(projectRoot, source) {
			*errs = append(*errs, fmt.Errorf("marketplace catalog %s: local source escapes catalog root: %s", path, item.Source))
			continue
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
