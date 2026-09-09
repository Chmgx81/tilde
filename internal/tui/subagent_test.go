package tui

import (
	"strings"
	"testing"

	"tilde/internal/agent"
	"tilde/internal/mode"
)

// Spec §2.19, render-state only: ⋮ running rows on spawn dispatch, │
// completed rows with right-aligned [done] on the matching result.
// No competing vocabulary: ⋮/│ appear only for spawns, never for
// ordinary calls, grouping, or confirms.

func subLines(t *testing.T, m Model, base int) []string {
	t.Helper()
	return m.lines[base:]
}

func TestSubagentExploreRunningRow(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "tilde-test", 32000)
	base := len(m.lines)
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "spawn_explore "})
	got := subLines(t, m, base)
	if len(got) != 1 {
		t.Fatalf("one spawn call = one running row, got %d", len(got))
	}
	plain := stripANSI(got[0])
	if !strings.HasPrefix(plain, "⋮ ") {
		t.Fatalf("running row must open with ⋮, got %q", plain)
	}
	if !strings.Contains(plain, "explore") {
		t.Fatalf("running row must name the type, got %q", plain)
	}
	if !strings.Contains(plain, "tilde-test") {
		t.Fatalf("running row must attribute the parent model, got %q", plain)
	}
	if strings.Contains(plain, "[done]") || strings.Contains(plain, "│") {
		t.Fatalf("running row must not carry completion vocabulary, got %q", plain)
	}
}

func TestSubagentWorkRunningRowCarriesPath(t *testing.T) {
	// shortArgs surfaces the worktree path on the call text — the only
	// call-time detail a spawn_work event carries.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "tilde-test", 32000)
	base := len(m.lines)
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "spawn_work .tilde-work/w1"})
	plain := stripANSI(subLines(t, m, base)[0])
	if !strings.HasPrefix(plain, "⋮ ") || !strings.Contains(plain, "work") {
		t.Fatalf("work running row must open with ⋮ and name the type, got %q", plain)
	}
	if !strings.Contains(plain, ".tilde-work/w1") || !strings.Contains(plain, "tilde-test") {
		t.Fatalf("work running row must carry path + model, got %q", plain)
	}
}

func TestSubagentExploreCompletion(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "tilde-test", 32000)
	base := len(m.lines)
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "spawn_explore "})
	m.renderEvent(agent.Event{Kind: "tool_result", Text: "parallel exploration: 2/2 done\n" +
		"│ Diff recent deploys    [done]\n" +
		"│ Rank slowest endpoints [done]\n" +
		"--- Diff recent deploys ---\ndeploys look clean\n" +
		"--- Rank slowest endpoints ---\n/auth/token is slowest\n"})
	var rows []string
	for _, ln := range subLines(t, m, base) {
		if strings.HasPrefix(stripANSI(ln), "│ ") && strings.HasSuffix(strings.TrimRight(stripANSI(ln), " "), "[done]") {
			rows = append(rows, stripANSI(ln))
		}
	}
	if len(rows) != 2 {
		t.Fatalf("one │ row per finished task, got %d:\n%s", len(rows), stripANSI(strings.Join(subLines(t, m, base), "\n")))
	}
	for _, r := range rows {
		if !strings.Contains(r, "explore · tilde-test") {
			t.Fatalf("done row must carry type · model, got %q", r)
		}
	}
	if !strings.Contains(stripANSI(rows[0]), "Diff recent deploys") ||
		!strings.Contains(stripANSI(rows[1]), "Rank slowest endpoints") {
		t.Fatalf("done rows must name each task, got %q", rows)
	}
	// Right-aligned: the status docks at the same visible column.
	ends := map[int]bool{}
	for _, r := range rows {
		ends[len([]rune(strings.TrimRight(r, " ")))] = true
	}
	if len(ends) != 1 {
		t.Fatalf("[done] must share one end column, got %q", rows)
	}
	// Findings survive: the summaries still render below the rows.
	joined := stripANSI(strings.Join(subLines(t, m, base), "\n"))
	if !strings.Contains(joined, "deploys look clean") || !strings.Contains(joined, "slowest") {
		t.Fatalf("completion must keep the summaries, got:\n%s", joined)
	}
	// The raw │ task lines are replaced, not duplicated.
	if strings.Count(joined, "Diff recent deploys") != 2 {
		t.Fatalf("task name must appear once styled + once in its summary header, got:\n%s", joined)
	}
}

func TestSubagentFailedRow(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "tilde-test", 32000)
	base := len(m.lines)
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "spawn_explore "})
	m.renderEvent(agent.Event{Kind: "tool_result", Text: "parallel exploration: 0/1 done\n│ Pull slow query plans [failed]\n--- Pull slow query plans ---\n[task failed: timeout — treat its area as unexplored]\n"})
	found := false
	for _, ln := range subLines(t, m, base) {
		if p := strings.TrimRight(stripANSI(ln), " "); strings.HasPrefix(p, "│ ") && strings.HasSuffix(p, "[failed]") {
			found = true
		}
	}
	if !found {
		t.Fatalf("failed task must render a │ [failed] row:\n%s", stripANSI(strings.Join(subLines(t, m, base), "\n")))
	}
}

