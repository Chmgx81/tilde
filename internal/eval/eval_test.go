package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tilde/internal/agent"
	"tilde/internal/mode"
	"tilde/internal/provider"
	"tilde/internal/tools"
)

var errSetupBoom = errors.New("boom")

// scriptProv replays responses per trial (fresh instance per loop).
type scriptProv struct {
	script []provider.Response
	n      int
}

func (f *scriptProv) Name() string { return "script" }
func (f *scriptProv) Chat(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (provider.Response, error) {
	if f.n >= len(f.script) {
		return provider.Response{Content: "done"}, nil
	}
	r := f.script[f.n]
	f.n++
	return r, nil
}

func writeCall(id, path, content string) provider.Response {
	return provider.Response{ToolCalls: []provider.ToolCall{{ID: id, Name: "write_file",
		Args: map[string]any{"path": path, "content": content}}}}
}

func testFactory(script []provider.Response) LoopFactory {
	return func(root string) *agent.Loop {
		reg := tools.NewRegistry()
		reg.Register(&tools.ReadFile{Root: root})
		reg.Register(&tools.WriteFile{Root: root})
		reg.Register(&tools.Shell{Root: root})
		return &agent.Loop{
			Prov: &scriptProv{script: script},
			Reg:  reg,
			Cfg:  agent.Config{MaxIters: 6, DoomRepeats: 5, Root: root, Mode: mode.Build},
		}
	}
}

func passTask() Task {
	return Task{
		Name: "t", Goal: "g", MaxIters: 6,
		Verify: func(root, _ string, _ []agent.Event) (bool, string) {
			if _, err := os.Stat(filepath.Join(root, "a.txt")); err == nil {
				return true, "ok"
			}
			return false, "missing"
		},
	}
}

func TestAllPassScoresFull(t *testing.T) {
	r := &Runner{
		Tasks:   []Task{passTask()},
		Trials:  3,
		NewLoop: testFactory([]provider.Response{writeCall("1", "a.txt", "x"), {Content: "done"}}),
	}
	reps, err := r.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 1 || reps[0].Passes != 3 || reps[0].Rate != 1.0 {
		t.Fatalf("%+v", reps)
	}
	if reps[0].MedianCall < 1 {
		t.Fatalf("trajectory must count tool calls: %+v", reps[0])
	}
}

func TestFailureDigestRecorded(t *testing.T) {
	r := &Runner{
		Tasks:  []Task{passTask()},
		Trials: 1,
		NewLoop: testFactory([]provider.Response{
			writeCall("1", "wrong.txt", "x"), {Content: "done"},
		}),
	}
	reps, err := r.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if reps[0].Passes != 0 {
		t.Fatalf("%+v", reps)
	}
	if len(reps[0].Digests) != 1 || len(reps[0].Digests[0]) == 0 {
		t.Fatalf("failing trial must carry a trajectory digest: %+v", reps[0])
	}
}

func TestAllFailCollectsReasons(t *testing.T) {
	r := &Runner{
		Tasks:   []Task{passTask()},
		Trials:  2,
		NewLoop: testFactory([]provider.Response{{Content: "I refuse"}}),
	}
	reps, err := r.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if reps[0].Passes != 0 || reps[0].Rate != 0 {
		t.Fatalf("%+v", reps)
	}
	if len(reps[0].Reasons) == 0 {
		t.Fatal("failure reasons must be collected")
	}
}

func TestFreshDirPerTrial(t *testing.T) {
	// Isolation proof: every trial must run in a distinct directory, so
	// no file can leak from one trial into the next.
	var dirs []string
	r := &Runner{
		Tasks: []Task{{
			Name: "t", Goal: "g", MaxIters: 6,
			Verify: func(root, _ string, _ []agent.Event) (bool, string) {
				dirs = append(dirs, root)
				return true, "ok"
			},
		}},
		Trials:  3,
		NewLoop: testFactory([]provider.Response{{Content: "done"}}),
	}
	if _, err := r.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, d := range dirs {
		if seen[d] {
			t.Fatalf("trial dir reused: %s", d)
		}
		seen[d] = true
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Fatalf("trial dir not cleaned: %s", d)
		}
	}
}

func TestTrialTimeoutIsFailureNotHang(t *testing.T) {
	r := &Runner{
		Tasks:        []Task{passTask()},
		Trials:       1,
		TrialTimeout: time.Millisecond,
		NewLoop: func(root string) *agent.Loop {
			reg := tools.NewRegistry()
			reg.Register(&tools.Shell{Root: root})
			hang := &hangProv{}
			return &agent.Loop{Prov: hang, Reg: reg,
				Cfg: agent.Config{MaxIters: 6, DoomRepeats: 5, Root: root, Mode: 1}}
		},
	}
	start := time.Now()
	reps, err := r.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 60*time.Second {
		t.Fatal("trial hung past its timeout")
	}
	if reps[0].Passes != 0 {
		t.Fatalf("timed-out trial must fail: %+v", reps)
	}
}

type hangProv struct{}

func (h *hangProv) Name() string { return "hang" }
func (h *hangProv) Chat(ctx context.Context, _ []provider.Message, _ []provider.ToolDef) (provider.Response, error) {
	<-ctx.Done()
	return provider.Response{}, ctx.Err()
}

func TestSetupFailureContinues(t *testing.T) {
	bad := Task{
		Name: "bad", Goal: "g", MaxIters: 6,
		Setup: func(string) error { return errSetupBoom },
		Verify: func(_, _ string, _ []agent.Event) (bool, string) {
			return true, "ok"
		},
	}
	good := passTask()
	good.Name = "good"
	r := &Runner{
		Tasks:   []Task{bad, good},
		Trials:  2,
		NewLoop: testFactory([]provider.Response{writeCall("1", "a.txt", "x"), {Content: "done"}}),
	}
	reps, err := r.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("setup failure must not abort suite: %v", err)
	}
	if len(reps) != 2 {
		t.Fatalf("both reports expected: %+v", reps)
	}
	if reps[0].Passes != 0 || len(reps[0].Reasons) == 0 {
		t.Fatalf("bad task must record 0-pass report with reason: %+v", reps[0])
	}
	if reps[1].Passes != 2 {
		t.Fatalf("runner must continue past setup failure: %+v", reps[1])
	}
}
