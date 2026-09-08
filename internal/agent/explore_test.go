package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tilde/internal/mode"
	"tilde/internal/policy"
	"tilde/internal/provider"
	"tilde/internal/tools"
)

func cannedLoop(summary string) *Loop {
	return &Loop{
		Prov: &fakeProv{script: []provider.Response{{Content: summary}}},
		Reg:  tools.NewRegistry(),
		Cfg:  Config{MaxIters: 3, DoomRepeats: 3, Mode: mode.Plan, Pol: &policy.Policy{}},
	}
}

func TestExploreFansOutInOrder(t *testing.T) {
	tool := &ExploreTool{NewChild: func(task string) *Loop {
		return cannedLoop("findings on " + task)
	}}
	out, err := tool.Exec(context.Background(), map[string]any{
		"tasks": []any{"checkout flow", "auth flow"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parallel exploration: 2/2 done") {
		t.Fatalf("header missing:\n%s", out)
	}
	i1, i2 := strings.Index(out, "checkout flow"), strings.Index(out, "auth flow")
	if i1 < 0 || i2 < 0 || i1 > i2 {
		t.Fatalf("task order not preserved:\n%s", out)
	}
	if !strings.Contains(out, "findings on checkout flow") || !strings.Contains(out, "findings on auth flow") {
		t.Fatalf("summaries missing:\n%s", out)
	}
}

func TestExploreSingleTaskString(t *testing.T) {
	tool := &ExploreTool{NewChild: func(task string) *Loop { return cannedLoop("s") }}
	out, err := tool.Exec(context.Background(), map[string]any{"task": "solo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1/1 done") {
		t.Fatalf("%s", out)
	}
}

func TestExploreCapsFanOut(t *testing.T) {
	tool := &ExploreTool{NewChild: func(task string) *Loop { return cannedLoop("s") }}
	var tasks []any
	for i := 0; i < 6; i++ {
		tasks = append(tasks, fmt.Sprintf("t%d", i))
	}
	if _, err := tool.Exec(context.Background(), map[string]any{"tasks": tasks}); err == nil {
		t.Fatal("expected fan-out cap error")
	}
}

func TestExploreMissingTask(t *testing.T) {
	tool := &ExploreTool{NewChild: func(task string) *Loop { return cannedLoop("s") }}
	if _, err := tool.Exec(context.Background(), map[string]any{}); err == nil {
		t.Fatal("expected missing-task error")
	}
}

func TestExploreChildFailureIsolated(t *testing.T) {
	tool := &ExploreTool{NewChild: func(task string) *Loop {
		if task == "bad" {
			return nil // factory failure
		}
		return cannedLoop("fine")
	}}
	out, err := tool.Exec(context.Background(), map[string]any{"tasks": []any{"good", "bad"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1/2 done") || !strings.Contains(out, "[failed]") {
		t.Fatalf("failure must isolate, not fail the batch:\n%s", out)
	}
}

func TestExploreChildCannotWrite(t *testing.T) {
	// A confused/malicious child model attempting writes must hit the
	// Plan gate — with the file untouched on disk.
	root := t.TempDir()
	parent := &Loop{
		Prov: &fakeProv{},
		Reg:  testRegistry(root),
		Cfg:  Config{MaxIters: 6, DoomRepeats: 6, Root: root, Mode: mode.Build, Pol: &policy.Policy{AlwaysAllow: true}},
	}
	evil := &fakeProv{script: []provider.Response{
		{ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file",
			Args: map[string]any{"path": "pwned.txt", "content": "x"}}}},
		{Content: "tried my best"},
	}}
	tool := &ExploreTool{NewChild: func(task string) *Loop {
		child := NewExploreChild(parent, task)
		child.Prov = evil
		return child
	}}
	out, err := tool.Exec(context.Background(), map[string]any{"task": "poke around"})
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "pwned.txt")); !os.IsNotExist(statErr) {
		t.Fatal("BREAKOUT: child write landed on disk")
	}
	if !strings.Contains(out, "read-only") {
		t.Fatalf("summary should show the block, got:\n%s", out)
	}
}

func TestExploreChildHasNoSpawn(t *testing.T) {
	root := t.TempDir()
	parent := &Loop{Prov: &fakeProv{}, Reg: testRegistry(root),
		Cfg: Config{Root: root, Mode: mode.Build, Pol: &policy.Policy{}}}
	child := NewExploreChild(parent, "t")
	if _, ok := child.Reg.Get("spawn_explore"); ok {
		t.Fatal("child must not spawn grandchildren")
	}
	if child.GetMode() != mode.Plan {
		t.Fatal("child must be Plan-locked")
	}
}

func TestExploreSessionCap(t *testing.T) {
	tool := &ExploreTool{NewChild: func(task string) *Loop { return cannedLoop("s") }}
	for i := 0; i < 8; i++ {
		if _, err := tool.Exec(context.Background(), map[string]any{"task": "t"}); err != nil {
			t.Fatalf("call %d should pass: %v", i, err)
		}
	}
	if _, err := tool.Exec(context.Background(), map[string]any{"task": "t"}); err == nil {
		t.Fatal("9th call must exhaust the session budget")
	}
}
