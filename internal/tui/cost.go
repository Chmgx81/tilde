package tui

import (
	"fmt"

	"tilde/internal/provider"
)

// Session cost meter (docs/ai-agents-and-terminal-coding-agents-2026.md
// §§5, 14): display-only. The status bar appends a compact ` $0.0123`
// readout next to the context % — (in*pin + out*pout)/1e6 from live loop
// totals × PriceFor. Known-free (0/0 → ok) renders `$0.0000` (local =
// free, honestly); unknown price (no catalog hit, or unreported
// pay-per-use) renders "" — never fabricated. Prices resolve once per
// model switch into the cached fields below, never per frame.

// refreshCost re-resolves the cached per-1M USD prices from the current
// m.model identity ("provider/model"). Call after every m.model write
// (New, /model paths). Unknown identity clears the cache (cost hidden).
func (m *Model) refreshCost() {
	if pid, mid, ok := provider.ParseModelRef(m.model); ok {
		m.costIn, m.costOut, m.costOK = provider.PriceFor(pid, mid)
		return
	}
	m.costIn, m.costOut, m.costOK = 0, 0, false
}

// formatCost renders session cost at sub-cent precision ($%.4f —
// sub-cent precision matters at 4B scale). Negative counts clamp to 0;
// prices come from the cache (see refreshCost), never guessed here.
func formatCost(in, out int, pin, pout float64) string {
	if in < 0 {
		in = 0
	}
	if out < 0 {
		out = 0
	}
	return fmt.Sprintf("$%.4f", (float64(in)*pin+float64(out)*pout)/1e6)
}

// buildCostSuffix is the pure bar-string builder: "" when the price is
// unknown (no fabrication), otherwise " $0.0123" (leading space — the
// caller appends, never reflows existing segments). Known-free yields
// " $0.0000".
func buildCostSuffix(in, out int, pin, pout float64, ok bool) string {
	if !ok {
		return ""
	}
	return " " + formatCost(in, out, pin, pout)
}

// costSuffix reads live loop totals against the cached prices. Nil loop
// (tests, pre-run) means zero tokens — still "$0.0000" when known-free,
// hidden when unknown.
func (m *Model) costSuffix() string {
	if !m.costOK {
		return ""
	}
	in, out := 0, 0
	if m.loop != nil {
		// Live reads race Run's writes only via atomics; int() narrows
		// the owned receipt back to a display width.
		in, out = int(m.loop.TotPrompt.Load()), int(m.loop.TotCompletion.Load())
	}
	return buildCostSuffix(in, out, m.costIn, m.costOut, true)
}
