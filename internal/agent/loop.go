// Package agent — the ReAct loop: think → act → observe → repeat.
// Phase 0 slice: iteration cap + doom-loop guard → Plan handoff.
// Compaction (Phase 1), sandboxing (Phase 2), repair layer (Phase 3)
// all hook into this loop without changing its shape.
package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"tilde/internal/compact"
	"tilde/internal/mode"
	"tilde/internal/policy"
	"tilde/internal/provider"
	"tilde/internal/session"
	"tilde/internal/skills"
	"tilde/internal/tools"
)

// Config tunes the loop.
type Config struct {
	MaxIters    int // hard iteration cap (fail loud, fail cheap)
	DoomRepeats int // same tool+args this many times → handoff to Plan
	Root        string
	Mode        mode.Mode
	Pol         *policy.Policy
	AskUser     func(tool string, args map[string]any) bool // nil = deny asks
	Compactor   *compact.Compactor                          // nil = default budget + fallback summary
	Skills      *skills.Index                               // nil = no skills (prompt extras empty)
	MCP         MCPStatus                                   // nil = no MCP servers (prompt extras empty)
}

// MCPStatus is the agent-visible slice of the MCP manager: server names
// and their tool names. The interface keeps agent decoupled from the
// manager's process lifecycle (see internal/mcp).
type MCPStatus interface {
	ServerTools() map[string][]string // server → tool names, sorted
}

// Event is emitted for the TUI timeline.
type Event struct {
	Kind string // assistant | tool_call | tool_result | system | handoff | done | usage | compacted
	Text string
	Pct  int // context usage percent (usage events only)
}

// Loop is one agent session.
//
// Cfg.Mode and Msgs are touched from two threads — the background agent
// goroutine (Run) and the render thread (Tab, handoff panel, resume,
// /compact) — so they sit behind mu. Everything else in Cfg is
// write-once at startup and needs no lock.
//
// TotPrompt/TotCompletion accumulate provider-reported token usage for
// the run. Written by Run only; read after Run returns (eval reports,
// turn summaries). Heuristic estimates still drive compaction (they exist
// pre-call); these are the receipt, used for cost tracking.
type Loop struct {
	Prov provider.Provider
	Reg  *tools.Registry
	Log  *session.Log
	Cfg  Config
	Msgs []provider.Message

	TotPrompt     int
	TotCompletion int

	mu sync.Mutex
	// pmut guards Prov: /model may swap the backend mid-session while a
	// background summarizer (compaction, explore child) still holds the
	// old one. Readers take it only around the pointer read.
	pmut sync.RWMutex
}

// CurrentProvider reads the live backend under lock — /model may swap
// it mid-session while background work still holds the old one.
func (l *Loop) CurrentProvider() provider.Provider {
	l.pmut.RLock()
	defer l.pmut.RUnlock()
	return l.Prov
}

// SetProvider swaps the backend for subsequent turns (spec §2.24:
// provider switching is a first-class mid-session operation). In-flight
// turns keep the provider they started with — backends are stateless
// besides config, so nothing else needs migrating.
func (l *Loop) SetProvider(p provider.Provider) {
	l.pmut.Lock()
	defer l.pmut.Unlock()
	l.Prov = p
}

// getMode reads the autonomy tier under lock.
func (l *Loop) GetMode() mode.Mode {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Cfg.Mode
}

// setMode writes the autonomy tier under lock.
func (l *Loop) SetMode(m mode.Mode) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Cfg.Mode = m
}

// appendLog writes one session-log entry; on first error it emits a
// system event so a dead log can never fail silently (success paths
// unchanged — this only adds the error branch).
func (l *Loop) appendLog(typ string, data map[string]any, emit func(Event)) {
	if l.Log == nil {
		return
	}
	if err := l.Log.Append(typ, data); err != nil && emit != nil {
		emit(Event{Kind: "system", Text: "session log: " + err.Error()})
	}
}

