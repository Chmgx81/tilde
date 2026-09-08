package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tilde/internal/compact"
	"tilde/internal/mode"
	"tilde/internal/policy"
	"tilde/internal/provider"
	"tilde/internal/skills"
	"tilde/internal/tools"
)

// fakeProv replays a scripted response sequence.
type fakeProv struct {
	script []provider.Response
	n      int
	err    error
}

func (f *fakeProv) Name() string { return "fake" }
func (f *fakeProv) Chat(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (provider.Response, error) {
	if f.err != nil {
		return provider.Response{}, f.err
	}
	if f.n >= len(f.script) {
		return provider.Response{Content: "done"}, nil
	}
	r := f.script[f.n]
	f.n++
	return r, nil
}

func testRegistry(root string) *tools.Registry {
	reg := tools.NewRegistry()
	reg.Register(&tools.ReadFile{Root: root})
	reg.Register(&tools.WriteFile{Root: root})
	reg.Register(&tools.EditFile{Root: root})
	reg.Register(&tools.Shell{Root: root})
	reg.Register(&tools.Grep{Root: root})
	reg.Register(&tools.Glob{Root: root})
	reg.Register(&tools.GitStatus{Root: root})
	reg.Register(&tools.GitDiff{Root: root})
	return reg
}

func TestPlanModeBlocksMutatingTools(t *testing.T) {
	root := t.TempDir()
	reg := testRegistry(root)
	loop := &Loop{
		Prov: &fakeProv{script: []provider.Response{
			{Content: "trying", ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Args: map[string]any{"path": "x.txt", "content": "hi"}}}},
			{Content: "ok"},
		}},
		Reg: reg,
		Cfg: Config{MaxIters: 5, DoomRepeats: 5, Root: root, Mode: mode.Plan, Pol: &policy.Policy{}},
	}
	var events []Event
	_, err := loop.Run(context.Background(), "goal", func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Kind == "tool_result" && strings.Contains(e.Text, "Plan mode is read-only") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Plan-mode block message, events: %+v", events)
	}
	// Plan mode is impossible to escape from the inside: nothing was written.
	if _, statErr := os.Stat(filepath.Join(root, "x.txt")); !os.IsNotExist(statErr) {
		t.Fatal("BREAKOUT: Plan-blocked write_file created the file")
	}
}

func TestRegistryGateBlocksEvenIfLoopIsConfused(t *testing.T) {
	// Defense in depth: the registry gate (what main.go installs) blocks
	// mutating tools in Plan mode independently of the loop's own check.
	root := t.TempDir()
	reg := testRegistry(root)
	plan := mode.Plan
	reg.Gate = func(toolName string, _ map[string]any) (bool, string) {
		if err := plan.Allowed(toolName); err != nil {
			return false, err.Error()
		}
		return true, ""
	}
	out := reg.Dispatch(context.Background(), "write_file", map[string]any{"path": "y.txt", "content": "hi"})
	if !strings.Contains(out, "blocked") || !strings.Contains(out, "Plan mode") {
		t.Fatalf("expected gate block, got %q", out)
	}
	if _, statErr := os.Stat(filepath.Join(root, "y.txt")); !os.IsNotExist(statErr) {
		t.Fatal("BREAKOUT: gated write_file created the file")
	}
}

func TestDoomLoopHandsOffToPlan(t *testing.T) {
	// Mutating repeats fail closed exactly as before: handoff + Plan.
	root := t.TempDir()
	reg := testRegistry(root)
	repeat := provider.Response{ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Args: map[string]any{"path": "x.txt", "content": "hi"}}}}
	loop := &Loop{
		Prov: &fakeProv{script: []provider.Response{repeat, repeat, repeat, repeat}},
		Reg:  reg,
		Cfg:  Config{MaxIters: 10, DoomRepeats: 3, Root: root, Mode: mode.Build, Pol: &policy.Policy{AlwaysAllow: true}},
	}
	var events []Event
	handoff := false
	_, err := loop.Run(context.Background(), "goal", func(e Event) {
		events = append(events, e)
		if e.Kind == "handoff" {
			handoff = true
		}
	})
	if err == nil {
		t.Fatal("expected doom-loop error")
	}
	if loop.Cfg.Mode != mode.Plan {
		t.Fatal("expected fail-closed drop to Plan mode")
	}
	if !handoff {
		t.Fatal("expected handoff event")
	}
}

