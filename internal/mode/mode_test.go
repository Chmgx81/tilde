package mode

import "testing"

func TestCycle(t *testing.T) {
	if Plan.Cycle() != Build || Build.Cycle() != Auto || Auto.Cycle() != Plan {
		t.Fatalf("bad cycle: %v %v %v", Plan.Cycle(), Build.Cycle(), Auto.Cycle())
	}
}

func TestPlanGate(t *testing.T) {
	if err := Plan.AllowedCall("shell_poll", map[string]any{"action": "kill"}); err == nil {
		t.Fatal("Plan must block shell_poll kill")
	}
	if err := Plan.AllowedCall("shell_poll", map[string]any{"action": "log"}); err != nil {
		t.Fatalf("Plan must allow shell_poll log: %v", err)
	}
	if err := Plan.AllowedCall("write_file", nil); err == nil {
		t.Fatal("Plan must block write_file")
	}
	if err := Build.AllowedCall("write_file", nil); err != nil {
		t.Fatalf("Build must allow write_file: %v", err)
	}
	if !IsMutatingCall("shell_poll", map[string]any{"action": "kill"}) {
		t.Fatal("kill must be mutating")
	}
	if IsMutatingCall("shell_poll", map[string]any{"action": "status"}) {
		t.Fatal("status must be read-only")
	}
	// Worktree writers: spawn/discard mutate (.git worktrees), apply
	// only reviews a diff — same split as git_worktree_add/remove.
	if err := Plan.AllowedCall("spawn_work", nil); err == nil {
		t.Fatal("Plan must block spawn_work")
	}
	if err := Plan.AllowedCall("discard_work", nil); err == nil {
		t.Fatal("Plan must block discard_work")
	}
	if err := Plan.AllowedCall("apply_work", nil); err != nil {
		t.Fatalf("Plan must allow apply_work (read-only review): %v", err)
	}
	if err := Build.AllowedCall("spawn_work", nil); err != nil {
		t.Fatalf("Build must allow spawn_work: %v", err)
	}
	// Memory is op-aware: recall reads, save/forget write.
	if err := Plan.AllowedCall("memory", map[string]any{"op": "recall"}); err != nil {
		t.Fatalf("Plan must allow memory recall: %v", err)
	}
	if err := Plan.AllowedCall("memory", map[string]any{"op": "save"}); err == nil {
		t.Fatal("Plan must block memory save")
	}
	if err := Plan.AllowedCall("memory", map[string]any{"op": "forget"}); err == nil {
		t.Fatal("Plan must block memory forget")
	}
	// remember: index rebuilds the vector store; recall/status read it.
	if err := Plan.AllowedCall("remember", map[string]any{"op": "recall"}); err != nil {
		t.Fatalf("Plan must allow remember recall: %v", err)
	}
	if err := Plan.AllowedCall("remember", map[string]any{"op": "status"}); err != nil {
		t.Fatalf("Plan must allow remember status: %v", err)
	}
	if err := Plan.AllowedCall("remember", map[string]any{"op": "index"}); err == nil {
		t.Fatal("Plan must block remember index")
	}
	// save_plan is Plan-visible by design: absent from the mutating set,
	// so Plan mode allows it (verified here, never by weakening a gate).
	if IsMutating("save_plan") {
		t.Fatal("save_plan must not be mutating")
	}
	if err := Plan.Allowed("save_plan"); err != nil {
		t.Fatalf("Plan must allow save_plan: %v", err)
	}
	if err := Plan.AllowedCall("save_plan", map[string]any{"title": "t", "content": "c"}); err != nil {
		t.Fatalf("Plan must allow save_plan call: %v", err)
	}
}