// maybeCompact runs the shared pre-call compaction path: no-op (no
// marker, no log) when nothing was dropped. Reused at the top of every
// iteration and after tool results land, so a mid-turn output spike is
// compacted before the next model call instead of overflowing it.
func (l *Loop) maybeCompact(ctx context.Context, comp *compact.Compactor, emit func(Event)) bool {
	live := l.MsgsSnapshot()
	if !comp.Needed(live) {
		return false
	}
	res := comp.Compact(ctx, live)
	if res.Dropped == 0 {
		return false
	}
	l.SetMsgs(res.Msgs)
	marker := fmt.Sprintf("[compacted: %d older messages — goals, findings & decisions kept · see session log]", res.Dropped)
	emit(Event{Kind: "compacted", Text: marker})
	l.appendLog("compacted", map[string]any{"dropped": res.Dropped, "summary": res.Summary}, emit)
	return true
}

// appendMsg appends to live context under lock.
func (l *Loop) AppendMsg(m provider.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Msgs = append(l.Msgs, m)
}

// SetMsgs swaps live context under lock (compaction, resume).
func (l *Loop) SetMsgs(msgs []provider.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Msgs = msgs
}

// MsgsSnapshot copies live context under lock for reads that must not
// race a concurrent SetMsgs (compaction estimates, prompt assembly).
func (l *Loop) MsgsSnapshot() []provider.Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]provider.Message, len(l.Msgs))
	copy(out, l.Msgs)
	return out
}

// systemPrompt assembles identity + tool docs + mode rule.
func systemPrompt(toolNames []string, m mode.Mode) string {
	var b strings.Builder
	b.WriteString("You are tilde, a terminal-native coding agent. ")
	b.WriteString("You read code, edit files, run commands, and report back.\n")
	b.WriteString("Available tools: ")
	b.WriteString(strings.Join(toolNames, ", "))
	b.WriteString(".\n")
	b.WriteString("Rules: make targeted tool calls; read before editing; ")
	b.WriteString("run the relevant tests after edits; report done concisely.\n")
	if m == mode.Plan {
		b.WriteString("MODE: Plan (read-only). Do NOT call write_file, edit_file, or shell_command. ")
		b.WriteString("Research with read-only tools and present a plan instead.\n")
	}
	return b.String()
}

// systemExtra renders the progressive-disclosure prompt sections: skill
// names + one-liners and MCP server + tool names. Bodies and schemas stay
// out — they load per-use via load_skill / mcp_call. Rebuilt every
// iteration so mid-session installs appear with no restart.
func (l *Loop) systemExtra() string {
	var b strings.Builder
	if l.Cfg.Skills != nil {
		if list := l.Cfg.Skills.List(); len(list) > 0 {
			b.WriteString("\nSkills (one line each — call load_skill <name> for full text, only when relevant):\n")
			for _, sk := range list {
				b.WriteString(sk.PromptLine())
				b.WriteString("\n")
			}
		}
	}
	if l.Cfg.MCP != nil {
		if st := l.Cfg.MCP.ServerTools(); len(st) > 0 {
			names := make([]string, 0, len(st))
			for s := range st {
				names = append(names, s)
			}
			sort.Strings(names)
			b.WriteString("\nMCP servers (call tools via mcp_call {server, tool, arguments}; list a server's tools via mcp_list {server}):\n")
			for _, s := range names {
				fmt.Fprintf(&b, "- %s — tools: %s\n", s, strings.Join(st[s], ", "))
			}
		}
	}
	return b.String()
}

// toolDefs converts the registry into provider-facing defs.
func (l *Loop) toolDefs() []provider.ToolDef {
	var out []provider.ToolDef
	for _, n := range l.Reg.Names() {
		if t, ok := l.Reg.Get(n); ok {
			out = append(out, provider.ToolDef{Name: t.Name(), Description: t.Description(), Schema: t.Schema()})
		}
	}
	return out
}