func TestDoomReadOnlyNudgesThenContinues(t *testing.T) {
	// Read-only repeats (nothing at risk) get a nudge naming the recovery
	// and the turn continues — no handoff, no mode drop.
	root := t.TempDir()
	reg := testRegistry(root)
	repeat := provider.Response{ToolCalls: []provider.ToolCall{{ID: "1", Name: "glob", Args: map[string]any{"pattern": "zzz"}}}}
	loop := &Loop{
		Prov: &fakeProv{script: []provider.Response{repeat, repeat, repeat, {Content: "done"}}},
		Reg:  reg,
		Cfg:  Config{MaxIters: 10, DoomRepeats: 3, Root: root, Mode: mode.Build, Pol: &policy.Policy{AlwaysAllow: true}},
	}
	var events []Event
	nudged := false
	handoff := false
	_, err := loop.Run(context.Background(), "goal", func(e Event) {
		events = append(events, e)
		if e.Kind == "tool_result" && strings.Contains(e.Text, "already in context") {
			nudged = true
		}
		if e.Kind == "handoff" {
			handoff = true
		}
	})
	if err != nil {
		t.Fatalf("read-only repeat must not hand off: %v", err)
	}
	if loop.GetMode() != mode.Build {
		t.Fatal("mode must stay Build after a nudge")
	}
	if !nudged {
		t.Fatal("expected nudge tool_result")
	}
	if handoff {
		t.Fatal("no handoff expected on a single read-only run")
	}
}

func TestDoomReadOnlyNudgeEscalates(t *testing.T) {
	// Two nudges, then the third identical-repeat trigger hands off like
	// before: 3+3+3 repeats of the same read-only call.
	root := t.TempDir()
	reg := testRegistry(root)
	repeat := provider.Response{ToolCalls: []provider.ToolCall{{ID: "1", Name: "glob", Args: map[string]any{"pattern": "zzz"}}}}
	script := []provider.Response{repeat, repeat, repeat, repeat, repeat, repeat, repeat, repeat, repeat}
	loop := &Loop{
		Prov: &fakeProv{script: script},
		Reg:  reg,
		Cfg:  Config{MaxIters: 12, DoomRepeats: 3, Root: root, Mode: mode.Build, Pol: &policy.Policy{AlwaysAllow: true}},
	}
	nudges, handoffs := 0, 0
	_, err := loop.Run(context.Background(), "goal", func(e Event) {
		if e.Kind == "tool_result" && strings.Contains(e.Text, "already in context") {
			nudges++
		}
		if e.Kind == "handoff" {
			handoffs++
		}
	})
	if err == nil {
		t.Fatal("expected escalation handoff after nudges exhausted")
	}
	if nudges != 2 {
		t.Fatalf("expected exactly 2 nudges, got %d", nudges)
	}
	if handoffs != 1 {
		t.Fatalf("expected exactly 1 handoff, got %d", handoffs)
	}
	if loop.GetMode() != mode.Plan {
		t.Fatal("escalation must drop to Plan")
	}
}

