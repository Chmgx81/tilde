// Package marketplace provides the read-only registry model used by the
// plugins/marketplace picker. It deliberately does not install or execute
// anything; callers must perform an explicit, verified install.
package marketplace

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"tilde/internal/plugin"
)

type Kind string

const (
	Hooks       Kind = "Hooks"
	Plugins     Kind = "Plugins"
	Marketplace Kind = "Marketplace"
	Skills      Kind = "Skills"
	MCPServers  Kind = "MCP Servers"
)

var Kinds = []Kind{Hooks, Plugins, Marketplace, Skills, MCPServers}

// Item is a registry row. InstallPath is intentionally opaque to the UI and
// is only used after an explicit install action by the owner.
type Item struct {
	Kind        Kind
	Name        string
	Version     string
	Scope       string // bundled, project, user, workspace, marketplace
	Description string
	Source      string
	InstallPath string
	Installed   bool
	Verified    bool
	Installable bool
	Skills      []string
	Agents      []string
	Enabled     bool
	Remote      bool
	Collision   bool
	Diagnostics []string
	Provenance  []Provenance
	UpdatePath  string
}

// Provenance records one source that contributed metadata to a registry row.
// Catalog is the file that declared the source; InstallPath is kept separate
// from Source because catalogs may use a display URL or a relative path.
type Provenance struct {
	Catalog     string
	Scope       string
	Source      string
	InstallPath string
	Version     string
	Installed   bool
	Verified    bool
	Installable bool
	Remote      bool
}

type Registry struct{ items []Item }

func (r *Registry) Add(item Item) {
	if r == nil || strings.TrimSpace(item.Name) == "" {
		return
	}
	item = normalizeItem(item)
	for i, old := range r.items {
		if old.Kind != item.Kind || old.Name != item.Name {
			continue
		}
		merged := mergeProvenance(old.Provenance, item.Provenance)
		collision := old.Collision || item.Collision || provenanceCollision(merged)
		winner := old
		// Installed and verified state outrank catalog metadata. Keep one row
		// for the browser, but never throw away the losing origin.
		if itemPriority(item) > itemPriority(old) {
			winner = item
		}
		winner.Provenance = merged
		winner.Collision = collision
		winner.Diagnostics = mergeDiagnostics(old.Diagnostics, item.Diagnostics)
		if collision {
			winner.Diagnostics = mergeDiagnostics(winner.Diagnostics, []string{collisionDiagnostic(winner.Name, merged)})
		}
		winner.UpdatePath = updatePath(merged)
		r.items[i] = winner
		return
	}
	r.items = append(r.items, item)
}

func normalizeItem(item Item) Item {
	// embedded://plugin sources ship inside the binary: local and reviewed,
	// not remote — they install through the standard validated path.
	if item.Remote || (strings.Contains(item.Source, "://") && !strings.HasPrefix(item.Source, plugin.EmbeddedScheme)) {
		item.Remote = true
		item.Installable = false
		item.Diagnostics = mergeDiagnostics(item.Diagnostics, []string{"remote source is metadata-only; remote installation is disabled"})
	}
	if len(item.Provenance) == 0 {
		item.Provenance = []Provenance{{
			Scope: item.Scope, Source: item.Source, InstallPath: item.InstallPath,
			Version: item.Version, Installed: item.Installed, Verified: item.Verified,
			Installable: item.Installable, Remote: item.Remote,
		}}
	}
	item.UpdatePath = updatePath(item.Provenance)
	return item
}

func itemPriority(item Item) int {
	if item.Installed && item.Verified {
		return 4
	}
	if item.Installed {
		return 3
	}
	if item.Installable {
		return 2
	}
	return 1
}

func provenanceKey(p Provenance) string {
	return strings.Join([]string{p.Scope, p.Source, p.InstallPath, p.Version}, "\x00")
}

func mergeProvenance(a, b []Provenance) []Provenance {
	out := make([]Provenance, 0, len(a)+len(b))
	seen := map[string]bool{}
	for _, list := range [][]Provenance{a, b} {
		for _, p := range list {
			key := provenanceKey(p)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Version < out[j].Version
	})
	return out
}

func provenanceIdentity(p Provenance) string {
	if p.InstallPath != "" {
		if abs, err := filepath.Abs(p.InstallPath); err == nil {
			return filepath.Clean(abs)
		}
	}
	return p.Source
}

func provenanceCollision(list []Provenance) bool {
	if len(list) < 2 {
		return false
	}
	first := list[0]
	identity, version := provenanceIdentity(first), first.Version
	for _, p := range list[1:] {
		if provenanceIdentity(p) != identity || p.Version != version {
			return true
		}
	}
	return false
}

func collisionDiagnostic(name string, list []Provenance) string {
	parts := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, p := range list {
		value := p.Source
		if value == "" {
			value = p.InstallPath
		}
		if p.Version != "" {
			value += " @ " + p.Version
		}
		if value != "" && !seen[value] {
			seen[value] = true
			parts = append(parts, value)
		}
	}
	return fmt.Sprintf("collision: %s has multiple source identities (%s)", name, strings.Join(parts, "; "))
}

func mergeDiagnostics(a, b []string) []string {
	out := append([]string(nil), a...)
	seen := map[string]bool{}
	for _, value := range out {
		seen[value] = true
	}
	for _, value := range b {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func updatePath(list []Provenance) string {
	for _, p := range list {
		if p.Installable && !p.Installed && !p.Remote && p.InstallPath != "" {
			return p.InstallPath
		}
	}
	return ""
}

func (r Registry) Items(kind Kind, query string) []Item {
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]Item, 0, len(r.items))
	for _, item := range r.items {
		if kind != "" && item.Kind != kind {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(item.Name+" "+item.Description+" "+item.Source), q) {
			continue
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version < out[j].Version
	})
	return out
}

func (r Registry) Len() int { return len(r.items) }
