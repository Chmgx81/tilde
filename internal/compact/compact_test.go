package compact

import (
	"context"
	"strings"
	"testing"

	"tilde/internal/provider"
)

func msgs(n int, size int) []provider.Message {
	out := make([]provider.Message, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, provider.Message{Role: "user", Content: strings.Repeat("x", size)})
	}
	return out
}

func TestEstimateErrsUpward(t *testing.T) {
	m := []provider.Message{{Role: "user", Content: "hello"}} // 4+5+12=21 → 5
	if got := Estimate(m); got < 2 {
		t.Fatalf("estimate implausibly low: %d", got)
	}
}

func TestNeededAt80Percent(t *testing.T) {
	c := &Compactor{Budget: 100}
	// 79 tokens under → false; over → true. Content chars/4 ≈ tokens.
	if c.Needed(msgs(1, 79*4-20)) {
		t.Fatal("compacted below threshold")
	}
	if !c.Needed(msgs(1, 81*4)) {
		t.Fatal("did not compact past threshold")
	}
}

func TestCompactKeepsRecentVerbatim(t *testing.T) {
	all := []provider.Message{
		{Role: "user", Content: "goal: fix the thing"},
		{Role: "assistant", Content: "old-1"},
		{Role: "assistant", Content: "old-2"},
		{Role: "assistant", Content: "new-1"},
		{Role: "assistant", Content: "new-2"},
	}
	c := &Compactor{Budget: 1, KeepRecent: 2, Summarize: func(_ context.Context, old []provider.Message) (string, error) {
		return "SUMMARY", nil
	}}
	res := c.Compact(context.Background(), all)
	if res.Dropped != 3 {
		t.Fatalf("dropped=%d, want 3", res.Dropped)
	}
	if len(res.Msgs) != 3 || res.Msgs[0].Role != "system" {
		t.Fatalf("expected summary + 2 recent, got %+v", res.Msgs)
	}
	if res.Msgs[1].Content != "new-1" || res.Msgs[2].Content != "new-2" {
		t.Fatalf("recent turns altered: %+v", res.Msgs)
	}
	if !strings.Contains(res.Msgs[0].Content, "SUMMARY") {
		t.Fatalf("summary missing: %q", res.Msgs[0].Content)
	}
}

func TestCompactFallsBackOnSummarizerError(t *testing.T) {
	all := []provider.Message{
		{Role: "user", Content: "goal: keep me"},
		{Role: "assistant", Content: "old"},
		{Role: "assistant", Content: "recent"},
	}
	c := &Compactor{Budget: 1, KeepRecent: 1, Summarize: func(_ context.Context, _ []provider.Message) (string, error) {
		return "", context.DeadlineExceeded // summarizer down
	}}
	res := c.Compact(context.Background(), all)
	if !strings.Contains(res.Msgs[0].Content, "goal: keep me") {
		t.Fatalf("fallback amputated the goal: %q", res.Msgs[0].Content)
	}
	if res.Dropped != 2 {
		t.Fatalf("dropped=%d, want 2", res.Dropped)
	}
}

func TestFallbackPrefersSystemGoal(t *testing.T) {
	// Nested compactions carry prior summaries as system messages
	// containing "Original goal:" — the fallback must reuse one so
	// repeated compactions never amputate intent.
	old := []provider.Message{
		{Role: "system", Content: "Original goal: fix the login bug\n[2 older messages dropped without model summary — recent messages below are verbatim.]"},
		{Role: "user", Content: "Tool glob result:\n*.go"},
		{Role: "assistant", Content: "old work"},
	}
	s := FallbackSummary(old)
	if !strings.Contains(s, "Original goal: fix the login bug") {
		t.Fatalf("carried goal lost: %q", s)
	}
}

func TestFallbackUserGoalStillWorks(t *testing.T) {
	old := []provider.Message{
		{Role: "user", Content: "goal: keep me"},
		{Role: "assistant", Content: "old"},
	}
	if s := FallbackSummary(old); !strings.Contains(s, "goal: keep me") {
		t.Fatalf("user goal lost: %q", s)
	}
}

func TestLabel(t *testing.T) {
	if got := Label(13100, 32000); got != "40% (13.1k/32.0k)" {
		t.Fatalf("got %q", got)
	}
}