func TestDoomReadOnlyNudgeBoundedByCap(t *testing.T) {
	// A model that never varies still terminates: the iteration cap
	// backstops the nudge path and drops to Plan.
	root := t.TempDir()
	reg := testRegistry(root)
	repeat := provider.Response{ToolCalls: []provider.ToolCall{{ID: "1", Name: "grep", Args: map[string]any{"pattern": "zzz"}}}}
	var script []provider.Response
	for i := 0; i < 8; i++ {
		script = append(script, repeat)
	}
	loop := &Loop{
		Prov: &fakeProv{script: script},
		Reg:  reg,
		Cfg:  Config{MaxIters: 5, DoomRepeats: 3, Root: root, Mode: mode.Build, Pol: &policy.Policy{AlwaysAllow: true}},
	}
	_, err := loop.Run(context.Background(), "goal", func(Event) {})
	if err == nil {
		t.Fatal("expected termination, got nil error (hang risk)")
	}
	if loop.GetMode() != mode.Plan {
		t.Fatal("capped run must fail closed to Plan")
	}
}

func TestMultiFileTaskWithFakeProvider(t *testing.T) {
	root := t.TempDir()
	reg := testRegistry(root)
	loop := &Loop{
		Prov: &fakeProv{script: []provider.Response{
			{ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Args: map[string]any{"path": "a.txt", "content": "one"}}}},
			{ToolCalls: []provider.ToolCall{{ID: "2", Name: "write_file", Args: map[string]any{"path": "b.txt", "content": "two"}}}},
			{ToolCalls: []provider.ToolCall{{ID: "3", Name: "shell_command", Args: map[string]any{"command": "cat a.txt b.txt"}}}},
			{Content: "both files written and verified"},
		}},
		Reg: reg,
		Cfg: Config{MaxIters: 10, DoomRepeats: 3, Root: root, Mode: mode.Build,
			Pol: &policy.Policy{AlwaysAllow: true}, AskUser: func(string, map[string]any) bool { return true }},
	}
	text, err := loop.Run(context.Background(), "write two files", func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "verified") {
		t.Fatalf("unexpected final text %q", text)
	}
}

