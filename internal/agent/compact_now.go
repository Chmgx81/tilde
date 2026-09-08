package agent

import (
	"context"
	"fmt"

	"tilde/internal/compact"
)

// CompactNow compacts on demand (/compact [focus]) outside the auto path.
// It reuses the loop's compactor, swaps in a focus-aware summarizer when
// asked, swaps live context, and preserves the full summary in the log.
// Returns the one-line transcript marker and how many turns were dropped.
func (l *Loop) CompactNow(ctx context.Context, focus string) (string, int) {
	comp := l.Cfg.Compactor
	if comp == nil {
		comp = &compact.Compactor{}
	}
	if l.Prov != nil && (focus != "" || comp.Summarize == nil) {
		c2 := *comp
		if focus != "" {
			c2.Summarize = compact.SummarizeWithPrompt(l.Prov,
				"Summarize this coding-agent session for context compaction with special attention to: "+focus+
					". Also keep: the user's goal, key findings, decisions, what was tried, pending work. Terse — under 60 lines.")
		} else {
			c2.Summarize = compact.SummarizeWithProvider(l.CurrentProvider())
		}
		comp = &c2
	}
	res := comp.Compact(ctx, l.MsgsSnapshot())
	if res.Dropped == 0 {
		return "", 0 // nothing dropped: no marker, no log
	}
	l.SetMsgs(res.Msgs)
	marker := fmt.Sprintf("[compacted: %d older messages — goals, findings & decisions kept · see session log]", res.Dropped)
	l.appendLog("compacted", map[string]any{"dropped": res.Dropped, "summary": res.Summary, "manual": true, "focus": focus}, nil)
	return marker, res.Dropped
}