func TestSubagentWorkCompletion(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "tilde-test", 32000)
	base := len(m.lines)
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "spawn_work .tilde-work/w1"})
	m.renderEvent(agent.Event{Kind: "tool_result", Text: "work session ready at .tilde-work/w1 (task_id: work_1)\nadded retry helper\nFinish with apply_work {\"path\": \".tilde-work/w1\"} to review the diff (kept), or discard_work {\"path\": \".tilde-work/w1\"} to remove it."})
	joined := stripANSI(strings.Join(subLines(t, m, base), "\n"))
	lines := strings.Split(joined, "\n")
	var done string
	for _, ln := range lines {
		if p := strings.TrimRight(ln, " "); strings.HasPrefix(p, "│ ") && strings.HasSuffix(p, "[done]") {
			done = p
		}
	}
	if done == "" {
		t.Fatalf("work completion must render one │ [done] row:\n%s", joined)
	}
	if !strings.Contains(done, "work · tilde-test") || !strings.Contains(done, ".tilde-work/w1") {
		t.Fatalf("work done row must carry path + type · model, got %q", done)
	}
	if !strings.Contains(joined, "added retry helper") {
		t.Fatalf("work summary must survive below the row:\n%s", joined)
	}
}

func TestSubagentDeniedResultWithoutCall(t *testing.T) {
	// Denials emit tool_result with no preceding tool_call: with nothing
	// pending they must render normally, never as a │ row.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "tilde-test", 32000)
	base := len(m.lines)
	m.renderEvent(agent.Event{Kind: "tool_result", Text: `tool "spawn_explore" denied by policy (ask-tier). Do not retry; propose an alternative.`})
	for _, ln := range subLines(t, m, base) {
		if strings.HasPrefix(strings.TrimSpace(stripANSI(ln)), "│") {
			t.Fatalf("denial without a spawn must not render │ vocabulary, got %q", stripANSI(ln))
		}
	}
	if len(m.subPending) != 0 {
		t.Fatal("denial must not arm the spawn tracker")
	}
}

func TestSubagentFlushesReadGroup(t *testing.T) {
	// Buffered read-only pairs flush as their parent BEFORE the ⋮ row —
	// the spawn landmarks a clean break, never joins the group.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "tilde-test", 32000)
	base := len(m.lines)
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "read_file a.txt"})
	m.renderEvent(agent.Event{Kind: "tool_result", Text: "content a"})
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "read_file b.txt"})
	m.renderEvent(agent.Event{Kind: "tool_result", Text: "content b"})
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "spawn_explore "})
	got := subLines(t, m, base)
	if len(got) != 4 {
		t.Fatalf("parent + 2 children + ⋮ row = 4 lines, got %d:\n%s", len(got), stripANSI(strings.Join(got, "\n")))
	}
	if p := stripANSI(got[0]); !strings.HasPrefix(p, "● ") || !strings.Contains(p, "×2") {
		t.Fatalf("group parent must come first, got %q", p)
	}
	if p := stripANSI(got[3]); !strings.HasPrefix(p, "⋮ ") {
		t.Fatalf("⋮ row must sit below the flushed group, got %q", p)
	}
	// A lone pair after the batch still renders ungrouped (no parent).
	m.renderEvent(agent.Event{Kind: "tool_result", Text: "parallel exploration: 0/0 done"})
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "read_file c.txt"})
	m.renderEvent(agent.Event{Kind: "tool_result", Text: "content c"})
	m.renderEvent(agent.Event{Kind: "assistant", Text: "synthesis"})
	rest := stripANSI(strings.Join(subLines(t, m, base+len(got)), "\n"))
	if strings.Contains(rest, "×") {
		t.Fatalf("lone pair after the batch must stay ungrouped:\n%s", rest)
	}
	if !strings.Contains(rest, "synthesis") {
		t.Fatalf("parent synthesis must sit below the batch:\n%s", rest)
	}
}

func TestApplyDiscardRenderNormally(t *testing.T) {
	// Minimal grouping: only the spawn call owns ⋮/│ rows. apply/discard
	// stay on the ordinary ● + ⎿ path (no cross-call path matching).
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "tilde-test", 32000)
	base := len(m.lines)
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "apply_work .tilde-work/w1"})
	m.renderEvent(agent.Event{Kind: "tool_result", Text: "applied work at .tilde-work/w1 (kept on disk — merge it yourself)"})
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "discard_work .tilde-work/w2"})
	m.renderEvent(agent.Event{Kind: "tool_result", Text: "removed worktree at .tilde-work/w2"})
	for _, ln := range subLines(t, m, base) {
		p := stripANSI(ln)
		if strings.HasPrefix(p, "⋮ ") || strings.HasPrefix(strings.TrimSpace(p), "│") {
			t.Fatalf("apply/discard must never use subagent glyphs, got %q", p)
		}
	}
	joined := stripANSI(strings.Join(subLines(t, m, base), "\n"))
	if !strings.Contains(joined, "apply_work") || !strings.Contains(joined, "discard_work") {
		t.Fatalf("apply/discard keep their raw names on ● lines:\n%s", joined)
	}
	if len(m.subPending) != 0 {
		t.Fatal("apply/discard must not touch the spawn tracker")
	}
}

func TestSubagentSynthesisBelowBatch(t *testing.T) {
	// Parent synthesis is plain prose after the batch — verify the order,
	// no implementation needed beyond it.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "tilde-test", 32000)
	base := len(m.lines)
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "spawn_explore "})
	m.renderEvent(agent.Event{Kind: "tool_result", Text: "parallel exploration: 1/1 done\n│ Diff recent deploys [done]\n--- Diff recent deploys ---\ndeploys clean\n"})
	m.renderEvent(agent.Event{Kind: "assistant", Text: "Splitting deploys, slow endpoints, DB plans into parallel digs."})
	lines := subLines(t, m, base)
	last := stripANSI(lines[len(lines)-1])
	if !strings.Contains(last, "parallel digs") {
		t.Fatalf("synthesis prose must close the batch, got %q", last)
	}
	for i, ln := range lines {
		if strings.Contains(stripANSI(ln), "parallel digs") && i != len(lines)-1 {
			t.Fatalf("synthesis must not interleave with batch rows:\n%s", stripANSI(strings.Join(lines, "\n")))
		}
	}
}
