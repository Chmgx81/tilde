package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tilde/internal/mode"
	"tilde/internal/policy"
	"tilde/internal/provider"
	"tilde/internal/tools"
)

// concurrencyProbe is a ParallelSafe-named tool ("grep") that records the
// peak concurrent Exec count — the semaphore bound is asserted on it.
type concurrencyProbe struct {
	delay time.Duration
	cur   *int64
	peak  *int64
}

func (p *concurrencyProbe) Name() string        { return "grep" }
func (p *concurrencyProbe) Description() string { return "test probe" }
func (p *concurrencyProbe) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{"pattern": map[string]any{"type": "string"}},
		"required":   []string{"pattern"}}
}
func (p *concurrencyProbe) Exec(_ context.Context, _ map[string]any) (string, error) {
	c := atomic.AddInt64(p.cur, 1)
	for {
		m := atomic.LoadInt64(p.peak)
		if c <= m || atomic.CompareAndSwapInt64(p.peak, m, c) {
			break
		}
	}
	time.Sleep(p.delay)
	atomic.AddInt64(p.cur, -1)
	return "ok", nil
}

func TestFlushBatchBoundsParallelism(t *testing.T) {
	var cur, peak int64
	reg := tools.NewRegistry()
	reg.Register(&concurrencyProbe{delay: 30 * time.Millisecond, cur: &cur, peak: &peak})
	loop := &Loop{Reg: reg, Cfg: Config{MaxIters: 3, DoomRepeats: 3, Mode: mode.Build}}
	var batch []provider.ToolCall
	for i := 0; i < 24; i++ {
		batch = append(batch, provider.ToolCall{ID: fmt.Sprint(i), Name: "grep", Args: map[string]any{"pattern": "x"}})
	}
	if err := loop.flushBatch(context.Background(), batch, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if p := atomic.LoadInt64(&peak); p > 8 {
		t.Fatalf("flushBatch ran %d concurrent tools, cap is 8", p)
	} else if p < 2 {
		t.Fatalf("expected some parallelism, peak=%d", p)
	}
}

func TestFlushBatchDefaultParallel(t *testing.T) {
	loop := &Loop{}
	if loop.flushParallel() != 8 {
		t.Fatalf("zero FlushParallel must default to 8, got %d", loop.flushParallel())
	}
	loop.Cfg.FlushParallel = 3
	if loop.flushParallel() != 3 {
		t.Fatalf("FlushParallel override lost, got %d", loop.flushParallel())
	}
}

func initWorkRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	run := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@t.t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return root
}

func workParent(t *testing.T, root string, script []provider.Response) (*Loop, *WorkState) {
	t.Helper()
	parent := &Loop{
		Prov: &fakeProv{},
		Reg:  testRegistry(root),
		Cfg:  Config{MaxIters: 6, DoomRepeats: 6, Root: root, Mode: mode.Build, Pol: &policy.Policy{AlwaysAllow: true}},
	}
	st := &WorkState{Root: root}
	st.NewChild = func(task, workRoot string) *Loop {
		child := NewWorkChild(parent, task, workRoot)
		child.Prov = &fakeProv{script: script}
		return child
	}
	return parent, st
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func TestSpawnApplyDiscardRoundTrip(t *testing.T) {
	root := initWorkRepo(t)
	ctx := context.Background()
	script := []provider.Response{
		{ToolCalls: []provider.ToolCall{{ID: "0", Name: "read_file", Args: map[string]any{"path": "f.txt"}}}},
		{ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Args: map[string]any{"path": "f.txt", "content": "v2\n"}}}},
		{Content: "done: bumped f to v2"},
	}
	_, st := workParent(t, root, script)
	spawn := &SpawnWorkTool{State: st}
	out, err := spawn.Exec(ctx, map[string]any{"task": "bump f", "path": "wt1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "wt1") || !strings.Contains(out, "work_1") {
		t.Fatalf("spawn must return worktree path + task id, got:\n%s", out)
	}
	if !strings.Contains(out, "begin untrusted output") {
		t.Fatalf("spawn output must be fenced, got:\n%s", out)
	}
	// Isolation: change landed in the worktree, not the main checkout.
	if b, _ := os.ReadFile(filepath.Join(root, "wt1", "f.txt")); string(b) != "v2\n" {
		t.Fatalf("worktree file wrong: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "f.txt")); string(b) != "v1\n" {
		t.Fatalf("main checkout polluted: %q", b)
	}
	apply := &ApplyWorkTool{State: st}
	aout, err := apply.Exec(ctx, map[string]any{"path": "wt1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(aout, "f.txt") || !strings.Contains(aout, "begin untrusted output") {
		t.Fatalf("apply must show the fenced diff, got:\n%s", aout)
	}
	// apply keeps the worktree on disk.
	if _, err := os.Stat(filepath.Join(root, "wt1", "f.txt")); err != nil {
		t.Fatalf("apply must keep the worktree: %v", err)
	}
	// Session closed by apply: a new spawn is allowed again.
	if _, err := spawn.Exec(ctx, map[string]any{"task": "noop", "path": "wt2"}); err != nil {
		t.Fatalf("spawn after apply must succeed: %v", err)
	}
	// Clean both worktrees (dirty refuses removal) then discard.
	gitIn(t, filepath.Join(root, "wt1"), "checkout", "--", ".")
	gitIn(t, filepath.Join(root, "wt2"), "checkout", "--", ".")
	discard := &DiscardWorkTool{State: st}
	// wt2 is the active session now; discarding wt1 must refuse naming it.
	if _, err := discard.Exec(ctx, map[string]any{"path": "wt1"}); err == nil {
		t.Fatal("discard of non-active path must refuse")
	} else if !strings.Contains(err.Error(), "wt2") {
		t.Fatalf("discard refusal must name the active session, got: %v", err)
	}
	if _, err := discard.Exec(ctx, map[string]any{"path": "wt2"}); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "wt2")); !os.IsNotExist(statErr) {
		t.Fatal("discard must remove the worktree dir")
	}
	// wt1's session already closed by apply; remove its dir directly.
	gitIn(t, root, "worktree", "remove", "--force", "--", "wt1")
}

