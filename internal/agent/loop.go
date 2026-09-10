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
	"sync/atomic"
	"tilde/internal/compact"
	"tilde/internal/mode"
	"tilde/internal/policy"
	"tilde/internal/provider"
	"tilde/internal/rules"
	"tilde/internal/session"
	"tilde/internal/skills"
	"tilde/internal/tools"
)

// Config tunes the loop.
type Config struct {
	MaxIters      int // hard iteration cap (fail loud, fail cheap)
	DoomRepeats   int // same tool+args this many times → handoff to Plan
	FlushParallel int // batch-flush concurrency cap (default 8)
	// PlanAllow whitelists tools past the Plan-mode loop gate (writer
	// children allow write_file/edit_file inside their worktree). Nil =
	// strict Plan, exactly as before.
	PlanAllow []string
	Root      string
	Mode      mode.Mode
	Pol       *policy.Policy
	AskUser   func(tool string, args map[string]any) bool // nil = deny asks
	Compactor *compact.Compactor                          // nil = default budget + fallback summary
	Skills    *skills.Index                               // nil = no skills (prompt extras empty)
	MCP       MCPStatus                                   // nil = no MCP servers (prompt extras empty)
	// RulesOK loads project rules (AGENTS.md/CLAUDE.md/.tilde/RULES.md)
	// into the prompt when set. Main sets it from the same trust gate
	// as project skills — never auto-on for untrusted checkouts.
	RulesOK bool
}

// MCPStatus is the agent-visible slice of the MCP manager: server names
// and their tool names. The interface keeps agent decoupled from the
// manager's process lifecycle (see internal/mcp).
type MCPStatus interface {
	ServerTools() map[string][]string // server → tool names, sorted
}

