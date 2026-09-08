// Package eval — trajectory-level consistency suite (docs/Plan.md Phase 7).
//
// Unit tests prove harness behavior; this answers guiding principle #1:
// does tilde solve the SAME task reliably across repeated runs? Each task
// runs N trials in fresh isolated workdirs against the real model, and the
// report scores trajectories (pass rate, steps, failure reasons) — not just
// final answers. A single pass@1 is a demo; pass rate over retries is a
// measurement.
package eval

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"tilde/internal/agent"
	"tilde/internal/compact"
	"tilde/internal/mode"
)

// sawKind reports whether any event of the given kind fired (e.g.
// "compacted", "handoff") — for asserting harness behavior, not output.
func sawKind(events []agent.Event, kind string) bool {
	for _, e := range events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// Task is one repeatable scenario.
type Task struct {
	Name string
	Goal string
	// Setup seeds the trial workdir. Verify judges it (plus final text).
	Setup  func(root string) error
	Verify func(root, finalText string, events []agent.Event) (bool, string)
	// MaxIters caps the turn (0 → runner default). Mode is always Build
	// except tasks that explicitly need otherwise — see Mode field.
	Mode     string // "build" (default) or "plan"
	MaxIters int
	// Budget overrides the context budget for the trial (0 → harness
	// default). Small budgets force mid-task auto-compaction, proving
	// the goal survives it.
	Budget int
	// ExpectTools names tools that must appear in the trajectory for a
	// pass (output-only scoring passes wrong-path successes 20–40% more
	// often; this scores the path, not just the end state).
	ExpectTools []string
}

// TrialResult is one attempt.
type TrialResult struct {
	Task     string
	Trial    int
	Pass     bool
	Reason   string
	ToolCall int
	Turns    int
	MS       int64
	// TokensIn/Out are provider-reported totals (0 when unreported).
	TokensIn  int
	TokensOut int
	// Digest is the trajectory tail: last tool events + final text head.
	// Saved into the JSON report so failures diagnose without reruns.
	Digest []string
	Final  string
}

// TaskReport aggregates a task's trials.
type TaskReport struct {
	Name       string
	Trials     int
	Passes     int
	Rate       float64 // passes/trials — the consistency number
	MedianCall int
	MedianMS   int64
	MedianIn   int
	MedianOut  int
	P90Call    int      // 90th percentile tool calls — tail cost, not just median
	P90MS      int64    // 90th percentile wall time — catches timeout-adjacent trials
	MaxCall    int      // worst trial — bounds the retry budget
	ZeroTokens bool     // true when every trial reported 0 tokens (provider silent)
	Reasons    []string // failure reasons, deduped
	Digests    [][]string
}

// LoopFactory builds a fresh headless loop rooted at dir. Production wires
// the full harness (tools, sandbox, policy-always-allow); tests inject fakes.
type LoopFactory func(root string) *agent.Loop

// Runner executes the suite.
type Runner struct {
	Tasks        []Task
	Trials       int // per task (0 → 3)
	TrialTimeout time.Duration
	NewLoop      LoopFactory
	OutDir       string // trial workdirs parent ("" → os temp)
}

func (r *Runner) trials() int {
	if r.Trials > 0 {
		return r.Trials
	}
	return 3
}

// Run executes every task, printing progress to progress (may be nil).
func (r *Runner) Run(ctx context.Context, progress func(string)) ([]TaskReport, error) {
	base := r.OutDir
	if base == "" {
		var err error
		base, err = os.MkdirTemp("", "tilde-eval")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(base)
	}
	var reports []TaskReport
	for _, task := range r.Tasks {
		rep, err := r.runTask(ctx, base, task, progress)
		if err != nil {
			return reports, err
		}
		reports = append(reports, rep)
	}
	return reports, nil
}

func (r *Runner) runTask(ctx context.Context, base string, task Task, progress func(string)) (TaskReport, error) {
	n := r.trials()
	rep := TaskReport{Name: task.Name, Trials: n}
	seenReasons := map[string]bool{}
	var calls []int
	var mss []int64
	var tins, touts []int
	for i := 0; i < n; i++ {
		dir, err := os.MkdirTemp(base, task.Name+"-trial-*")
		if err != nil {
			return rep, err
		}
		if task.Setup != nil {
			if err := task.Setup(dir); err != nil {
				os.RemoveAll(dir)
				reason := "setup: " + err.Error()
				if !seenReasons[reason] {
					seenReasons[reason] = true
					rep.Reasons = append(rep.Reasons, reason)
				}
				rep.Rate = 0
				return rep, nil
			}
		}
		res := r.runTrial(ctx, task, dir, i+1)
		os.RemoveAll(dir) // isolated by design: no leakage between trials
		calls = append(calls, res.ToolCall)
		mss = append(mss, res.MS)
		tins = append(tins, res.TokensIn)
		touts = append(touts, res.TokensOut)
		if !res.Pass {
			rep.Digests = append(rep.Digests, res.Digest)
		}
		if res.Pass {
			rep.Passes++
		} else if !seenReasons[res.Reason] {
			seenReasons[res.Reason] = true
			rep.Reasons = append(rep.Reasons, res.Reason)
		}
		if progress != nil {
			progress(fmt.Sprintf("[%s] trial %d/%d: %s (%s)", task.Name, i+1, n,
				map[bool]string{true: "PASS", false: "FAIL"}[res.Pass], res.Reason))
		}
	}
	rep.Rate = float64(rep.Passes) / float64(n)
	sort.Ints(calls)
	if len(calls) > 0 {
		rep.MedianCall = calls[len(calls)/2]
		rep.P90Call = calls[pctIndex(len(calls), 0.9)]
		rep.MaxCall = calls[len(calls)-1]
	}
	sort.Slice(mss, func(i, j int) bool { return mss[i] < mss[j] })
	if len(mss) > 0 {
		rep.MedianMS = mss[len(mss)/2]
		rep.P90MS = mss[pctIndex(len(mss), 0.9)]
	}
	sort.Ints(tins)
	sort.Ints(touts)
	if len(tins) > 0 {
		rep.MedianIn = tins[len(tins)/2]
		rep.MedianOut = touts[len(touts)/2]
	}
	// Local providers often report no usage: surface it so medians
	// are not mistaken for measured efficiency.
	rep.ZeroTokens = true
	for _, v := range tins {
		if v > 0 {
			rep.ZeroTokens = false
			break
		}
	}
	if rep.ZeroTokens {
		for _, v := range touts {
			if v > 0 {
				rep.ZeroTokens = false
				break
			}
		}
	}
	return rep, nil
}

// pctIndex returns the sorted-slice index for percentile p in [0,1].
// Single-element and empty-safe: clamps to the last valid slot.
func pctIndex(n int, p float64) int {
	if n <= 0 {
		return 0
	}
	i := int(p * float64(n))
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

func (r *Runner) runTrial(ctx context.Context, task Task, dir string, trial int) TrialResult {
	res := TrialResult{Task: task.Name, Trial: trial}
	timeout := r.TrialTimeout
	if timeout == 0 {
		timeout = 6 * time.Minute
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	loop := r.NewLoop(dir)
	if task.MaxIters > 0 {
		loop.Cfg.MaxIters = task.MaxIters
	}
	if task.Budget > 0 {
		loop.Cfg.Compactor = &compact.Compactor{Budget: task.Budget}
	}
	m := mode.Build
	if task.Mode == "plan" {
		m = mode.Plan
	}
	loop.Cfg.Mode = m
	var events []agent.Event
	final, err := loop.Run(tctx, task.Goal, func(e agent.Event) {
		events = append(events, e)
		if e.Kind == "tool_call" {
			res.ToolCall++
		}
	})
	res.MS = time.Since(start).Milliseconds()
	res.TokensIn = loop.TotPrompt
	res.TokensOut = loop.TotCompletion
	for _, e := range events {
		if e.Kind == "assistant" {
			res.Turns++
		}
	}
	if err != nil && final == "" && len(events) == 0 {
		res.Reason = "harness error: " + err.Error()
		return res
	}
	// Handoff/loop errors still verify: partial work may satisfy the task.
	// The error itself becomes the reason on failure.
	pass, reason := task.Verify(dir, final, events)
	if pass && len(task.ExpectTools) > 0 {
		for _, want := range task.ExpectTools {
			if !sawTool(events, want) {
				pass, reason = false, fmt.Sprintf("solved without %s (wrong trajectory — output-only pass rejected)", want)
				break
			}
		}
	}
	res.Pass = pass
	res.Reason = reason
	res.Final = trunc(final, 300)
	res.Digest = digest(events)
	if !pass && err != nil {
		res.Reason = fmt.Sprintf("%s (loop: %s)", reason, trunc(err.Error(), 160))
	}
	return res
}

// digest keeps the last 8 tool events (one line each) for the report.
func digest(events []agent.Event) []string {
	var tools []string
	for _, e := range events {
		if e.Kind == "tool_call" || e.Kind == "tool_result" || e.Kind == "handoff" || e.Kind == "compacted" {
			tools = append(tools, e.Kind+": "+trunc(strings.ReplaceAll(e.Text, "\n", " \\ "), 160))
		}
	}
	if len(tools) > 8 {
		tools = tools[len(tools)-8:]
	}
	return tools
}

func trunc(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
