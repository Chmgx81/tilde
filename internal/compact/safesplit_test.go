package compact

import (
	"strings"
	"testing"

	"tilde/internal/provider"
)

// A compaction boundary that lands on a tool-result run must walk forward
// past it, so the kept-recent slice never starts on a result whose
// originating call was summarized away.
func TestSafeSplitSkipsStrandedToolResults(t *testing.T) {
	msgs := []provider.Message{
		{Role: "user", Content: "goal"},
		{Role: "assistant", Content: "working"},
		{Role: "user", Content: "Tool read_file result:\nA"},
		{Role: "user", Content: "Tool grep result:\nB"},
		{Role: "assistant", Content: "done"},
	}
	// len=5, keep=2 → naive boundary index 3 (a tool result).
	split := safeSplit(msgs, 2)
	if split != 4 {
		t.Fatalf("split = %d, want 4 (past the tool-result run)", split)
	}
	if strings.HasPrefix(msgs[split].Content, "Tool ") {
		t.Fatalf("recent must not start on a tool result: %q", msgs[split].Content)
	}
}

func TestSafeSplitNoopWhenBoundaryClean(t *testing.T) {
	msgs := []provider.Message{
		{Role: "user", Content: "goal"},
		{Role: "assistant", Content: "a"},
		{Role: "user", Content: "next"},
		{Role: "assistant", Content: "b"},
	}
	if got := safeSplit(msgs, 2); got != 2 {
		t.Fatalf("clean boundary must be unchanged, got %d", got)
	}
	// keep >= len → everything retained (split 0).
	if got := safeSplit(msgs, 10); got != 0 {
		t.Fatalf("keep>=len must split at 0, got %d", got)
	}
}
