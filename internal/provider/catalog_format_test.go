package provider

import (
	"strings"
	"testing"
)

// Full table: every shipped provider appears, window/price columns exist.
func TestFormatCatalogAllProviders(t *testing.T) {
	out, err := FormatCatalog("")
	if err != nil {
		t.Fatalf("FormatCatalog(\"\"): %v", err)
	}
	for _, id := range []string{"ollama", "openai", "anthropic", "openrouter", "gemini", "opencode"} {
		if !strings.Contains(out, id+"/") {
			t.Errorf("table must contain provider %q:\n%s", id, out)
		}
	}
	if !strings.Contains(strings.ToUpper(out), "WINDOW") {
		t.Errorf("table must have a window column:\n%s", out)
	}
	if !strings.Contains(strings.ToUpper(out), "PRICE") {
		t.Errorf("table must have a price column:\n%s", out)
	}
	if !strings.Contains(out, "6 providers") {
		t.Errorf("totals line must count 6 providers:\n%s", out)
	}
	if !strings.Contains(out, "models") {
		t.Errorf("totals line must count models:\n%s", out)
	}
}

// Single shelf: only that provider's rows, catalog order preserved.
func TestFormatCatalogSingleProvider(t *testing.T) {
	out, err := FormatCatalog("openai")
	if err != nil {
		t.Fatalf("FormatCatalog(openai): %v", err)
	}
	for _, id := range CatalogIDs("openai") {
		if !strings.Contains(out, "openai/"+id) {
			t.Errorf("missing openai/%s:\n%s", id, out)
		}
	}
	for _, other := range []string{"ollama/", "anthropic/", "gemini/"} {
		if strings.Contains(out, other) {
			t.Errorf("single-provider table must not contain %q:\n%s", other, out)
		}
	}
	if !strings.Contains(out, "1 provider") {
		t.Errorf("totals line must count 1 provider:\n%s", out)
	}
}

// Price rendering: stored values only — free stays free, negatives stay
// pay-per-use, stored pairs render as-is.
func TestFormatCatalogPricesAsStored(t *testing.T) {
	out, err := FormatCatalog("")
	if err != nil {
		t.Fatalf("FormatCatalog(\"\"): %v", err)
	}
	if !strings.Contains(out, "free") {
		t.Errorf("free rows must render as free:\n%s", out)
	}
	if !strings.Contains(out, "pay-per-use, see dashboard") {
		t.Errorf("negative-price rows must render pay-per-use, never free:\n%s", out)
	}
	if !strings.Contains(out, "$1.25/$10.00 per 1M in/out") {
		t.Errorf("stored openai pair must render exactly:\n%s", out)
	}
	if !strings.Contains(out, "window unreported") {
		t.Errorf("zero-context rows must render unreported:\n%s", out)
	}
}

// Unknown provider fails loud and names the valid set — never a silent
// fallback (Factory/selectProvider discipline).
func TestFormatCatalogUnknownProvider(t *testing.T) {
	if _, err := FormatCatalog("nope"); err == nil {
		t.Fatal("unknown provider must fail, not fall back")
	} else {
		for _, id := range []string{"ollama", "openai", "anthropic", "openrouter", "gemini", "opencode"} {
			if !strings.Contains(err.Error(), id) {
				t.Errorf("error must name valid provider %q: %v", id, err)
			}
		}
	}
}

// Model ids render in catalog order (no reordering of shipped data).
func TestCatalogOrderPreserved(t *testing.T) {
	out, err := FormatCatalog("openai")
	if err != nil {
		t.Fatalf("FormatCatalog(openai): %v", err)
	}
	ids := CatalogIDs("openai")
	prev := -1
	for _, id := range ids {
		i := strings.Index(out, "openai/"+id)
		if i < 0 {
			t.Fatalf("missing openai/%s", id)
		}
		if i < prev {
			t.Fatalf("catalog order violated at openai/%s", id)
		}
		prev = i
	}
}