func TestAutoCompactionFiresBeforeOverflow(t *testing.T) {
	root := t.TempDir()
	reg := testRegistry(root)
	big := strings.Repeat("filler-finding ", 60) // ~900 chars ≈ 225 tokens
	glob := provider.ToolCall{ID: "g", Name: "glob", Args: map[string]any{"pattern": "*.go"}}
	loop := &Loop{
		Prov: &fakeProv{script: []provider.Response{
			{Content: big, ToolCalls: []provider.ToolCall{glob}},
			{Content: big, ToolCalls: []provider.ToolCall{glob}},
			{Content: "final answer"},
		}},
		Reg: reg,
		Cfg: Config{MaxIters: 5, DoomRepeats: 5, Root: root, Mode: mode.Build,
			Pol: &policy.Policy{AlwaysAllow: true},
			Compactor: &compact.Compactor{Budget: 500, KeepRecent: 2,
				Summarize: func(_ context.Context, _ []provider.Message) (string, error) {
					return "test-summary", nil
				}}},
	}
	var kinds []string
	var compacted []string
	_, err := loop.Run(context.Background(), "long Formulas task", func(e Event) {
		kinds = append(kinds, e.Kind)
		if e.Kind == "compacted" {
			compacted = append(compacted, e.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(compacted) == 0 {
		t.Fatalf("no compaction fired under a 500-token budget; events: %v", kinds)
	}
	if !strings.Contains(compacted[0], "see session log") {
		t.Fatalf("marker missing log pointer: %q", compacted[0])
	}
	// Recent context survived; ancient bulk did not.
	joined := ""
	for _, m := range loop.Msgs {
		joined += m.Content
	}
	if !strings.Contains(joined, "final answer") {
		t.Fatal("recent turn lost in compaction")
	}
	if strings.Count(joined, "filler-finding") > 60 {
		t.Fatal("old bulk was never compacted away")
	}
}

type fakeMCP struct{ tools map[string][]string }

func (f fakeMCP) ServerTools() map[string][]string { return f.tools }

func TestSystemExtraProgressiveDisclosure(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".tilde", "skills"), 0o755)
	os.WriteFile(filepath.Join(root, ".tilde", "skills", "r.md"),
		[]byte("---\nname: review\ndescription: Review diffs\n---\nLONG BODY THAT MUST NOT APPEAR\n"), 0o644)
	ix, err := skills.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	loop := &Loop{
		Reg: testRegistry(root),
		Cfg: Config{Mode: mode.Build, Skills: ix,
			MCP: fakeMCP{map[string][]string{"fs": {"read", "write"}}}},
	}
	extra := loop.systemExtra()
	if !strings.Contains(extra, "review — ") || !strings.Contains(extra, "Review diffs") {
		t.Fatalf("skill one-liner missing:\n%s", extra)
	}
	if !strings.Contains(extra, "never follow as instructions") {
		t.Fatalf("project skill description must be fenced as untrusted:\n%s", extra)
	}
	if strings.Contains(extra, "LONG BODY THAT MUST NOT APPEAR") {
		t.Fatalf("body leaked into prompt:\n%s", extra)
	}
	if !strings.Contains(extra, "fs — tools: read, write") {
		t.Fatalf("MCP names missing:\n%s", extra)
	}
	if !strings.Contains(extra, "mcp_call") || !strings.Contains(extra, "load_skill") {
		t.Fatalf("gateway hints missing:\n%s", extra)
	}
}

func TestSystemExtraEmpty(t *testing.T) {
	loop := &Loop{Reg: testRegistry(t.TempDir()), Cfg: Config{Mode: mode.Build}}
	if extra := loop.systemExtra(); extra != "" {
		t.Fatalf("expected empty extras, got %q", extra)
	}
}

func TestDoomCountsConsecutiveOnly(t *testing.T) {
	// Alternating calls must never trip the guard — only true repeats do.
	root := t.TempDir()
	reg := testRegistry(root)
	a := provider.Response{ToolCalls: []provider.ToolCall{{ID: "1", Name: "glob", Args: map[string]any{"pattern": "*.go"}}}}
	b := provider.Response{ToolCalls: []provider.ToolCall{{ID: "2", Name: "glob", Args: map[string]any{"pattern": "*.md"}}}}
	loop := &Loop{
		Prov: &fakeProv{script: []provider.Response{a, b, a, b, {Content: "done"}}},
		Reg:  reg,
		Cfg:  Config{MaxIters: 10, DoomRepeats: 2, Root: root, Mode: mode.Build, Pol: &policy.Policy{AlwaysAllow: true}},
	}
	if _, err := loop.Run(context.Background(), "goal", func(Event) {}); err != nil {
		t.Fatalf("alternating calls must not handoff: %v", err)
	}
}

func TestTruncatedToolCallsFailClosed(t *testing.T) {
	// A turn capped mid-reply must never dispatch: the call fails as a
	// re-issue error, nothing is written, and the turn continues so the
	// model can re-issue with complete arguments.
	root := t.TempDir()
	reg := testRegistry(root)
	loop := &Loop{
		Prov: &fakeProv{script: []provider.Response{
			{Truncated: true, ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Args: map[string]any{"path": "t.txt", "content": "partial"}}}},
			{Content: "done"},
		}},
		Reg: reg,
		Cfg: Config{MaxIters: 5, DoomRepeats: 5, Root: root, Mode: mode.Build,
			Pol: &policy.Policy{AlwaysAllow: true}, AskUser: func(string, map[string]any) bool { return true }},
	}
	var events []Event
	text, err := loop.Run(context.Background(), "goal", func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	if text != "done" {
		t.Fatalf("turn must continue after the truncated failure, got %q", text)
	}
	if _, statErr := os.Stat(filepath.Join(root, "t.txt")); !os.IsNotExist(statErr) {
		t.Fatal("BREAKOUT: truncated write_file must not execute")
	}
	found := false
	for _, e := range events {
		if e.Kind == "tool_result" && strings.Contains(e.Text, "arguments may be truncated") && strings.Contains(e.Text, "re-issue") {
			found = true
		}
		if e.Kind == "tool_call" {
			t.Fatalf("truncated turn must emit no tool_call events, got %q", e.Text)
		}
	}
	if !found {
		t.Fatalf("expected truncated re-issue tool_result, events: %+v", events)
	}
	joined := ""
	for _, m := range loop.Msgs {
		joined += m.Content + "\n"
	}
	if !strings.Contains(joined, "arguments may be truncated") {
		t.Fatal("re-issue error must land in context for the retry")
	}
}

func TestFingerprintDeterministic(t *testing.T) {
	args := map[string]any{"z": "1", "a": "2", "m": "3"}
	first := fingerprint("glob", args)
	for i := 0; i < 50; i++ {
		if fingerprint("glob", args) != first {
			t.Fatal("fingerprint varies run to run — doom guard would misfire")
		}
	}
	if fingerprint("glob", map[string]any{"pattern": "x"}) == fingerprint("glob", map[string]any{"pattern": "y"}) {
		t.Fatal("distinct calls collide")
	}
}

func TestConcurrentModeAndContext(t *testing.T) { // The render thread (Tab, handoff, resume) mutates mode + context
	// while Run works: run under -race to prove the mutex holds.
	root := t.TempDir()
	reg := testRegistry(root)
	loop := &Loop{
		Prov: &fakeProv{script: []provider.Response{
			{Content: "one"},
			{Content: "two"},
			{Content: "three"},
		}},
		Reg: reg,
		Cfg: Config{MaxIters: 10, DoomRepeats: 10, Root: root, Mode: mode.Build, Pol: &policy.Policy{}},
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			loop.SetMode(mode.Build)
			loop.SetMode(mode.Plan)
			_ = loop.GetMode()
			loop.AppendMsg(provider.Message{Role: "user", Content: "poke"})
			_ = loop.MsgsSnapshot()
		}
	}()
	_, _ = loop.Run(context.Background(), "goal", func(Event) {})
	<-done
}

