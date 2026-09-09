package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"tilde/internal/agent"
	"tilde/internal/policy"
)

// updateConfirm answers the inline approval prompt. The default focused
// option is always the safe one: bare Enter denies.
func (m Model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Ctrl+C never traps inside a prompt: this press denies (unblocking
	// the turn). Cancelling a running turn itself is double-Esc.
	if msg.Type == tea.KeyCtrlC {
		if m.pendingShell != "" {
			m.pendingShell, m.confirm = "", nil
			m.append("  ⎿ denied")
			return m, nil
		}
		select {
		case m.confirm.Done <- false:
		default:
		}
		m.append("  ⎿ denied")
		m.confirm = nil
		return m, nil
	}
	switch strings.ToLower(msg.String()) {
	case "r":
		// Reason is explanatory model output, not an authorization signal.
		// Keep it collapsible so a long rationale never dominates the bounded
		// approval surface; the exact action and y/n decision stay visible.
		if m.confirm != nil {
			m.confirm.reasonHidden = !m.confirm.reasonHidden
		}
		return m, nil
	case "y":
		if m.pendingShell != "" {
			cmd := m.pendingShell
			m.pendingShell, m.confirm = "", nil
			m.append("  ⎿ approved once")
			return m, m.shellEscapeCmd(cmd)
		}
		select {
		case m.confirm.Done <- true:
		default:
		}
		m.append("  ⎿ approved once")
		m.confirm = nil
		return m, nil
	case "a":
		// Session-scoped exact-command approval: the EXACT literal
		// command string is approved for the rest of this process.
		// Shell only — for any other tool [a] is not offered and this
		// press is ignored. Deny-tier shapes never reach a prompt
		// (Ask-tier only), and Policy.Check re-evaluates deny first,
		// so a listed command that parses destructive still denies.
		if m.pendingShell != "" {
			cmd := m.pendingShell
			if m.loop != nil && m.loop.Cfg.Pol != nil {
				m.loop.Cfg.Pol.ApproveSession(cmd)
			}
			m.pendingShell, m.confirm = "", nil
			m.append("  ⎿ approved always this session (exact command)")
			return m, m.shellEscapeCmd(cmd)
		}
		if m.confirm != nil && m.confirm.Tool == "shell_command" {
			if cmd, _ := m.confirm.Args["command"].(string); cmd != "" {
				if m.loop != nil && m.loop.Cfg.Pol != nil {
					m.loop.Cfg.Pol.ApproveSession(cmd)
				}
			}
			select {
			case m.confirm.Done <- true:
			default:
			}
			m.append("  ⎿ approved always this session (exact command)")
			m.confirm = nil
			return m, nil
		}
		return m, nil
	case "n", "esc", "enter":
		if m.pendingShell != "" {
			m.pendingShell, m.confirm = "", nil
			m.append("  ⎿ denied")
			return m, nil
		}
		// Enter with no other input denies — destructive actions stay one
		// accidental keypress away from *not* happening.
		select {
		case m.confirm.Done <- false:
		default:
		}
		m.append("  ⎿ denied")
		m.confirm = nil
		return m, nil
	}
	return m, nil
}

// confirmFooter renders the approval panel body: the literal call plus
// the key hints. The session-scoped exact-command option ([a]) is
// offered for shell_command only — never for mcp/tools, where an
// "always" approval would be a pattern-shaped hole. Wire it at the
// confirm panel (model.go View): Confirm + "\n" + confirmFooter(…).
func confirmFooter(tool string, args map[string]any, width int, reasonHidden bool) string {
	reason := "The active policy requires your approval before this action can run."
	if supplied, ok := args["reason"].(string); ok && strings.TrimSpace(supplied) != "" {
		reason = strings.TrimSpace(ansi.Strip(supplied))
	}
	lineWidth := width - 6 // rounded border + padding
	if lineWidth < 10 {
		lineWidth = 10
	}
	foot := "Confirm\n" + wrapLine(ansi.Strip(policy.Describe(tool, args)), lineWidth)
	if !reasonHidden {
		reasonLine := wrapLine("Reason: "+reason, lineWidth)
		foot += "\n\n" + reasonLine
	}
	hint := "[y] once   [n] deny (default — Enter denies)   [r] "
	if reasonHidden {
		hint += "show reason"
	} else {
		hint += "hide reason"
	}
	if tool == "shell_command" {
		hint += "   [a] always this session (exact command)"
	}
	foot += "\n" + wrapLine(hint, lineWidth)
	return foot
}

// shellEscape runs a raw command without invoking the model (§2.6). It
// passes through the same sandbox + confirm tier as a model-issued call —
// the UI never implies "typed directly" means "unsandboxed".
func (m *Model) shellEscape(cmdStr string) {
	if cmdStr == "" {
		m.append("! shell escape: send ! followed by a command, e.g. ! git status")
		return
	}
	if m.running {
		m.append("✗ an agent turn is already running — Esc twice, then ! again.")
		return
	}
	// The registry gate would block this after approval; say so now
	// instead of asking a question with only one answer.
	if err := m.loop.GetMode().Allowed("shell_command"); err != nil {
		m.append("✗ " + err.Error())
		return
	}
	args := map[string]any{"command": cmdStr}
	dec := policy.Allow
	if m.loop.Cfg.Pol != nil {
		dec = m.loop.Cfg.Pol.Check("shell_command", args)
	}
	switch dec {
	case policy.Deny:
		m.append("✗ denied by policy: " + policy.Describe("shell_command", args))
		return
	case policy.Ask:
		m.pendingShell = cmdStr
		m.confirm = &confirmState{Tool: "shell_command", Args: args}
		return
	default:
		m.append("→ ! " + cmdStr)
		pp := m.progPtr
		send := func(msg tea.Msg) {
			if pp != nil && *pp != nil {
				(*pp).Send(msg)
			}
		}
		go func() {
			m.emitShellResult(cmdStr, send)
			send(agentDoneMsg{})
		}()
	}
}

// shellEscapeCmd runs a confirm-approved escape off the render thread.
func (m Model) shellEscapeCmd(cmdStr string) tea.Cmd {
	pp := m.progPtr
	return func() tea.Msg {
		send := func(msg tea.Msg) {
			if pp != nil && *pp != nil {
				(*pp).Send(msg)
			}
		}
		m.emitShellResult(cmdStr, send)
		return agentDoneMsg{}
	}
}

func (m Model) emitShellResult(cmdStr string, send func(tea.Msg)) {
	args := map[string]any{"command": cmdStr}
	send(agentEventMsg(agent.Event{Kind: "tool_call", Text: "shell_command " + cmdStr}))
	if m.loop.Log != nil {
		_ = m.loop.Log.Append("tool_call", map[string]any{"name": "shell_command", "args": args})
	}
	out := m.loop.Reg.Dispatch(context.Background(), "shell_command", args)
	if m.loop.Log != nil {
		_ = m.loop.Log.Append("tool_result", map[string]any{"name": "shell_command", "output": out})
	}
	send(agentEventMsg(agent.Event{Kind: "tool_result", Text: out}))
}
