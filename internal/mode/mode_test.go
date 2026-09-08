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
}
