package tui

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"tilde/internal/agent"
	"tilde/internal/mode"
	"tilde/internal/tools"
)

// startSleepTask registers a Shell tool with a live background task so the
// status bar has something to report. Returns the model and a cleanup.
func startSleepTask(t *testing.T, root string) *Model {
	t.Helper()
	mgr := &tools.TaskManager{LogDir: t.TempDir()}
	reg := tools.NewRegistry()
	reg.Register(&tools.Shell{Root: root, Tasks: mgr})
	loop := &agent.Loop{Reg: reg}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(func() {
		mgr.KillAll()
		cancel()
	})
	if _, err := mgr.Start("sleep 30", ctx, cancel, func(stdout, stderr io.Writer) *exec.Cmd {
		cmd := exec.Command("sleep", "30")
		cmd.Stdout, cmd.Stderr = stdout, stderr
		return cmd
	}); err != nil {
		t.Fatalf("start background task: %v", err)
	}

	m := New(loop, mode.Plan, root, "openai/gpt-4.1-mini", 32000)
	m.vp.Width = 120
	m.ctx = "41% (13.1k/32k)"
	return &m
}

func TestStatusBarShowsBackgroundTasks(t *testing.T) {
	m := startSleepTask(t, t.TempDir())
	got := stripANSI(m.statusBar())
	if !strings.Contains(got, "⚙ 1 bg") {
		t.Fatalf("bar must show running background work: %q", got)
	}
	// The glyph and the words carry the meaning without color (NO_COLOR).
	if !strings.Contains(got, "bg") {
		t.Fatalf("indicator needs a word, not color alone: %q", got)
	}
}

func TestStatusBarHidesBackgroundWhenIdle(t *testing.T) {
	// A loop with no shell task manager (the test default) must render the
	// bar exactly as before — no stray glyph.
	m := New(newTestLoop(), mode.Plan, t.TempDir(), "openai/gpt-4.1-mini", 32000)
	m.vp.Width = 120
	m.ctx = "41% (13.1k/32k)"
	if got := stripANSI(m.statusBar()); strings.Contains(got, "bg") {
		t.Fatalf("idle bar must not show a background indicator: %q", got)
	}
}

func TestStatusBarBackgroundDropsBeforeRootShortens(t *testing.T) {
	m := startSleepTask(t, t.TempDir())
	// Isolate the background segment: with a bare ctx percentage and no
	// branch, the earlier drop steps (raw token count, dirty count, branch)
	// are all no-ops, so the next thing to give is the background indicator.
	m.ctx = "41%"
	m.branch = ""
	// Walk widths down and find the first one where the indicator is gone
	// (the rendered bar is padded, so its length is not the budget).
	for w := 200; w >= 20; w-- {
		m.vp.Width = w
		got := stripANSI(m.statusBar())
		if strings.Contains(got, "bg") {
			continue
		}
		if !strings.Contains(got, m.root) {
			t.Fatalf("at width %d the root was shortened before the background indicator dropped: %q", w, got)
		}
		return
	}
	t.Fatal("the background indicator never dropped, even at 20 columns")
}

func TestStatusBarNeverWrapsWithBackground(t *testing.T) {
	m := startSleepTask(t, t.TempDir())
	for _, w := range []int{40, 60, 80, 120} {
		m.vp.Width = w
		got := stripANSI(m.statusBar())
		if runeLen(got) > w {
			t.Fatalf("width %d: bar overflows (%d cells): %q", w, runeLen(got), got)
		}
	}
}
