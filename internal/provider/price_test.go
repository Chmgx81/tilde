package provider

import "testing"

// Known shelf: exact stored figures, no new data.
func TestPriceForKnown(t *testing.T) {
	in, out, ok := PriceFor("openai", "gpt-5.2")
	if !ok {
		t.Fatal("openai/gpt-5.2 must be known")
	}
	if in != 1.25 || out != 10 {
		t.Fatalf("stored figures must pass through unchanged: got %v/%v", in, out)
	}
}

// Provider id match is case-insensitive (same as BudgetFor).
func TestPriceForProviderCaseInsensitive(t *testing.T) {
	_, _, ok := PriceFor("OpenAI", "gpt-5.2-mini")
	if !ok {
		t.Fatal("provider id match must be case-insensitive")
	}
}

// Free shelf: known-free (0/0 → ok), not unknown.
func TestPriceForFree(t *testing.T) {
	in, out, ok := PriceFor("ollama", "qwen3.8-4b:16k")
	if !ok {
		t.Fatal("free models must report ok=true (known-free, not unknown)")
	}
	if in != 0 || out != 0 {
		t.Fatalf("free figures must stay 0/0: got %v/%v", in, out)
	}
}

// Unknown model, unknown provider, and unreported pay-per-use
// (negative legs) all read as unknown — never fabricated.
func TestPriceForUnknown(t *testing.T) {
	for _, tc := range [][2]string{
		{"openai", "no-such-model"},
		{"nope", "gpt-5.2"},
		{"", ""},
		{"opencode", "kimi-k2.7-code"}, // negative legs: pay-per-use
	} {
		if _, _, ok := PriceFor(tc[0], tc[1]); ok {
			t.Fatalf("PriceFor(%q, %q) must be !ok", tc[0], tc[1])
		}
	}
}
