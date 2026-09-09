package provider

import (
	"fmt"
	"sort"
	"strings"
)

// catalogPrice renders one entry's price exactly as stored: 0/0 is free,
// any negative leg is unreported pay-per-use (never free), otherwise the
// stored per-1M in/out pair. No currency math — display only.
func catalogPrice(in, out float64) string {
	if in < 0 || out < 0 {
		return "pay-per-use, see dashboard"
	}
	if in == 0 && out == 0 {
		return "free"
	}
	return fmt.Sprintf("$%.2f/$%.2f per 1M in/out", in, out)
}

// catalogWindow renders one entry's context window the same way the
// /model picker does: 0 means unreported, never a guess.
func catalogWindow(ctx int) string {
	if ctx <= 0 {
		return "window unreported"
	}
	return fmt.Sprintf("%dk ctx", ctx/1000)
}

// FormatCatalog renders the shipped model catalog as aligned
// `provider/model window price` rows plus a totals line. It is read-only
// (no network, catalog order preserved — Descriptions order, entries in
// slice order).
//
// Pass "" for every provider, or one provider id for that shelf only.
// An unknown id fails loud with the valid set — never a silent fallback
// (same discipline as Factory/selectProvider).
//
// Owner wiring (main.go, `tilde models [provider]`):
//
//	out, err := provider.FormatCatalog(providerArg)
//	if err != nil {
//		fmt.Fprintln(os.Stderr, "tilde: "+err.Error())
//		os.Exit(2)
//	}
//	fmt.Println(out)
func FormatCatalog(providerID string) (string, error) {
	descs := Descriptions
	if strings.TrimSpace(providerID) != "" {
		id := strings.ToLower(strings.TrimSpace(providerID))
		var match *ProviderDesc
		for i, d := range Descriptions {
			if d.ID == id {
				match = &Descriptions[i]
				break
			}
		}
		if match == nil {
			ids := make([]string, 0, len(Descriptions))
			for _, d := range Descriptions {
				ids = append(ids, d.ID)
			}
			sort.Strings(ids)
			return "", fmt.Errorf("unknown provider %q (use %s)", providerID, strings.Join(ids, "|"))
		}
		descs = []ProviderDesc{*match}
	}

	type row struct {
		ref   string
		win   string
		price string
	}
	var rows []row
	for _, d := range descs {
		for _, cm := range Catalog[d.ID] {
			rows = append(rows, row{
				ref:   d.ID + "/" + cm.ID,
				win:   catalogWindow(cm.Context),
				price: catalogPrice(cm.InCost, cm.OutCost),
			})
		}
	}

	modelW, winW := len("MODEL"), len("WINDOW")
	for _, r := range rows {
		modelW = max(modelW, len(r.ref))
		winW = max(winW, len(r.win))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-*s  %-*s  %s\n", modelW, "MODEL", winW, "WINDOW", "PRICE")
	for _, r := range rows {
		fmt.Fprintf(&b, "%-*s  %-*s  %s\n", modelW, r.ref, winW, r.win, r.price)
	}
	provs := len(descs)
	provWord := "providers"
	if provs == 1 {
		provWord = "provider"
	}
	fmt.Fprintf(&b, "%d %s, %d models", provs, provWord, len(rows))
	return b.String(), nil
}