// Run handles one user goal, streaming events to emit.
func (l *Loop) Run(ctx context.Context, goal string, emit func(Event)) (string, error) {
	maxIters := l.Cfg.MaxIters
	if maxIters <= 0 {
		maxIters = 25
	}
	doomAt := l.Cfg.DoomRepeats
	if doomAt <= 0 {
		doomAt = 3
	}
	// maxNudges bounds the read-only nudge path: two nudges, then the
	// third identical-repeat trigger escalates to the Plan handoff.
	// The iteration cap backstops everything regardless.
	const maxNudges = 2
	comp := l.Cfg.Compactor
	if comp == nil {
		comp = &compact.Compactor{} // default budget + fallback summary
	}
	budget := comp.Budget
	if budget <= 0 {
		budget = compact.DefaultBudget
	}
	l.AppendMsg(provider.Message{Role: "user", Content: goal})
	l.appendLog("user", map[string]any{"content": goal}, emit)

	// Doom-loop detector: the same tool+args issued consecutively.
	// Counts repeats (success or failure — either way the turn burns
	// without progress), resets on any different call, and fingerprints
	// deterministically (sorted keys, name + k=v pairs — never Go map
	// print order).
	lastFP, runLen := "", 0
	nudges := 0 // read-only doom nudges this turn; escalates to handoff at maxNudges
	var lastText string

	for i := 0; i < maxIters; i++ {
		// System prompt is composed fresh every iteration so mid-session
		// installs (skills, MCP servers) appear with no restart.
		sys := systemPrompt(l.Reg.Names(), l.GetMode()) + l.systemExtra()
		defs := l.toolDefs()
		live := l.MsgsSnapshot()
		// Pre-check + auto-compaction: never silently overflow. The
		// estimate covers everything the next call sends: live context
		// + the composed system prompt (identity, tool docs, skills/MCP
		// extras) + ~32 tokens per registered tool schema (name,
		// description, parameter refs — errs upward so we compact
		// early rather than overflow silently).
		used := compact.Estimate(live) +
			(len("system")+len(sys)+12)/4 +
			len(defs)*32
		pct := 0
		if budget > 0 {
			pct = used * 100 / budget
		}
		emit(Event{Kind: "usage", Text: compact.Label(used, budget), Pct: pct})
		l.maybeCompact(ctx, comp, emit)
		full := append([]provider.Message{{Role: "system", Content: sys}}, l.MsgsSnapshot()...)
		resp, err := l.CurrentProvider().Chat(ctx, full, defs)
		if err != nil {
			emit(Event{Kind: "system", Text: "model error [" + provider.Classify(err) + "]: " + err.Error()})
			return "", err
		}
		l.TotPrompt += resp.Usage.Prompt
		l.TotCompletion += resp.Usage.Completion
		// Thinking trace (transparency): when the backend exposes model
		// reasoning, surface it as its own event ahead of the reply and
		// record it in the session log — the user sees how a decision
		// was reached. It is deliberately NOT appended to Msgs: echoing
		// reasoning back as context bloats every turn and risks the
		// model re-reading stale rationale as fresh instruction. The
		// actions it produced (calls + results below) carry the state.
		if thought := strings.TrimSpace(resp.Thinking); thought != "" {
			emit(Event{Kind: "thinking", Text: thought})
			l.appendLog("thinking", map[string]any{"content": resp.Thinking}, emit)
		}
		if resp.Content != "" {
			lastText = resp.Content
			emit(Event{Kind: "assistant", Text: resp.Content})
			l.AppendMsg(provider.Message{Role: "assistant", Content: resp.Content})
			l.appendLog("assistant", map[string]any{"content": resp.Content}, emit)
		}
		if len(resp.ToolCalls) == 0 {
			emit(Event{Kind: "done", Text: "Done"})
			return lastText, nil
		}
		if resp.Truncated {
			// Fail closed: the backend hit its output limit, so tool
			// arguments may be cut mid-JSON — never dispatch. Each
			// call fails as a model-readable tool-result error asking
			// for a re-issue with complete arguments; the turn
			// continues so the model can re-issue immediately. (These
			// skips bypass the doom-loop fingerprint below — a run of
			// truncated re-issues is still bounded by the iteration
			// cap, which hands off to Plan like any capped run.)
			for _, tc := range resp.ToolCalls {
				out := fmt.Sprintf("tool %q not executed: arguments may be truncated (the model response hit its output limit) — re-issue the call with complete arguments.", tc.Name)
				emit(Event{Kind: "tool_result", Text: out})
				l.appendLog("tool_result", map[string]any{"name": tc.Name, "output": out}, emit)
				l.AppendMsg(provider.Message{Role: "user", Content: "Tool " + tc.Name + " result:\n" + out})
			}
			continue
		}
		var pending []provider.ToolCall
		for _, tc := range resp.ToolCalls {
			fp := fingerprint(tc.Name, tc.Args)
			if fp == lastFP {
				runLen++
			} else {
				lastFP, runLen = fp, 1
			}
			if runLen >= doomAt {
				// Split doom handling: mutating repeats fail closed to
				// Plan exactly as before. Read-only repeats (nothing was
				// ever at risk) get one pointed nudge naming the recovery
				// instead — measured cost was fan-out tasks dying on
				// grep/read re-verification loops. Static text only: the
				// tool name is allowlisted, no output or args echoed.
				// Escalation + iteration cap bound the nudge path: two
				// nudges, then the third trigger hands off like before.
				if !mode.IsMutatingCall(tc.Name, tc.Args) && nudges < maxNudges {
					nudges++
					runLen = 0 // re-arm: next nudge needs doomAt fresh repeats
					out := fmt.Sprintf("identical read-only call %q issued %d times in a row — the result is already in context above; act on it with a different call next (nudge %d of %d). Do not retry the identical call.", tc.Name, doomAt, nudges, maxNudges)
					emit(Event{Kind: "tool_result", Text: out})
					l.appendLog("tool_result", map[string]any{"name": tc.Name, "output": out}, emit)
					l.AppendMsg(provider.Message{Role: "user", Content: "Tool " + tc.Name + " result:\n" + out})
					continue
				}
				msg := fmt.Sprintf("Same call (%s) issued %d times in a row with no progress — reverting to Plan mode. Nothing further will be changed. Partial work above is preserved.", tc.Name, runLen)
				emit(Event{Kind: "handoff", Text: msg})
				l.appendLog("system", map[string]any{"handoff": msg}, emit)
				l.SetMode(mode.Plan) // fail closed: drop autonomy
				return lastText + "\n\n[HANDOFF TO PLAN: " + msg + "]", fmt.Errorf("%s", msg)
			}
			// Mode gate (registry level — cannot be prompt-hacked around).
			// Arg-aware: shell_poll kill is mutating (Plan-blocked) while
			// status/log stay read-only.
			if err := l.GetMode().AllowedCall(tc.Name, tc.Args); err != nil {
				out := err.Error() + " Do not retry this call."
				emit(Event{Kind: "tool_result", Text: out})
				l.AppendMsg(provider.Message{Role: "user", Content: "Tool " + tc.Name + " result: " + out})
				continue
			}
			// Policy tier — always serial: never prompt concurrently.
			dec := policy.Allow
			if l.Cfg.Pol != nil {
				dec = l.Cfg.Pol.Check(tc.Name, tc.Args)
			}
			// Auto mode auto-approves Ask-tier live at dispatch time, so
			// Tab-cycling into Auto takes effect immediately (the startup
			// AlwaysAllow wiring cannot see later mode changes). Deny
			// still beats everything.
			if dec == policy.Ask && l.GetMode() == mode.Auto {
				dec = policy.Allow
			}
			switch dec {
			case policy.Deny:
				out := fmt.Sprintf("tool %q denied by policy (%s). Do not retry; propose an alternative.", tc.Name, policy.Describe(tc.Name, tc.Args))
				emit(Event{Kind: "tool_result", Text: out})
				l.AppendMsg(provider.Message{Role: "user", Content: out})
				continue
			case policy.Ask:
				ok := false
				if l.Cfg.AskUser != nil {
					ok = l.Cfg.AskUser(tc.Name, tc.Args)
				}
				emit(Event{Kind: "tool_result", Text: fmt.Sprintf("%s → %s", policy.Describe(tc.Name, tc.Args), map[bool]string{true: "approved", false: "denied"}[ok])})
				if !ok {
					l.AppendMsg(provider.Message{Role: "user", Content: fmt.Sprintf("User denied %s. Do not retry; find another way or ask.", tc.Name)})
					continue
				}
			}
			if ParallelSafe(tc.Name) {
				pending = append(pending, tc)
				continue
			}
			// Serial barrier: mutating/unknown calls flush the batch
			// first, then run alone in request order.
			if err := l.flushBatch(ctx, pending, emit); err != nil {
				return lastText, err
			}
			pending = nil
			if err := l.execApproved(ctx, tc, emit); err != nil {
				return lastText, err
			}
		}
		if err := l.flushBatch(ctx, pending, emit); err != nil {
			return lastText, err
		}
		pending = nil
		// Mid-turn spike: tool outputs just landed — re-check before the
		// next model call via the same shared path as the top of loop.
		l.maybeCompact(ctx, comp, emit)
	}
	msg := fmt.Sprintf("Iteration cap (%d) reached — stopping cheap instead of looping. Summarize partial progress and hand back to the user.", maxIters)
	emit(Event{Kind: "handoff", Text: msg})
	l.SetMode(mode.Plan) // fail closed: a capped run drops autonomy like the doom path
	return lastText, fmt.Errorf("%s", msg)
}

