package marketplace

import "testing"

func TestRegistryDeduplicatesByKindAndName(t *testing.T) {
	var r Registry
	r.Add(Item{Kind: Skills, Name: "browser-review", Scope: "marketplace"})
	r.Add(Item{Kind: Skills, Name: "browser-review", Scope: "workspace", Installed: true, Verified: true})
	got := r.Items(Skills, "")
	if len(got) != 1 || !got[0].Installed || !got[0].Verified || got[0].Scope != "workspace" {
		t.Fatalf("dedupe did not preserve installed item: %+v", got)
	}
}

func TestRegistryFiltersTabsAndQueries(t *testing.T) {
	var r Registry
	r.Add(Item{Kind: Plugins, Name: "browser-review", Description: "review browser flows"})
	r.Add(Item{Kind: Skills, Name: "tui-design", Description: "terminal interface design"})
	if got := r.Items(Plugins, "browser"); len(got) != 1 || got[0].Name != "browser-review" {
		t.Fatalf("unexpected plugin filter: %+v", got)
	}
	if got := r.Items(Skills, "browser"); len(got) != 0 {
		t.Fatalf("query leaked across tabs: %+v", got)
	}
}
