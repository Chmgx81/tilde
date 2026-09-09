// Package marketplace provides the read-only registry model used by the
// plugins/marketplace picker. It deliberately does not install or execute
// anything; callers must perform an explicit, verified install.
package marketplace

import (
	"sort"
	"strings"
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
}

type Registry struct{ items []Item }

func (r *Registry) Add(item Item) {
	if r == nil || strings.TrimSpace(item.Name) == "" {
		return
	}
	for i, old := range r.items {
		if old.Kind != item.Kind || old.Name != item.Name {
			continue
		}
		// Installed and verified state outrank catalog metadata. This keeps
		// one row per underlying item and prevents duplicate skill/plugin rows.
		if item.Installed || item.Verified || (!old.Installed && !old.Verified) {
			r.items[i] = item
		}
		return
	}
	r.items = append(r.items, item)
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