// ParallelSafe names tools with no observable side effects: safe to run
// concurrently within one turn. Everything else (writes, shell, unknown
// or MCP-executed tools) runs serially as an ordering barrier. The set is
// closed by default — a new tool is serial until listed here. Exported so
// the TUI groups the same read-only calls it batches (single source).
func ParallelSafe(name string) bool {
	switch name {
	case "read_file", "grep", "glob", "git_status", "git_diff",
		"git_worktree_list", "mcp_list", "load_skill":
		return true
	}
	return false
}

// execApproved runs one gate-and-policy-cleared call: timeline, audit
// log, dispatch, context. Returns ctx.Err() on cancellation.
func (l *Loop) execApproved(ctx context.Context, tc provider.ToolCall, emit func(Event)) error {
	emit(Event{Kind: "tool_call", Text: tc.Name + " " + shortArgs(tc.Args)})
	l.appendLog("tool_call", map[string]any{"name": tc.Name, "args": tc.Args}, emit)
	out := l.Reg.Dispatch(ctx, tc.Name, tc.Args)
	emit(Event{Kind: "tool_result", Text: out})
	l.appendLog("tool_result", map[string]any{"name": tc.Name, "output": out}, emit)
	l.AppendMsg(provider.Message{Role: "user", Content: "Tool " + tc.Name + " result:\n" + out})
	if ctx.Err() != nil {
		return ctx.Err() // cooperative cancellation
	}
	return nil
}