func TestParallelBatchKeepsOrder(t *testing.T) {
	// One turn, three read-only calls: all must run, results in order.
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("A\n"), 0o644)
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("B\n"), 0o644)
	reg := testRegistry(root)
	loop := &Loop{
		Prov: &fakeProv{script: []provider.Response{{
			ToolCalls: []provider.ToolCall{
				{ID: "1", Name: "read_file", Args: map[string]any{"path": "a.txt"}},
				{ID: "2", Name: "read_file", Args: map[string]any{"path": "b.txt"}},
				{ID: "3", Name: "glob", Args: map[string]any{"pattern": "*.txt"}},
			}},
			{Content: "done"},
		}},
		Reg: reg,
		Cfg: Config{MaxIters: 5, DoomRepeats: 5, Root: root, Mode: mode.Build, Pol: &policy.Policy{AlwaysAllow: true}},
	}
	var calls []string
	_, err := loop.Run(context.Background(), "goal", func(e Event) {
		if e.Kind == "tool_call" {
			calls = append(calls, e.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("calls=%v", calls)
	}
	// Request order preserved on the timeline despite concurrency.
	if !strings.HasPrefix(calls[0], "read_file a") || !strings.HasPrefix(calls[1], "read_file b") || !strings.HasPrefix(calls[2], "glob") {
		t.Fatalf("order broken: %v", calls)
	}
	joined := ""
	for _, m := range loop.Msgs {
		joined += m.Content + "\n"
	}
	if !strings.Contains(joined, "A") || !strings.Contains(joined, "B") {
		t.Fatal("results missing from context")
	}
}

func TestAutoModeApprovesAskTier(t *testing.T) {
	// Auto must actually auto-approve Ask-tier calls live at dispatch:
	// AskUser denies, AlwaysAllow is off, yet the write lands.
	root := t.TempDir()
	reg := testRegistry(root)
	loop := &Loop{
		Prov: &fakeProv{script: []provider.Response{
			{ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Args: map[string]any{"path": "auto.txt", "content": "x"}}}},
			{Content: "done"},
		}},
		Reg: reg,
		Cfg: Config{MaxIters: 5, DoomRepeats: 5, Root: root, Mode: mode.Auto,
			Pol: &policy.Policy{}, AskUser: func(string, map[string]any) bool { return false }},
	}
	if _, err := loop.Run(context.Background(), "goal", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "auto.txt")); err != nil {
		t.Fatal("Auto mode did not auto-approve the Ask-tier call")
	}
}

