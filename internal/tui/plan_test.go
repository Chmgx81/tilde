package tui

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"tilde/internal/agent"
	"tilde/internal/mode"
	"tilde/internal/tools"
)

// countBanner counts §2.9 banner occurrences in the transcript.
func countBanner(m Model) int {
	n := 0
	for _, ln := range m.lines {
		if strings.Contains(stripANSI(ln), planBannerTitle) {
			n++
		}
	}
	return n
}

// countTodoBlocks counts rendered ● Update Todos headers.
func countTodoBlocks(m Model) int {
	n := 0
	for _, ln := range m.lines {
		if strings.Contains(stripANSI(ln), "Update Todos") {
			n++
		}
	}
	return n
}

func TestPlanBannerOnceAtSessionStart(t *testing.T) {
	m := New(&agent.Loop{}, mode.Plan, t.TempDir(), "test-model", 32000)
	if !m.planBannerShown {
		t.Fatal("Plan session must record its session-start banner")
	}
	if n := countBanner(m); n != 1 {
		t.Fatalf("session start must render the banner once, got %d", n)
	}
	// The banner is a full-width box: every line lands exactly on the frame.
	for _, ln := range planBanner(78) {
		if w := runeLen(stripANSI(ln)); w != 78 {
			t.Fatalf("banner line must fill the frame (78), got %d: %q", w, stripANSI(ln))
		}
	}
	// Spec wording: one-line mode+descriptor plus the researching line.
	joined := stripANSI(strings.Join(m.lines, "\n"))
	if !strings.Contains(joined, "Plan · read-only") {
		t.Fatal("banner must carry the one-line mode+descriptor")
	}
	if !strings.Contains(joined, planBannerBody) {
		t.Fatalf("banner body must match spec wording %q", planBannerBody)
	}
}

func TestPlanBannerAbsentOutsidePlan(t *testing.T) {
	m := New(&agent.Loop{}, mode.Build, t.TempDir(), "test-model", 32000)
	if m.planBannerShown {
		t.Fatal("Build session must not record a session-start banner")
	}
	if n := countBanner(m); n != 0 {
		t.Fatalf("Build session must start banner-free, got %d", n)
	}
}

func TestPlanBannerNotPerTurn(t *testing.T) {
	m := New(&agent.Loop{}, mode.Plan, t.TempDir(), "test-model", 32000)
	m.renderEvent(agent.Event{Kind: "assistant", Text: "looking into it"})
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "read_file internal/config/config.go"})
	m.renderEvent(agent.Event{Kind: "tool_result", Text: "package config"})
	m.renderEvent(agent.Event{Kind: "assistant", Text: "still researching"})
	if n := countBanner(m); n != 1 {
		t.Fatalf("Plan turns must not re-post the banner, got %d", n)
	}
}

func TestDemotionPostsBannerBesideToast(t *testing.T) {
	m := New(&agent.Loop{}, mode.Build, t.TempDir(), "test-model", 32000)
	m.handleModeCmd("plan")
	if n := countBanner(m); n != 1 {
		t.Fatalf("Build→Plan demotion must post the banner once, got %d", n)
	}
	if !strings.Contains(m.toast, "Mode: Build → Plan") {
		t.Fatalf("demotion must keep the existing toast, got %q", m.toast)
	}
	// Re-selecting Plan is a no-op: no second banner.
	m.handleModeCmd("plan")
	if n := countBanner(m); n != 1 {
		t.Fatalf("no-op mode set must not re-post the banner, got %d", n)
	}
	// Promotions never banner.
	m.handleModeCmd("build")
	if n := countBanner(m); n != 1 {
		t.Fatalf("Plan→Build promotion must not post the banner, got %d", n)
	}
}

// todoTestModel wires a live TodoManager through the loop registry — the
// same chain production uses (loop.Reg.Get("todo_write") → TodoWrite{Mgr}).
func todoTestModel(t *testing.T, mgr *tools.TodoManager) Model {
	t.Helper()
	reg := tools.NewRegistry()
	reg.Register(&tools.TodoWrite{Mgr: mgr})
	return New(&agent.Loop{Reg: reg}, mode.Plan, t.TempDir(), "test-model", 32000)
}

func todoFire(m *Model, ctx context.Context, reg *tools.Registry, op map[string]any) {
	m.renderEvent(agent.Event{Kind: "tool_call", Text: "todo_write list"})
	m.renderEvent(agent.Event{Kind: "tool_result", Text: reg.Dispatch(ctx, "todo_write", op)})
}

