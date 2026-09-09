package tui

import (
	"strings"
	"testing"
	"time"

	"tilde/internal/mode"
)

// P7-C session cost meter: formatCost table — sub-cent precision ($%.4f)
// matters at 4B scale.
func TestFormatCost(t *testing.T) {
	cases := []struct {
		name      string
		in, out   int
		pin, pout float64
		want      string
	}{
		{"zero tokens", 0, 0, 1, 10, "$0.0000"},
		{"known-free stays honest zero", 1234, 5678, 0, 0, "$0.0000"},
		{"mixed legs", 1000, 500, 1, 10, "$0.0060"},
		{"sub-cent precision", 1500, 0, 1, 0, "$0.0015"},
		{"large 4B-scale", 4000000000, 0, 1, 0, "$4000.0000"},
		{"large mixed", 1234567, 765432, 1.25, 10, "$9.1975"},
		{"negative counts clamp", -5, -10, 1, 10, "$0.0000"},
	}
	for _, c := range cases {
		if got := formatCost(c.in, c.out, c.pin, c.pout); got != c.want {
			t.Errorf("%s: formatCost(%d,%d,%v,%v)=%q, want %q",
				c.name, c.in, c.out, c.pin, c.pout, got, c.want)
		}
	}
}

// Unknown price hides the readout entirely — no fabrication.
func TestBuildCostSuffixUnknownHidden(t *testing.T) {
	if got := buildCostSuffix(100000, 50000, 0, 0, false); got != "" {
		t.Fatalf("unknown price must hide the readout, got %q", got)
	}
	if got := buildCostSuffix(0, 0, 0, 0, false); got != "" {
		t.Fatalf("unknown price with zero tokens must still hide, got %q", got)
	}
}

// Known-free renders $0.0000 (honest: local = free), never hidden.
func TestBuildCostSuffixKnownFree(t *testing.T) {
	if got := buildCostSuffix(999, 999, 0, 0, true); got != " $0.0000" {
		t.Fatalf("known-free must show $0.0000, got %q", got)
	}
	if got := buildCostSuffix(1000, 500, 1, 10, true); got != " $0.0060" {
		t.Fatalf("known price must append with leading space, got %q", got)
	}
}

// Model-switch recompute: prices resolve from the model identity and
// refresh when it changes (cache on the struct, not per frame).
func TestRefreshCostOnModelSwitch(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "openai/gpt-4.1-mini", 32000)
	if !m.costOK || m.costIn != 0.4 || m.costOut != 1.6 {
		t.Fatalf("openai/gpt-4.1-mini: got (%v,%v,%v)", m.costIn, m.costOut, m.costOK)
	}
	m.model = "ollama/llama3.1:8b"
	m.refreshCost()
	if !m.costOK || m.costIn != 0 || m.costOut != 0 {
		t.Fatalf("ollama local must be known-free: got (%v,%v,%v)", m.costIn, m.costOut, m.costOK)
	}
	m.model = "opencode/glm-5.3" // negative = unreported pay-per-use
	m.refreshCost()
	if m.costOK {
		t.Fatal("unreported pay-per-use must be unknown (hidden)")
	}
	m.model = "nope/custom-xyz" // not in catalog
	m.refreshCost()
	if m.costOK {
		t.Fatal("off-catalog model must be unknown (hidden)")
	}
	m.model = "anthropic/claude-haiku-4-5"
	m.refreshCost()
	if !m.costOK || m.costIn != 1 || m.costOut != 5 {
		t.Fatalf("switch back must recompute: got (%v,%v,%v)", m.costIn, m.costOut, m.costOK)
	}
}

// Status bar: cost appends next to ctx, existing segments never reflow.
func TestStatusBarCostAppended(t *testing.T) {
	loop := newTestLoop()
	loop.TotPrompt, loop.TotCompletion = 1000, 500
	m := New(loop, mode.Plan, t.TempDir(), "openai/gpt-4.1-mini", 32000)
	// (1000*0.4 + 500*1.6)/1e6 = 0.0012
	m.ctx = "41% (13.1k/32k)"
	got := stripANSI(m.statusBar())
	for _, want := range []string{"openai/gpt-4.1-mini", "ctx 41%", " $0.0012"} {
		if !strings.Contains(got, want) {
			t.Fatalf("bar must carry %q (append, never reflow): %q", want, got)
		}
	}
	if !strings.HasSuffix(got, " $0.0012") {
		t.Fatalf("cost must append at the end, got %q", got)
	}
}

// Status bar: unknown price hides the readout (no "$" anywhere).
func TestStatusBarCostHiddenWhenUnknown(t *testing.T) {
	loop := newTestLoop()
	loop.TotPrompt, loop.TotCompletion = 100000, 50000
	m := New(loop, mode.Plan, t.TempDir(), "opencode/glm-5.3", 32000)
	m.ctx = "41% (13.1k/32k)"
	if got := stripANSI(m.statusBar()); strings.Contains(got, "$") {
		t.Fatalf("unknown price must hide the readout, got %q", got)
	}
}

// Status bar: known-free local shows $0.0000, honestly.
func TestStatusBarCostKnownFree(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/llama3.1:8b", 32000)
	m.ctx = "10% (1.6k/16k)"
	if got := stripANSI(m.statusBar()); !strings.Contains(got, " $0.0000") {
		t.Fatalf("known-free must show $0.0000, got %q", got)
	}
}

// Status bar: mid-turn Working indicator is never reflowed by cost.
func TestStatusBarCostHiddenWhileRunning(t *testing.T) {
	loop := newTestLoop()
	loop.TotPrompt, loop.TotCompletion = 1000, 500
	m := New(loop, mode.Plan, t.TempDir(), "openai/gpt-4.1-mini", 32000)
	m.ctx = "41% (13.1k/32k)"
	m.turnStart = time.Now().Add(-30 * time.Second)
	if got := stripANSI(m.statusBar()); strings.Contains(got, "$") {
		t.Fatalf("running turn must not carry cost, got %q", got)
	}
}