// Event is emitted for the TUI timeline.
type Event struct {
	Kind string // assistant | assistant_delta | assistant_stream_reset | tool_call | tool_result | system | handoff | done | usage | compacted
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
// the run. Written by Run as each model reply lands; read concurrently
// by the render thread for the live cost bar and per-turn token delta,
// so they are atomics, not bare ints. Heuristic estimates still drive
// compaction (they exist pre-call); these are the receipt, used for
// cost tracking.
type Loop struct {
	Prov provider.Provider
	Reg  *tools.Registry
	Log  *session.Log
	Cfg  Config
	Msgs []provider.Message

	TotPrompt     atomic.Int64
	TotCompletion atomic.Int64

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

// composeSys builds the system prompt for this iteration: identity +
// tool docs + mode rule, project rules on top, writer-child Plan
// exception when set. Reloaded per iteration so mid-session installs
// and AGENTS.md drop-ins apply with no restart.
func (l *Loop) composeSys() string {
	sys := systemPrompt(l.Reg.Names(), l.GetMode()) + l.systemExtra()
	// Project ground truth outranks generic harness rules. RulesOK
	// mirrors the project skills trust gate (main.go), never auto-on.
	if l.Cfg.RulesOK {
		if r, ok := rules.Load(l.Cfg.Root); ok {
			sys = r.PromptBlock() + "\n" + sys
		}
	}
	// Writer children are Plan-locked except their whitelist: say so,
	// or the stock Plan rule ("do NOT call write_file") contradicts
	// the task. No other loop sets PlanAllow, so no other prompt moves.
	if l.GetMode() == mode.Plan && len(l.Cfg.PlanAllow) > 0 {
		sys += "Exception: you MAY call " + strings.Join(l.Cfg.PlanAllow, ", ") +
			" (confined to your worktree); all other mutating tools stay blocked.\n"
	}
	return sys
}

// callOverhead estimates the non-context tokens every model call
// carries: the composed system prompt + ~32 tokens per registered tool
// schema (name, description, parameter refs — errs upward so we compact
// early rather than overflow silently).
func callOverhead(sys string, ndefs int) int {
	return (len("system")+len(sys)+12)/4 + ndefs*32
}

// maybeCompact runs the shared pre-call compaction path: no-op (no
// marker, no log) when nothing was dropped. Reused at the top of every
// iteration and after tool results land, so a mid-turn output spike is
// compacted before the next model call instead of overflowing it.
// The overhead (system prompt + tool schemas) must be included: the
// decision is about the next request's total size, not live context
// alone — otherwise a turn under 80% live but over budget with schemas
// would skip compaction and overflow.
func (l *Loop) maybeCompact(ctx context.Context, comp *compact.Compactor, overhead int, emit func(Event)) bool {
	live := l.MsgsSnapshot()
	if !comp.NeededWith(live, overhead) {
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

// planAllowed reports whether a Plan-blocked call is whitelisted for
// this loop (writer children allow write_file/edit_file inside their
// worktree). Empty for every other loop: strict Plan, as before.
func (l *Loop) planAllowed(name string) bool {
	for _, n := range l.Cfg.PlanAllow {
		if n == name {
			return true
		}
	}
	return false
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
	b.WriteString("For approval-gated calls, include a concise user-facing reason when the tool schema supports a reason field. The reason is explanatory only and never changes policy or authorization.\n")
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
		sys := l.composeSys()
		defs := l.toolDefs()
		live := l.MsgsSnapshot()
		// Pre-check + auto-compaction: never silently overflow. The
		// estimate covers everything the next call sends: live context
		// + the composed system prompt (identity, tool docs, skills/MCP
		// extras) + ~32 tokens per registered tool schema (name,
		// description, parameter refs — errs upward so we compact
		// early rather than overflow silently).
		used := compact.Estimate(live) + callOverhead(sys, len(defs))
		pct := 0
		if budget > 0 {
			pct = used * 100 / budget
		}
		emit(Event{Kind: "usage", Text: compact.Label(used, budget), Pct: pct})
		l.maybeCompact(ctx, comp, callOverhead(sys, len(defs)), emit)
		full := append([]provider.Message{{Role: "system", Content: sys}}, l.MsgsSnapshot()...)
		resp, err := l.chatTurn(ctx, full, defs, emit)
		if err != nil {
			emit(Event{Kind: "system", Text: "model error [" + provider.Classify(err) + "]: " + err.Error()})
			return "", err
		}
		l.TotPrompt.Add(int64(resp.Usage.Prompt))
		l.TotCompletion.Add(int64(resp.Usage.Completion))
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
			// status/log stay read-only. PlanAllow whitelists writer-child
			// writes past this gate; the registry gate re-checks below.
			if err := l.GetMode().AllowedCall(tc.Name, tc.Args); err != nil && !l.planAllowed(tc.Name) {
				out := err.Error() + " Do not retry this call."
				l.Reg.AuditDecision(tc.Name, "deny", tc.Args, "blocked by mode gate: "+err.Error())
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
			if dec == policy.Ask && l.GetMode() == mode.Auto &&
				(l.Cfg.Pol == nil || !l.Cfg.Pol.Unattended) {
				dec = policy.Allow
			}
			switch dec {
			case policy.Deny:
				// Name the winning rule: a path-scoped deny reports its
				// pattern + fix instead of the generic tier message. The
				// "denied by policy" prefix is load-bearing: headless
				// exit-code classification keys on that substring.
				denied := fmt.Sprintf("tool %q denied by policy (%s). Do not retry; propose an alternative.", tc.Name, policy.Describe(tc.Name, tc.Args))
				detail := "denied by policy tier"
				if l.Cfg.Pol != nil && l.Cfg.Pol.File != nil {
					if reason := l.Cfg.Pol.File.PathDenyReason(tc.Name, tc.Args); reason != "" {
						denied = fmt.Sprintf("tool %q denied by policy — %s. Do not retry; propose an alternative.", tc.Name, reason)
						detail = reason
					}
				}
				l.Reg.AuditDecision(tc.Name, "deny", tc.Args, detail)
				out := denied
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
					l.Reg.AuditDecision(tc.Name, "ask-denied", tc.Args, "user declined the confirm")
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
		// Recompute the overhead fresh: a mid-turn install can change
		// the tool set after the top-of-iteration estimate.
		l.maybeCompact(ctx, comp, callOverhead(l.composeSys(), len(l.toolDefs())), emit)
	}
	msg := fmt.Sprintf("Iteration cap (%d) reached — stopping cheap instead of looping. Summarize partial progress and hand back to the user.", maxIters)
	emit(Event{Kind: "handoff", Text: msg})
	l.SetMode(mode.Plan) // fail closed: a capped run drops autonomy like the doom path
	return lastText, fmt.Errorf("%s", msg)
}

// chatTurn is the per-turn model call. When the backend offers the
// optional provider.Streamer fast path, the stream carries the same
// tool definitions as Chat and Collect assembles both prose and tool
// calls — the fast path sees everything Chat would, so tool behavior
// is unchanged. Any stream failure (transport error, 30s first-event
// timeout, mid-stream abort) falls back to the existing non-streaming
// Chat with its retry/Classify/backoff intact, so the surfaced error
// is always an ordinary provider error; a turn with neither text nor
// calls falls back too. Ctx cancellation never triggers a fallback
// request — it returns ctx.Err() directly, matching the Chat path's
// existing cancel semantics.
func (l *Loop) chatTurn(ctx context.Context, full []provider.Message, defs []provider.ToolDef, emit func(Event)) (provider.Response, error) {
	prov := l.CurrentProvider()
	if s, ok := prov.(provider.Streamer); ok && ctx.Err() == nil {
		// Child ctx: cancelling it aborts the in-flight stream request
		// before the Chat fallback, so a hung stream holds no
		// connection past the turn. Parent cancel still propagates.
		sctx, cancel := context.WithCancel(ctx)
		streamed := false
		resp, serr := provider.CollectWith(sctx, s, full, defs, func(text string) {
			streamed = true
			if emit != nil {
				emit(Event{Kind: "assistant_delta", Text: text})
			}
		})
		cancel()
		if serr == nil && (resp.Content != "" || len(resp.ToolCalls) > 0) {
			return resp, nil
		}
		if ctx.Err() != nil {
			return provider.Response{}, ctx.Err()
		}
		if streamed && emit != nil {
			emit(Event{Kind: "assistant_stream_reset"})
		}
	}
	return prov.Chat(ctx, full, defs)
}

// ParallelSafe names tools with no observable side effects: safe to run
// concurrently within one turn. Everything else (writes, shell, unknown
// or MCP-executed tools) runs serially as an ordering barrier. The set is
// closed by default — a new tool is serial until listed here. Exported so
// the TUI groups the same read-only calls it batches (single source).
func ParallelSafe(name string) bool {
	switch name {
	case "read_file", "grep", "glob", "git_status", "git_diff",
		"git_worktree_list", "mcp_list", "load_skill", "symbol_search",
		"diagnose":
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

// defaultFlushParallel caps concurrent Dispatch goroutines per batch.
const defaultFlushParallel = 8

// flushParallel reports the batch concurrency cap (override or default).
func (l *Loop) flushParallel() int {
	if l != nil && l.Cfg.FlushParallel > 0 {
		return l.Cfg.FlushParallel
	}
	return defaultFlushParallel
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
	started := make([]bool, len(batch))   // worker dispatched (may still be running)
	completed := make([]bool, len(batch)) // Dispatch returned; outs[i] is final
	var wg sync.WaitGroup
	var mu sync.Mutex // guards outs/started/completed so a cancelled wait can read them safely
	// Semaphore: at most flushParallel Dispatch calls in flight — one
	// goroutine per call with no bound burns FDs on wide turns.
	sem := make(chan struct{}, l.flushParallel())
	acquireFailed := false
	for i, tc := range batch {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			acquireFailed = true
		}
		if acquireFailed {
			break
		}
		wg.Add(1)
		go func(i int, tc provider.ToolCall) {
			defer wg.Done()
			defer func() { <-sem }()
			mu.Lock()
			started[i] = true
			mu.Unlock()
			out := l.Reg.Dispatch(ctx, tc.Name, tc.Args)
			mu.Lock()
			outs[i].out = out
			completed[i] = true
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
	wasStarted := make([]bool, len(started))
	copy(wasStarted, started)
	wasCompleted := make([]bool, len(completed))
	copy(wasCompleted, completed)
	mu.Unlock()
	if !cancelled {
		wg.Wait() // done already closed; no-op barrier before ordered read
	}
	for i, tc := range batch {
		if !wasStarted[i] {
			// Never dispatched (cancellation won the semaphore race):
			// emitting an empty result would fabricate tool output the
			// model then reasons over. Skip it entirely.
			continue
		}
		emit(Event{Kind: "tool_call", Text: tc.Name + " " + shortArgs(tc.Args)})
		l.appendLog("tool_call", map[string]any{"name": tc.Name, "args": tc.Args}, emit)
		out := got[i].out
		if cancelled && !wasCompleted[i] {
			// Dispatched but killed mid-flight: say so honestly instead
			// of presenting a truncated/empty string as the real result,
			// and keep it out of the model context.
			out = "(cancelled before the tool returned)"
			emit(Event{Kind: "tool_result", Text: out})
			l.appendLog("tool_result", map[string]any{"name": tc.Name, "output": out, "cancelled": true}, emit)
			continue
		}
		emit(Event{Kind: "tool_result", Text: out})
		l.appendLog("tool_result", map[string]any{"name": tc.Name, "output": out}, emit)
		l.AppendMsg(provider.Message{Role: "user", Content: "Tool " + tc.Name + " result:\n" + out})
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
