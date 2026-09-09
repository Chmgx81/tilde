package eval

import (
	"context"
	"math"
	"testing"

	"tilde/internal/agent"
	"tilde/internal/mode"
	"tilde/internal/provider"
	"tilde/internal/tools"
)

// usageFactory returns a LoopFactory replaying one fixed usage-carrying
// reply per trial (no model): every Chat reports the same prompt/comp
// totals, so per-trial loop.TotPrompt/TotCompletion are deterministic.
func usageFactory(prompt, completion int) LoopFactory {
	return func(root string) *agent.Loop {
		reg := tools.NewRegistry()
		reg.Register(&tools.ReadFile{Root: root})
		reg.Register(&tools.WriteFile{Root: root})
		reg.Register(&tools.Shell{Root: root})
		script := []provider.Response{{
			Content: "done",
			Usage:   provider.Usage{Prompt: prompt, Completion: completion},
		}}
		return &agent.Loop{
			Prov: &scriptProv{script: script},
			Reg:  reg,
			Cfg:  agent.Config{MaxIters: 6, DoomRepeats: 5, Root: root, Mode: mode.Build},
		}
	}
}

func alwaysPassTask() Task {
	return Task{
		Name: "t", Goal: "g", MaxIters: 6,
		Verify: func(_, _ string, _ []agent.Event) (bool, string) {
			return true, "ok"
		},
	}
}

// Spec example: 1000 in @ $1 + 500 out @ $10 = $0.006.
func TestCostMath(t *testing.T) {
	got := CostUSD(1000, 500, 1, 10)
	if math.Abs(got-0.006) > 1e-12 {
		t.Fatalf("CostUSD(1000, 500, 1, 10) = %v, want 0.006", got)
	}
}

func TestCostZeroTokens(t *testing.T) {
	if got := CostUSD(0, 0, 1.25, 10); got != 0 {
		t.Fatalf("zero tokens must cost zero: got %v", got)
	}
	if got := CostUSD(1000, 500, 0, 0); got != 0 {
		t.Fatalf("free prices must cost zero: got %v", got)
	}
}

// Unknown price → zero, never fabricated, even with real token counts.
func TestCostZeroWhenUnknown(t *testing.T) {
	r := &Runner{
		Tasks:      []Task{alwaysPassTask()},
		Trials:     2,
		NewLoop:    usageFactory(1000, 500),
		ProviderID: "nope",
		Model:      "nope",
	}
	reps, err := r.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if reps[0].Passes != 2 {
		t.Fatalf("all trials must pass: %+v", reps[0])
	}
	if reps[0].MedianIn == 0 || reps[0].MedianOut == 0 {
		t.Fatalf("tokens must be tracked even when price is unknown: %+v", reps[0])
	}
	if reps[0].MedianCostUSD != 0 {
		t.Fatalf("unknown price must report zero cost, never a guess: %+v", reps[0])
	}
}

// No provider/model on the runner → zero cost (unwired, not unknown data).
func TestCostZeroWhenUnwired(t *testing.T) {
	r := &Runner{
		Tasks:   []Task{alwaysPassTask()},
		Trials:  1,
		NewLoop: usageFactory(1000, 500),
	}
	reps, err := r.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if reps[0].MedianCostUSD != 0 {
		t.Fatalf("unwired runner must report zero cost: %+v", reps[0])
	}
}

// Report field populated end-to-end (synthetic pass, no model):
// openai/gpt-5.2-mini is $0.25/$2 per 1M, so 1000/500 tokens → $0.00125.
func TestEvalMedianCostPopulated(t *testing.T) {
	r := &Runner{
		Tasks:      []Task{alwaysPassTask()},
		Trials:     3,
		NewLoop:    usageFactory(1000, 500),
		ProviderID: "openai",
		Model:      "gpt-5.2-mini",
	}
	reps, err := r.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := (1000*0.25 + 500*2) / 1e6 // 0.00125
	if math.Abs(reps[0].MedianCostUSD-want) > 1e-12 {
		t.Fatalf("MedianCostUSD = %v, want %v (%+v)", reps[0].MedianCostUSD, want, reps[0])
	}
}

// Median is over PASSING trials only: one cheap pass + two huge-token
// failures must still report the cheap success price. A cost-of-medians
// shortcut would report the huge figure instead.
func TestEvalMedianCostPassingOnly(t *testing.T) {
	var idx int
	newLoop := func(root string) *agent.Loop {
		idx++
		if idx == 1 {
			return usageFactory(1000, 500)(root)
		}
		return usageFactory(1000000, 1000000)(root)
	}
	task := alwaysPassTask()
	task.Verify = func(_, _ string, _ []agent.Event) (bool, string) {
		if idx == 1 {
			return true, "ok"
		}
		return false, "boom"
	}
	// NOTE: idx is read in Verify after NewLoop set it for the same
	// trial (NewLoop always precedes Verify within a trial), so trial 1
	// passes cheap and trials 2–3 fail huge. Sequential by construction:
	// runTask loops trials one at a time.
	r := &Runner{
		Tasks:      []Task{task},
		Trials:     3,
		NewLoop:    newLoop,
		ProviderID: "openai",
		Model:      "gpt-5.2-mini",
	}
	reps, err := r.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if reps[0].Passes != 1 {
		t.Fatalf("want exactly 1 pass: %+v", reps[0])
	}
	want := (1000*0.25 + 500*2) / 1e6
	if math.Abs(reps[0].MedianCostUSD-want) > 1e-12 {
		t.Fatalf("MedianCostUSD = %v, want passing-only %v (%+v)", reps[0].MedianCostUSD, want, reps[0])
	}
}

// No passes → zero cost (no successful task to price).
func TestEvalMedianCostZeroWhenNoPass(t *testing.T) {
	task := alwaysPassTask()
	task.Verify = func(_, _ string, _ []agent.Event) (bool, string) {
		return false, "boom"
	}
	r := &Runner{
		Tasks:      []Task{task},
		Trials:     2,
		NewLoop:    usageFactory(1000, 500),
		ProviderID: "openai",
		Model:      "gpt-5.2-mini",
	}
	reps, err := r.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if reps[0].MedianCostUSD != 0 {
		t.Fatalf("zero passes must report zero cost: %+v", reps[0])
	}
}