func TestShellPollKillBlockedInPlan(t *testing.T) {
	root := t.TempDir()
	reg := testRegistry(root)
	reg.Register(&tools.ShellPoll{Tasks: &tools.TaskManager{}})
	mkLoop := func(tc provider.ToolCall) *Loop {
		return &Loop{
			Prov: &fakeProv{script: []provider.Response{
				{ToolCalls: []provider.ToolCall{tc}},
				{Content: "done"},
			}},
			Reg: reg,
			Cfg: Config{MaxIters: 5, DoomRepeats: 5, Root: root, Mode: mode.Plan, Pol: &policy.Policy{}},
		}
	}
	run := func(tc provider.ToolCall) []Event {
		var events []Event
		_, _ = mkLoop(tc).Run(context.Background(), "goal", func(e Event) { events = append(events, e) })
		return events
	}
	blocked := func(e Event) bool {
		return e.Kind == "tool_result" && strings.Contains(e.Text, "Plan mode is read-only")
	}
	kill := run(provider.ToolCall{ID: "1", Name: "shell_poll", Args: map[string]any{"action": "kill", "task_id": "t1"}})
	found := false
	for _, e := range kill {
		if blocked(e) {
			found = true
		}
	}
	if !found {
		t.Fatalf("shell_poll kill must be Plan-blocked, events: %+v", kill)
	}
	for _, args := range []map[string]any{
		{"action": "status", "task_id": "t1"},
		{"action": "log", "task_id": "t1"},
	} {
		for _, e := range run(provider.ToolCall{ID: "1", Name: "shell_poll", Args: args}) {
			if blocked(e) {
				t.Fatalf("shell_poll %v must stay readable in Plan: %+v", args["action"], e)
			}
		}
	}
}

func TestIterationCapDropsToPlan(t *testing.T) {
	root := t.TempDir()
	reg := testRegistry(root)
	loop := &Loop{
		Prov: &fakeProv{script: []provider.Response{
			{ToolCalls: []provider.ToolCall{{ID: "1", Name: "glob", Args: map[string]any{"pattern": "*.go"}}}},
		}},
		Reg: reg,
		Cfg: Config{MaxIters: 1, DoomRepeats: 5, Root: root, Mode: mode.Build, Pol: &policy.Policy{AlwaysAllow: true}},
	}
	if _, err := loop.Run(context.Background(), "goal", func(Event) {}); err == nil {
		t.Fatal("expected cap error")
	}
	if loop.GetMode() != mode.Plan {
		t.Fatal("capped run must fail closed to Plan like the doom path")
	}
}

// hangTool ignores ctx (worst case) so the cancellable wait is exercised.
type hangTool struct{ d time.Duration }

func (h *hangTool) Name() string        { return "hang" }
func (h *hangTool) Description() string { return "test sleeper" }
func (h *hangTool) Schema() map[string]any {
	return map[string]any{"type": "object"}
}
func (h *hangTool) Exec(_ context.Context, _ map[string]any) (string, error) {
	time.Sleep(h.d)
	return "late", nil
}