func TestSecondSpawnRefusesWhileActive(t *testing.T) {
	root := initWorkRepo(t)
	ctx := context.Background()
	_, st := workParent(t, root, []provider.Response{{Content: "done"}})
	spawn := &SpawnWorkTool{State: st}
	if _, err := spawn.Exec(ctx, map[string]any{"task": "one", "path": "wt-a"}); err != nil {
		t.Fatal(err)
	}
	_, err := spawn.Exec(ctx, map[string]any{"task": "two", "path": "wt-b"})
	if err == nil {
		t.Fatal("second spawn while a session is active must refuse")
	}
	msg := err.Error()
	if !strings.Contains(msg, "apply_work") || !strings.Contains(msg, "discard_work") {
		t.Fatalf("refusal must name the fix (apply_work/discard_work), got: %v", err)
	}
	// Clean session is discardable directly.
	if _, err := (&DiscardWorkTool{State: st}).Exec(ctx, map[string]any{"path": "wt-a"}); err != nil {
		t.Fatal(err)
	}
}

func TestSpawnWorkSessionCap(t *testing.T) {
	root := initWorkRepo(t)
	ctx := context.Background()
	_, st := workParent(t, root, []provider.Response{{Content: "done"}})
	spawn := &SpawnWorkTool{State: st}
	discard := &DiscardWorkTool{State: st}
	for i := 0; i < 8; i++ {
		p := fmt.Sprintf("wt-cap-%d", i)
		if _, err := spawn.Exec(ctx, map[string]any{"task": "t", "path": p}); err != nil {
			t.Fatalf("call %d should pass: %v", i, err)
		}
		if _, err := discard.Exec(ctx, map[string]any{"path": p}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := spawn.Exec(ctx, map[string]any{"task": "t", "path": "wt-cap-9"}); err == nil {
		t.Fatal("9th spawn must exhaust the session budget")
	} else if !strings.Contains(err.Error(), "budget") {
		t.Fatalf("cap error must say budget, got: %v", err)
	}
}

func TestWorkChildPlanLockedExceptWrites(t *testing.T) {
	root := initWorkRepo(t)
	parent, _ := workParent(t, root, nil)
	child := NewWorkChild(parent, "t", filepath.Join(root, "wt-plan"))
	if child.GetMode() != mode.Plan {
		t.Fatal("writer child must be Plan-locked")
	}
	for _, n := range []string{"spawn_explore", "spawn_work", "apply_work", "discard_work", "git_worktree_add", "git_worktree_remove"} {
		if _, ok := child.Reg.Get(n); ok {
			t.Fatalf("writer child must not have %s", n)
		}
	}
	// Whitelisted writes pass the gate; everything mutating else blocks.
	if ok, _ := child.Reg.Gate("write_file", map[string]any{"path": "f", "content": "x"}); !ok {
		t.Fatal("write_file must be whitelisted in the writer child")
	}
	if ok, _ := child.Reg.Gate("edit_file", map[string]any{}); !ok {
		t.Fatal("edit_file must be whitelisted in the writer child")
	}
	if ok, _ := child.Reg.Gate("shell_command", map[string]any{"command": "echo hi"}); ok {
		t.Fatal("shell_command must stay blocked in the writer child")
	}
	// End to end: the blocked shell never runs, noted in the summary.
	out, err := stSpawnForGate(t, root, parent)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "wt-gate", "pwned.txt")); !os.IsNotExist(statErr) {
		t.Fatal("BREAKOUT: blocked child shell created the file")
	}
	if _, statErr := os.Stat(filepath.Join(root, "pwned.txt")); !os.IsNotExist(statErr) {
		t.Fatal("BREAKOUT: blocked child shell escaped the worktree")
	}
	if !strings.Contains(out, "read-only") && !strings.Contains(out, "blocked") {
		t.Fatalf("summary should show the block, got:\n%s", out)
	}
	gitIn(t, root, "worktree", "remove", "--force", "--", "wt-gate")
}

func stSpawnForGate(t *testing.T, root string, parent *Loop) (string, error) {
	t.Helper()
	st := &WorkState{Root: root}
	st.NewChild = func(task, workRoot string) *Loop {
		child := NewWorkChild(parent, task, workRoot)
		child.Prov = &fakeProv{script: []provider.Response{
			{ToolCalls: []provider.ToolCall{{ID: "1", Name: "shell_command", Args: map[string]any{"command": "touch pwned.txt"}}}},
			{Content: "tried my best"},
		}}
		return child
	}
	return (&SpawnWorkTool{State: st}).Exec(context.Background(), map[string]any{"task": "poke", "path": "wt-gate"})
}
