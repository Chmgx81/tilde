package tui

import (
	"context"
	"fmt"
	"strings"

	"tilde/internal/agent"
	"tilde/internal/tools"
)

// Worktree apply/discard affordance (spec §2.19-adjacent): /apply and
// /discard dispatch to the loop registry's apply_work/discard_work tools
// with the active session path, reusing the existing plumbing. Human
// surface only — output never enters model context, and the transcript
// render stays bounded with an announced truncation receipt.

// maxWorkApplyLines bounds apply/discard output in the transcript; the
// session log always keeps everything, and the receipt says so.
const maxWorkApplyLines = 40

// doApplyWork reviews the pending work session and keeps the worktree.
func (m *Model) doApplyWork(args string) { m.doWorkSession("apply_work", args) }

// doDiscardWork removes the pending work session's worktree.
func (m *Model) doDiscardWork(args string) { m.doWorkSession("discard_work", args) }

func (m *Model) doWorkSession(toolName, args string) {
	if m.loop.Reg == nil {
		m.append("✗ no tool registry attached.")
		return
	}
	t, ok := m.loop.Reg.Get(toolName)
	if !ok {
		m.append(fmt.Sprintf("✗ %s tool not registered.", toolName))
		return
	}
	path := workSessionPath(args, t)
	if path == "" {
		m.append("✗ no pending work session — run spawn_work first, or pass its path: /apply <path>.")
		return
	}
	out, err := t.Exec(context.Background(), map[string]any{"path": path})
	if err != nil {
		// First line only: tool errors carry multi-line fix guidance that
		// would smear the one-event-one-line transcript discipline.
		m.append("✗ " + firstLine(err.Error()))
		return
	}
	rendered, _ := renderToolResultLimit(out, maxWorkApplyLines)
	m.append(rendered)
}

// workSessionPath resolves the session path: an explicit argument wins,
// otherwise the pending session tracked by the work tool's state. Empty
// means no session — fail loud, never guess.
func workSessionPath(args string, t tools.Tool) string {
	if f := strings.Fields(args); len(f) > 0 {
		return f[0]
	}
	switch w := t.(type) {
	case *agent.ApplyWorkTool:
		if w.State != nil {
			return w.State.Active()
		}
	case *agent.DiscardWorkTool:
		if w.State != nil {
			return w.State.Active()
		}
	}
	return ""
}