func TestFlushBatchCancelReturnsCollected(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&hangTool{d: 500 * time.Millisecond})
	loop := &Loop{Reg: reg}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	batch := []provider.ToolCall{
		{ID: "1", Name: "hang", Args: map[string]any{}},
		{ID: "2", Name: "hang", Args: map[string]any{}},
	}
	var calls []string
	start := time.Now()
	err := loop.flushBatch(ctx, batch, func(e Event) {
		if e.Kind == "tool_call" {
			calls = append(calls, e.Text)
		}
	})
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("cancelled batch hung %v instead of returning collected-so-far", elapsed)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	// Request order preserved even on the cancel path.
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "hang") || !strings.HasPrefix(calls[1], "hang") {
		t.Fatalf("order broken on cancel: %v", calls)
	}
}

func TestCompactNowNoopWhenNothingDropped(t *testing.T) {
	loop := &Loop{Msgs: []provider.Message{{Role: "user", Content: "hi"}}}
	marker, dropped := loop.CompactNow(context.Background(), "")
	if dropped != 0 || marker != "" {
		t.Fatalf("expected no marker and no drop, got %q, %d", marker, dropped)
	}
	if len(loop.Msgs) != 1 {
		t.Fatalf("context must be untouched, got %+v", loop.Msgs)
	}
}

func TestWriteBreaksBatch(t *testing.T) {
	// A mutating call between reads forces serial barriers: read, write,
	// read — each in request order.
	root := t.TempDir()
	reg := testRegistry(root)
	loop := &Loop{
		Prov: &fakeProv{script: []provider.Response{{
			ToolCalls: []provider.ToolCall{
				{ID: "1", Name: "glob", Args: map[string]any{"pattern": "*.go"}},
				{ID: "2", Name: "write_file", Args: map[string]any{"path": "n.txt", "content": "x"}},
				{ID: "3", Name: "glob", Args: map[string]any{"pattern": "*.txt"}},
			}},
			{Content: "done"},
		}},
		Reg: reg,
		Cfg: Config{MaxIters: 5, DoomRepeats: 5, Root: root, Mode: mode.Build,
			Pol: &policy.Policy{AlwaysAllow: true}, AskUser: func(string, map[string]any) bool { return true }},
	}
	var calls []string
	if _, err := loop.Run(context.Background(), "goal", func(e Event) {
		if e.Kind == "tool_call" {
			calls = append(calls, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || !strings.HasPrefix(calls[0], "glob") ||
		!strings.HasPrefix(calls[1], "write_file") || !strings.HasPrefix(calls[2], "glob") {
		t.Fatalf("barrier order broken: %v", calls)
	}
	if _, err := os.Stat(filepath.Join(root, "n.txt")); err != nil {
		t.Fatal("write did not land")
	}
}

func TestThinkingEmittedAheadOfReply(t *testing.T) {
	root := t.TempDir()
	loop := &Loop{
		Prov: &fakeProv{script: []provider.Response{
			{Thinking: "why x", Content: "doing x"},
		}},
		Reg: testRegistry(root),
		Cfg: Config{MaxIters: 5, DoomRepeats: 5, Root: root, Mode: mode.Build, Pol: &policy.Policy{AlwaysAllow: true}},
	}
	var kinds []string
	if _, err := loop.Run(context.Background(), "goal", func(e Event) { kinds = append(kinds, e.Kind) }); err != nil {
		t.Fatal(err)
	}
	think, say := -1, -1
	for i, k := range kinds {
		if k == "thinking" && think < 0 {
			think = i
		}
		if k == "assistant" && say < 0 {
			say = i
		}
	}
	if think < 0 || say < 0 || think > say {
		t.Fatalf("thinking must surface ahead of the reply, kinds=%v", kinds)
	}
	// Reasoning is display + log, never context: the next turn must not
	// re-read stale rationale as fresh instruction.
	for _, m := range loop.Msgs {
		if strings.Contains(m.Content, "why x") {
			t.Fatalf("thinking leaked into context: %q", m.Content)
		}
	}
}
