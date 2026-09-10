package tui

import (
	"context"
	"strings"
	"testing"

	"tilde/internal/agent"
	"tilde/internal/mode"
	"tilde/internal/tools"
)

// workStub stands in for apply_work/discard_work: the TUI dispatches by
// registry name with {"path": ...}, so a stub proves the wiring without a
// real git worktree.
type workStub struct {
	name string
	got  map[string]any
	out  string
	err  error
}

func (s *workStub) Name() string        { return s.name }
func (s *workStub) Description() string { return "test stub" }
func (s *workStub) Schema() map[string]any {
	return map[string]any{"type": "object"}
}
func (s *workStub) Exec(_ context.Context, args map[string]any) (string, error) {
	s.got = args
	return s.out, s.err
}

func newWorkLoop(stubs ...tools.Tool) *agent.Loop {
	reg := tools.NewRegistry()
	for _, t := range stubs {
		reg.Register(t)
	}
	return &agent.Loop{Reg: reg}
}

// Palette must advertise /apply and /discard.
func TestWorkPaletteEntries(t *testing.T) {
	for _, want := range []string{"/apply [path]", "/discard [path]"} {
		found := false
		for _, r := range slashCommands {
			if r.cmd == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("palette missing %s", want)
		}
	}
	if rows := filterSlash("/apply"); len(rows) == 0 || rows[0].cmd != "/apply [path]" {
		t.Fatalf("/apply must be top match for its filter, got %v", rows)
	}
	if rows := filterSlash("/discard"); len(rows) == 0 || rows[0].cmd != "/discard [path]" {
		t.Fatalf("/discard must be top match for its filter, got %v", rows)
	}
	help := helpView(200)
	if !strings.Contains(help, "/apply") || !strings.Contains(help, "/discard") {
		t.Fatal("help view must list /apply and /discard")
	}
}

// Fail loud with no session: no arg and no pending session tracked.
func TestWorkNoSessionFailLoud(t *testing.T) {
	loop := newWorkLoop(&workStub{name: "apply_work"}, &workStub{name: "discard_work"})
	m := New(loop, mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.runSlash("/apply", "", slashRow{})
	if got := stripANSI(strings.Join(m.lines, "\n")); !strings.Contains(got, "no pending work session") {
		t.Fatalf("/apply with no session must fail loud, got:\n%s", got)
	}
	m.runSlash("/discard", "", slashRow{})
	if got := stripANSI(strings.Join(m.lines, "\n")); !strings.Contains(got, "no pending work session") {
		t.Fatalf("/discard with no session must fail loud, got:\n%s", got)
	}
}

// Fail loud with no registry and with the tool unregistered.
func TestWorkNoRegistryFailLoud(t *testing.T) {
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.runSlash("/apply", "", slashRow{})
	if got := stripANSI(strings.Join(m.lines, "\n")); !strings.Contains(got, "no tool registry attached") {
		t.Fatalf("/apply with no registry must fail loud, got:\n%s", got)
	}
	m2 := New(newWorkLoop(), mode.Plan, t.TempDir(), "ollama/m", 32000)
	m2.runSlash("/discard", "", slashRow{})
	if got := stripANSI(strings.Join(m2.lines, "\n")); !strings.Contains(got, "discard_work tool not registered") {
		t.Fatalf("/discard with no tool must fail loud, got:\n%s", got)
	}
}

// Dispatch: an explicit path reaches the registry tool verbatim.
func TestWorkDispatchApplies(t *testing.T) {
	apply := &workStub{name: "apply_work", out: "applied work at wt1 (kept on disk — merge it yourself)"}
	loop := newWorkLoop(apply)
	m := New(loop, mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.runSlash("/apply", "wt1", slashRow{})
	if apply.got == nil || apply.got["path"] != "wt1" {
		t.Fatalf("apply_work must receive {\"path\": \"wt1\"}, got %v", apply.got)
	}
	if got := stripANSI(strings.Join(m.lines, "\n")); !strings.Contains(got, "applied work at wt1") {
		t.Fatalf("apply result must surface, got:\n%s", got)
	}
}

func TestWorkDispatchDiscards(t *testing.T) {
	discard := &workStub{name: "discard_work", out: "removed worktree at wt2"}
	loop := newWorkLoop(discard)
	m := New(loop, mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.runSlash("/discard", "wt2", slashRow{})
	if discard.got == nil || discard.got["path"] != "wt2" {
		t.Fatalf("discard_work must receive {\"path\": \"wt2\"}, got %v", discard.got)
	}
	if got := stripANSI(strings.Join(m.lines, "\n")); !strings.Contains(got, "removed worktree at wt2") {
		t.Fatalf("discard result must surface, got:\n%s", got)
	}
}

// Tool errors surface first-line loud, never swallowed.
func TestWorkToolErrorSurfaces(t *testing.T) {
	stub := &workStub{name: "apply_work", out: "", err: errWorkTest("unknown work session \"wt9\": active session is \"wt1\"\nsecond line must not smear")}
	loop := newWorkLoop(stub)
	m := New(loop, mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.runSlash("/apply", "wt9", slashRow{})
	got := stripANSI(strings.Join(m.lines, "\n"))
	if !strings.Contains(got, "unknown work session") {
		t.Fatalf("tool error must surface, got:\n%s", got)
	}
	if strings.Contains(got, "second line must not smear") {
		t.Fatalf("only the first error line may render, got:\n%s", got)
	}
}

type errWorkTest string

func (e errWorkTest) Error() string { return string(e) }

// Bounded output: a huge apply diff truncates in the transcript, loudly.
func TestWorkApplyOutputBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("applied work at wt1 (kept on disk — merge it yourself)\n")
	for i := 0; i < maxWorkApplyLines+50; i++ {
		b.WriteString("+line of diff\n")
	}
	apply := &workStub{name: "apply_work", out: b.String()}
	loop := newWorkLoop(apply)
	m := New(loop, mode.Plan, t.TempDir(), "ollama/m", 32000)
	base := len(m.lines)
	m.runSlash("/apply", "wt1", slashRow{})
	if n := len(m.lines) - base; n > maxWorkApplyLines+2 {
		t.Fatalf("apply output must stay bounded at ~%d lines, got %d", maxWorkApplyLines, n)
	}
	if got := stripANSI(strings.Join(m.lines[base:], "\n")); !strings.Contains(got, "truncated in transcript") {
		t.Fatalf("truncation must be announced, got:\n%s", got)
	}
}