func TestTodoBlockFromManagerState(t *testing.T) {
	ctx := context.Background()
	mgr := &tools.TodoManager{}
	reg := tools.NewRegistry()
	reg.Register(&tools.TodoWrite{Mgr: mgr})
	m := todoTestModel(t, mgr)

	reg.Dispatch(ctx, "todo_write", map[string]any{"op": "add", "text": "Explore project structure"})
	reg.Dispatch(ctx, "todo_write", map[string]any{"op": "add", "text": "Draft the provider change"})
	reg.Dispatch(ctx, "todo_write", map[string]any{"op": "done", "id": float64(1)})
	todoFire(&m, ctx, reg, map[string]any{"op": "list"})

	joined := stripANSI(strings.Join(m.lines, "\n"))
	if n := countTodoBlocks(m); n != 1 {
		t.Fatalf("one todo_write result must render one block, got %d", n)
	}
	for _, want := range []string{"● Update Todos", "☑ Explore project structure", "□ Draft the provider change"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("todo block must contain %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "%") {
		t.Fatalf("todo block must never carry a percentage bar:\n%s", joined)
	}
	// Order follows the manager list: done first, then pending.
	if strings.Index(joined, "☑ Explore") > strings.Index(joined, "□ Draft") {
		t.Fatalf("todo block must preserve manager order:\n%s", joined)
	}
}

func TestTodoUnchangedStateSuppressed(t *testing.T) {
	ctx := context.Background()
	mgr := &tools.TodoManager{}
	reg := tools.NewRegistry()
	reg.Register(&tools.TodoWrite{Mgr: mgr})
	m := todoTestModel(t, mgr)

	reg.Dispatch(ctx, "todo_write", map[string]any{"op": "add", "text": "Only item"})
	todoFire(&m, ctx, reg, map[string]any{"op": "list"})
	todoFire(&m, ctx, reg, map[string]any{"op": "list"})
	if n := countTodoBlocks(m); n != 1 {
		t.Fatalf("unchanged state must not reprint the block, got %d", n)
	}
	// A real change revises: second block appears with the new checkbox.
	reg.Dispatch(ctx, "todo_write", map[string]any{"op": "done", "id": float64(1)})
	todoFire(&m, ctx, reg, map[string]any{"op": "list"})
	if n := countTodoBlocks(m); n != 2 {
		t.Fatalf("changed state must post a revised block, got %d", n)
	}
	if joined := stripANSI(strings.Join(m.lines, "\n")); !strings.Contains(joined, "☑ Only item") {
		t.Fatalf("revised block must reflect the new state:\n%s", joined)
	}
}

func TestTodoEmptyAndUnreachableRenderNothing(t *testing.T) {
	ctx := context.Background()
	// Empty manager: raw result only, no structured block.
	mgr := &tools.TodoManager{}
	reg := tools.NewRegistry()
	reg.Register(&tools.TodoWrite{Mgr: mgr})
	m := todoTestModel(t, mgr)
	todoFire(&m, ctx, reg, map[string]any{"op": "list"})
	if n := countTodoBlocks(m); n != 0 {
		t.Fatalf("empty todo list must render no block, got %d", n)
	}
	// Unreachable manager (nil loop): no block, no crash.
	bare := New(nil, mode.Plan, t.TempDir(), "test-model", 32000)
	bare.renderEvent(agent.Event{Kind: "tool_call", Text: "todo_write list"})
	bare.renderEvent(agent.Event{Kind: "tool_result", Text: "todo list is empty."})
	if n := countTodoBlocks(bare); n != 0 {
		t.Fatalf("unreachable manager must render no block, got %d", n)
	}
	// Registry without todo_write: no block, no crash.
	m2 := New(&agent.Loop{Reg: tools.NewRegistry()}, mode.Plan, t.TempDir(), "test-model", 32000)
	m2.renderEvent(agent.Event{Kind: "tool_call", Text: "todo_write list"})
	m2.renderEvent(agent.Event{Kind: "tool_result", Text: "whatever"})
	if n := countTodoBlocks(m2); n != 0 {
		t.Fatalf("missing todo_write tool must render no block, got %d", n)
	}
}

func TestTodoActiveIndex(t *testing.T) {
	items := []tools.TodoItem{{ID: 1, Text: "done", Done: true}, {ID: 2, Text: "active"}, {ID: 3, Text: "later"}}
	if got := todoActiveIndex(items); got != 1 {
		t.Fatalf("active must be the first unchecked item, got %d", got)
	}
	if got := todoActiveIndex(nil); got != -1 {
		t.Fatalf("empty list must have no active item, got %d", got)
	}
	allDone := []tools.TodoItem{{ID: 1, Text: "a", Done: true}}
	if got := todoActiveIndex(allDone); got != -1 {
		t.Fatalf("all-done list must have no active item, got %d", got)
	}
}

func TestTodoActiveItemBold(t *testing.T) {
	// Force SGR output: plain test runs sit on the Ascii profile, which
	// strips all styling (see the fuzzy-highlight precedent).
	lipgloss.SetColorProfile(termenv.ANSI)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	items := []tools.TodoItem{{ID: 1, Text: "finished", Done: true}, {ID: 2, Text: "active work"}, {ID: 3, Text: "later work"}}
	got := renderTodoBlock(items)
	// Lipgloss folds bold into the color sequence ("\x1b[1;37m"), so match
	// the SGR bold opener in either its lone or combined form.
	boldRe := regexp.MustCompile("\x1b\\[1[;m]")
	if locs := boldRe.FindAllStringIndex(got, -1); len(locs) != 1 {
		t.Fatalf("exactly the active item must be bold, bold runs=%d:\n%q", len(locs), got)
	}
	activeLine := ""
	for _, ln := range strings.Split(got, "\n") {
		if strings.Contains(stripANSI(ln), "active work") {
			activeLine = ln
		}
	}
	if !boldRe.MatchString(activeLine) {
		t.Fatalf("bold must sit on the first unchecked item, got %q", activeLine)
	}
}
