// Package compact — auto-compaction at 80% of a token budget (docs/Plan.md Phase 1).
//
// The context window is the tightest resource an agent has. Before every
// model call the loop estimates live usage; past 80% it summarizes older
// turns, keeps the most recent ones verbatim, and posts a one-line marker.
// The JSONL session log is append-only and is never rewritten — compaction
// only affects live context, so full history always survives on disk.
package compact

import (
	"context"
	"fmt"
	"strings"

	"tilde/internal/provider"
)

// Threshold is the fraction of budget that triggers compaction.
const Threshold = 0.8

// Estimate returns a rough token count: ~4 chars per token plus a small
// per-message overhead. Heuristic, not a tokenizer — it errs upward so we
// compact early rather than overflow silently.
func Estimate(msgs []provider.Message) int {
	total := 0
	for _, m := range msgs {
		total += (len(m.Role) + len(m.Content) + 12) / 4
	}
	return total
}

// Usage returns used/budget as a fraction (may exceed 1.0).
func Usage(msgs []provider.Message, budget int) float64 {
	if budget <= 0 {
		return 0
	}
	return float64(Estimate(msgs)) / float64(budget)
}

// Label renders "41% (13.1k/32k)" for status surfaces.
func Label(used, budget int) string {
	pct := 0
	if budget > 0 {
		pct = used * 100 / budget
	}
	return fmt.Sprintf("%d%% (%s/%s)", pct, k(used), k(budget))
}

func k(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// Compactor holds the budget policy.
type Compactor struct {
	Budget int // 0 → DefaultBudget
	// KeepRecent is how many trailing messages survive verbatim. 0 → 8.
	KeepRecent int
	// Summarize condenses dropped turns. Nil → FallbackSummary (no model call).
	Summarize func(ctx context.Context, old []provider.Message) (string, error)
}

// DefaultBudget matches the TUI spec reference (32.0k tokens).
const DefaultBudget = 32000

// Result is the compacted context.
type Result struct {
	Msgs    []provider.Message // summary message + kept recent
	Dropped int                // how many messages were summarized away
	Summary string
}

func (c *Compactor) budget() int {
	if c.Budget > 0 {
		return c.Budget
	}
	return DefaultBudget
}

func (c *Compactor) keep() int {
	if c.KeepRecent > 0 {
		return c.KeepRecent
	}
	return 8
}

// Needed reports whether msgs are past the compaction threshold.
func (c *Compactor) Needed(msgs []provider.Message) bool {
	return Usage(msgs, c.budget()) >= Threshold
}

// Compact summarizes all but the trailing KeepRecent messages. It never
// fails the turn: if Summarize errors, FallbackSummary keeps the original
// user goal plus a receipt of what was dropped.
func (c *Compactor) Compact(ctx context.Context, msgs []provider.Message) *Result {
	keep := c.keep()
	if len(msgs) <= keep {
		return &Result{Msgs: msgs}
	}
	old, recent := msgs[:len(msgs)-keep], msgs[len(msgs)-keep:]
	var summary string
	if c.Summarize != nil {
		s, err := c.Summarize(ctx, old)
		if err == nil && strings.TrimSpace(s) != "" {
			summary = s
		}
	}
	if summary == "" {
		summary = FallbackSummary(old)
	}
	if len(summary) > 2000 {
		summary = summary[:2000] + "\n[summary truncated]"
	}
	out := make([]provider.Message, 0, len(recent)+1)
	out = append(out, provider.Message{Role: "system",
		Content: "[Context compacted: " + fmt.Sprintf("%d", len(old)) + " older messages summarized — goals, findings & decisions kept. Full log on disk.]\n" + summary})
	out = append(out, recent...)
	return &Result{Msgs: out, Dropped: len(old), Summary: summary}
}

// FallbackSummary keeps the first user message (the goal) verbatim so a
// failed summarizer can never amputate intent, plus a drop receipt.
// Tool-result echoes (stored as user-role messages) are skipped — they
// are outputs, never the goal.
func FallbackSummary(old []provider.Message) string {
	goal := "(no earlier user message found)"
	// Prefer a carried goal: nested compactions embed prior summaries as
	// system messages starting with "Original goal:" — reusing one keeps
	// intent alive across repeated compactions instead of amputating it.
	for _, m := range old {
		if m.Role == "system" && strings.Contains(m.Content, "Original goal:") {
			goal = m.Content
			break
		}
	}
	if goal == "(no earlier user message found)" {
		for _, m := range old {
			if m.Role == "user" && strings.TrimSpace(m.Content) != "" &&
				!strings.HasPrefix(m.Content, "Tool ") {
				goal = m.Content
				break
			}
		}
	}
	return "Original goal: " + goal + fmt.Sprintf("\n[%d older messages dropped without model summary — recent messages below are verbatim.]", len(old))
}

// SummarizeWithProvider condenses turns via a model call (no tools).
// An empty/contentless reply is an error so the caller falls back loudly.
func SummarizeWithProvider(p provider.Provider) func(context.Context, []provider.Message) (string, error) {
	return SummarizeWithPrompt(p, "Summarize this coding-agent session for context compaction. Keep: the user's goal, key findings about the codebase, decisions made, what was already tried, and pending work. Be terse — under 60 lines, no preamble.")
}

// SummarizeWithPrompt is SummarizeWithProvider with a custom brief
// (e.g. a /compact focus area).
func SummarizeWithPrompt(p provider.Provider, prompt string) func(context.Context, []provider.Message) (string, error) {
	return func(ctx context.Context, old []provider.Message) (string, error) {
		msgs := append([]provider.Message{{Role: "system", Content: prompt}}, old...)
		resp, err := p.Chat(ctx, msgs, nil)
		if err != nil {
			return "", fmt.Errorf("compaction summary call failed: %w — falling back to goal + drop receipt", err)
		}
		if strings.TrimSpace(resp.Content) == "" {
			return "", fmt.Errorf("compaction summary came back empty — falling back to goal + drop receipt")
		}
		return resp.Content, nil
	}
}
