package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"tilde/internal/agent"
	"tilde/internal/mode"
	"tilde/internal/policy"
)

func confirmKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func shellLoop() (*agent.Loop, *policy.Policy) {
	pol := &policy.Policy{}
	return &agent.Loop{Cfg: agent.Config{Pol: pol}}, pol
}

func TestConfirmAlwaysShellEscapeRecords(t *testing.T) {
	loop, pol := shellLoop()
	m := New(loop, mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.pendingShell = "go test ./..."
	m.confirm = &confirmState{Tool: "shell_command", Args: map[string]any{"command": "go test ./..."}}
	nm, cmd := m.updateConfirm(confirmKey('a'))
	after := nm.(Model)
	if after.pendingShell != "" || after.confirm != nil {
		t.Fatal("approved prompt must clear")
	}
	if cmd == nil {
		t.Fatal("approved escape must return the runner cmd (like [y])")
	}
	if got := pol.Check("shell_command", map[string]any{"command": "go test ./..."}); got != policy.Allow {
		t.Fatalf("exact command must be session-approved: got %v", got)
	}
	if got := pol.Check("shell_command", map[string]any{"command": "go test ./... -run X"}); got != policy.Ask {
		t.Fatalf("different args must still prompt: got %v", got)
	}
	found := false
	for _, ln := range after.lines {
		if strings.Contains(ln, "always this session") {
			found = true
		}
	}
	if !found {
		t.Fatal("transcript must name the session approval")
	}
}

func TestConfirmAlwaysAgentPathApproves(t *testing.T) {
	loop, pol := shellLoop()
	m := New(loop, mode.Plan, t.TempDir(), "ollama/m", 32000)
	done := make(chan bool, 1)
	m.confirm = &confirmState{Tool: "shell_command", Args: map[string]any{"command": "go test ./..."}, Done: done}
	nm, _ := m.updateConfirm(confirmKey('a'))
	after := nm.(Model)
	if after.confirm != nil {
		t.Fatal("approved prompt must clear")
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("Done must carry approval (true), like [y]")
		}
	default:
		t.Fatal("Done must receive approval without blocking")
	}
	if got := pol.Check("shell_command", map[string]any{"command": "go test ./..."}); got != policy.Allow {
		t.Fatalf("exact command must be session-approved: got %v", got)
	}
}

func TestConfirmAlwaysIgnoresNonShell(t *testing.T) {
	loop, pol := shellLoop()
	m := New(loop, mode.Plan, t.TempDir(), "ollama/m", 32000)
	done := make(chan bool, 1)
	m.confirm = &confirmState{Tool: "mcp_call", Args: map[string]any{"server": "fs", "tool": "read"}, Done: done}
	nm, _ := m.updateConfirm(confirmKey('a'))
	after := nm.(Model)
	if after.confirm == nil {
		t.Fatal("[a] must not resolve a non-shell prompt")
	}
	select {
	case v := <-done:
		t.Fatalf("[a] must not answer a non-shell prompt (got %v)", v)
	default:
	}
	if got := pol.Check("mcp_call", map[string]any{"server": "fs", "tool": "read"}); got != policy.Ask {
		t.Fatalf("mcp_call must stay Ask: got %v", got)
	}
}

func TestConfirmAlwaysNilPolicyApprovesOnce(t *testing.T) {
	m := New(&agent.Loop{}, mode.Plan, t.TempDir(), "ollama/m", 32000)
	done := make(chan bool, 1)
	m.confirm = &confirmState{Tool: "shell_command", Args: map[string]any{"command": "go test ./..."}, Done: done}
	nm, _ := m.updateConfirm(confirmKey('a'))
	if nm.(Model).confirm != nil {
		t.Fatal("nil policy must still approve once (no store, no crash)")
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("Done must carry approval")
		}
	default:
		t.Fatal("Done must receive approval")
	}
}

func TestConfirmAlwaysUppercase(t *testing.T) {
	loop, _ := shellLoop()
	m := New(loop, mode.Plan, t.TempDir(), "ollama/m", 32000)
	done := make(chan bool, 1)
	m.confirm = &confirmState{Tool: "shell_command", Args: map[string]any{"command": "go test ./..."}, Done: done}
	nm, _ := m.updateConfirm(confirmKey('A'))
	if nm.(Model).confirm != nil {
		t.Fatal("key matching is case-insensitive (like y/n): [A] must approve")
	}
}

func TestConfirmFooterShellOnly(t *testing.T) {
	shell := confirmFooter("shell_command", map[string]any{"command": "go test ./..."}, 100, false)
	if !strings.Contains(strings.ReplaceAll(shell, "\n", " "), "[a] always this session (exact command)") {
		t.Fatalf("shell footer must offer [a]:\n%s", shell)
	}
	if !strings.Contains(shell, "[y] approve once") || !strings.Contains(shell, "[n] deny") {
		t.Fatalf("footer must keep y/n:\n%s", shell)
	}
	if !strings.Contains(shell, "\n\n") || !strings.Contains(shell, "Actions") {
		t.Fatalf("footer must separate reason from action controls:\n%s", shell)
	}
	mcp := confirmFooter("mcp_call", map[string]any{"server": "fs", "tool": "read"}, 100, false)
	if strings.Contains(mcp, "[a]") {
		t.Fatalf("non-shell footer must not offer [a]:\n%s", mcp)
	}
}

func TestConfirmFooterUsesSuppliedReason(t *testing.T) {
	reason := "May I commit and push the verified updater fix that prevents historical unsigned tags from blocking current updates?"
	got := confirmFooter("shell_command", map[string]any{"command": "git diff --check", "reason": reason}, 200, false)
	if !strings.Contains(got, "Reason: "+reason) {
		t.Fatalf("confirm footer must show supplied reason:\n%s", got)
	}
}

func TestConfirmFooterCanHideReason(t *testing.T) {
	args := map[string]any{"command": "git diff --check", "reason": "May I run the verified command?"}
	got := confirmFooter("shell_command", args, 100, true)
	if strings.Contains(got, "Reason:") || !strings.Contains(got, "[r] show reason") {
		t.Fatalf("collapsed footer must hide reason and advertise expansion:\n%s", got)
	}
}

func TestConfirmReasonToggle(t *testing.T) {
	m := New(&agent.Loop{}, mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.confirm = &confirmState{Tool: "shell_command", Args: map[string]any{"command": "git status", "reason": "Inspect the repository state."}}
	nm, _ := m.updateConfirm(confirmKey('r'))
	if !nm.(Model).confirm.reasonHidden {
		t.Fatal("r must collapse the approval reason")
	}
	nm, _ = nm.(Model).updateConfirm(confirmKey('r'))
	if nm.(Model).confirm.reasonHidden {
		t.Fatal("r must expand the approval reason")
	}
}

func TestConfirmPreemptsAllComposerPickers(t *testing.T) {
	m := New(&agent.Loop{}, mode.Plan, t.TempDir(), "ollama/m", 32000)
	m.slashOpen = true
	m.atOpen = true
	done := make(chan bool, 1)
	nm, _ := m.Update(showConfirmMsg{Tool: "shell_command", Args: map[string]any{"command": "printf ok"}, Done: done})
	after := nm.(Model)
	if after.confirm == nil {
		t.Fatal("approval message must open the confirm surface")
	}
	if after.slashOpen || after.atOpen {
		t.Fatal("approval surface must close composer pickers so they cannot intercept y/n")
	}
}