// flushBatch runs pending read-only calls concurrently and processes
// results strictly in request order: timeline, log, and context all read
// as if serial, at a fraction of the wall time.
func (l *Loop) flushBatch(ctx context.Context, batch []provider.ToolCall, emit func(Event)) error {
	if len(batch) == 0 {
		return nil
	}
	type res struct {
		out string
	}
	outs := make([]res, len(batch))
	var wg sync.WaitGroup
	var mu sync.Mutex // guards outs so a cancelled wait can read them safely
	for i, tc := range batch {
		wg.Add(1)
		go func(i int, tc provider.ToolCall) {
			defer wg.Done()
			out := l.Reg.Dispatch(ctx, tc.Name, tc.Args)
			mu.Lock()
			outs[i].out = out
			mu.Unlock()
		}(i, tc)
	}
	// Cancellable wait: on ctx.Done return what is collected so far
	// instead of hanging a cancelled turn. Results still process
	// strictly in request order below — the same code either way.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	cancelled := false
	select {
	case <-done:
	case <-ctx.Done():
		cancelled = true
	}
	mu.Lock()
	got := make([]res, len(outs))
	copy(got, outs)
	mu.Unlock()
	if !cancelled {
		wg.Wait() // done already closed; no-op barrier before ordered read
	}
	for i, tc := range batch {
		emit(Event{Kind: "tool_call", Text: tc.Name + " " + shortArgs(tc.Args)})
		l.appendLog("tool_call", map[string]any{"name": tc.Name, "args": tc.Args}, emit)
		emit(Event{Kind: "tool_result", Text: got[i].out})
		l.appendLog("tool_result", map[string]any{"name": tc.Name, "output": got[i].out}, emit)
		l.AppendMsg(provider.Message{Role: "user", Content: "Tool " + tc.Name + " result:\n" + got[i].out})
	}
	if cancelled {
		return ctx.Err()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func fingerprint(name string, args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	b.WriteString("\x00")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%v;", k, args[k])
	}
	return b.String()
}

func shortArgs(args map[string]any) string {
	for _, k := range []string{"path", "command", "pattern"} {
		if v, ok := args[k]; ok {
			s := fmt.Sprintf("%v", v)
			if len(s) > 60 {
				s = s[:57] + "..."
			}
			return s
		}
	}
	return ""
}
