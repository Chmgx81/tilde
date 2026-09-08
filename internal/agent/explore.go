package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"tilde/internal/mode"
	"tilde/internal/tools"
)

// Explore limits: depth 1 (children never spawn), bounded fan-out and time.
const (
	maxExploreTasks = 4
	exploreIters    = 8
	exploreTimeout  = 4 * time.Minute
	// maxExploreCalls caps spawn_explore invocations per parent session.
	// Each call fans out up to 4 children × 8 iters; without a session
	// cap a prompt looping novel task names burns unbounded tokens.
	maxExploreCalls = 8
)

// ExploreTool fans out read-only codebase exploration to scoped child
// loops — each with its own context window — and returns a combined
// summary. The parent never sees raw exploration, only the digest.
// Children are Plan-locked at the registry gate and cannot spawn, so
// writes and recursion are structurally impossible, not just instructed.
// Children share the parent's session log (standard entry types), so a
// resumed session renders their steps as ordinary transcript lines.
type ExploreTool struct {
	// NewChild builds a child loop for one task. Production wires children
	// sharing provider/log with Plan mode; tests inject canned loops.
	NewChild func(task string) *Loop

	mu    sync.Mutex
	calls int
}

func (t *ExploreTool) Name() string { return "spawn_explore" }
func (t *ExploreTool) Description() string {
	return "Explore the codebase in parallel with scoped read-only subagents (up to 4 tasks at once). Each returns a compact summary. Use for independent investigations; read-only, works in Plan mode."
}
func (t *ExploreTool) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"task":  map[string]any{"type": "string", "description": "One investigation task"},
			"tasks": map[string]any{"type": "array", "description": "Several independent tasks (max 4)", "items": map[string]any{"type": "string"}},
		}}
}

type exploreResult struct {
	task    string
	summary string
	err     error
}

func (t *ExploreTool) Exec(ctx context.Context, args map[string]any) (string, error) {
	t.mu.Lock()
	t.calls++
	n := t.calls
	t.mu.Unlock()
	if n > maxExploreCalls {
		return "", fmt.Errorf("subagent budget exhausted (%d explore calls this session) — continue with direct tools instead", maxExploreCalls)
	}
	tasks := exploreTasks(args)
	if len(tasks) == 0 {
		return "", fmt.Errorf("missing task: send {\"task\": \"...\"} or {\"tasks\": [...]} with 1–4 independent investigation topics")
	}
	if len(tasks) > maxExploreTasks {
		return "", fmt.Errorf("got %d tasks, max is %d: split into batches and call again", len(tasks), maxExploreTasks)
	}
	if t.NewChild == nil {
		return "", fmt.Errorf("subagents unavailable in this session (no child factory wired)")
	}
	results := make([]exploreResult, len(tasks))
	var wg sync.WaitGroup
	for i, task := range tasks {
		wg.Add(1)
		go func(i int, task string) {
			defer wg.Done()
			results[i] = t.runOne(ctx, task)
		}(i, task)
	}
	wg.Wait()
	var b strings.Builder
	done := 0
	for _, r := range results {
		if r.err == nil {
			done++
		}
	}
	fmt.Fprintf(&b, "parallel exploration: %d/%d done\n", done, len(results))
	for _, r := range results {
		state := "[done]"
		if r.err != nil {
			state = "[failed]"
		}
		fmt.Fprintf(&b, "│ %-60.60s %s\n", r.task, state)
	}
	for _, r := range results {
		fmt.Fprintf(&b, "--- %s ---\n", r.task)
		if r.err != nil {
			fmt.Fprintf(&b, "[task failed: %v — treat its area as unexplored]\n", r.err)
		} else {
			b.WriteString(strings.TrimSpace(r.summary))
			b.WriteString("\n")
		}
	}
	// Child summaries are model-generated from file content the parent
	// never saw: fence the batch like any other untrusted output.
	return tools.Fence(b.String()), nil
}

func (t *ExploreTool) runOne(ctx context.Context, task string) exploreResult {
	cctx, cancel := context.WithTimeout(ctx, exploreTimeout)
	defer cancel()
	child := t.NewChild(task)
	if child == nil {
		return exploreResult{task: task, err: fmt.Errorf("child factory returned nil")}
	}
	var last, blocked string
	nblocked := 0
	goal := "Explore the codebase read-only and summarize: " + task + ". " +
		"Work this way: 1) list candidate files with glob, 2) read the 2-3 most " +
		"relevant ones with read_file, 3) end with a compact summary of findings " +
		"(under 30 lines). You cannot modify files. Never repeat a call with " +
		"identical arguments — if a call gives nothing useful, try different " +
		"arguments or move on."
	_, err := child.Run(cctx, goal, func(e Event) {
		if e.Kind == "assistant" && e.Text != "" {
			last = e.Text
		}
		if e.Kind == "tool_result" && isBlockNote(e.Text) {
			nblocked++
			if blocked == "" {
				blocked = firstLine(e.Text)
			}
		}
	})
	// Reap strays: a finished scope leaves no background tasks behind.
	// (Children get a private task manager precisely so this is safe —
	// the parent's own tasks are never touched.)
	if child.Reg != nil {
		if sh, ok := child.Reg.Get("shell_command"); ok {
			if shell, ok := sh.(*tools.Shell); ok && shell.Tasks != nil {
				if stopped := shell.Tasks.KillAll(); len(stopped) > 0 {
					last += fmt.Sprintf("\n[note: stopped stray background tasks: %s]", strings.Join(stopped, ", "))
				}
			}
		}
	}
	if err != nil && last == "" {
		return exploreResult{task: task, err: err}
	}
	// A handoff still yields partial findings via the last prose.
	summary := strings.TrimSpace(last)
	if summary == "" {
		summary = "[no findings recorded]"
	}
	if nblocked > 0 {
		summary += fmt.Sprintf("\n[note: %d explorer call(s) blocked as read-only, e.g.: %s]", nblocked, blocked)
	}
	return exploreResult{task: task, summary: summary}
}

func isBlockNote(s string) bool {
	return strings.Contains(s, "blocked") || strings.Contains(s, "denied")
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

func exploreTasks(args map[string]any) []string {
	if arr, ok := args["tasks"].([]any); ok {
		var out []string
		for _, v := range arr {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	}
	if s, ok := args["task"].(string); ok && strings.TrimSpace(s) != "" {
		return []string{s}
	}
	return nil
}

// NewExploreChild builds a production child loop: same provider and log,
// a registry with the explore tool removed (depth cap 1), Plan mode
// enforced at that registry's own gate, denies for any approval.
func NewExploreChild(parent *Loop, task string) *Loop {
	child := &Loop{
		Prov: parent.Prov,
		Log:  parent.Log,
		Cfg: Config{
			MaxIters: exploreIters, DoomRepeats: 3, Root: parent.Cfg.Root,
			Mode: mode.Plan, Pol: parent.Cfg.Pol,
			AskUser: func(string, map[string]any) bool { return false },
		},
	}
	reg := tools.NewRegistry()
	if parent.Reg != nil {
		for _, n := range parent.Reg.Names() {
			if n == "spawn_explore" {
				continue // depth cap: children never spawn
			}
			tl, ok := parent.Reg.Get(n)
			if !ok {
				continue
			}
			// Fresh per-child instances for stateful tools: concurrent
			// children must not thrash one read cache or share one task
			// manager (struct copies would copy mutexes — construct).
			// Stateless tools are safe to share.
			switch t := tl.(type) {
			case *tools.ReadFile:
				reg.Register(&tools.ReadFile{Root: t.Root, Seen: tools.NewSeenMap(parent.Cfg.Root)})
			case *tools.Grep:
				reg.Register(&tools.Grep{Root: t.Root, Seen: tools.NewSeenMap(parent.Cfg.Root)})
			case *tools.WriteFile:
				reg.Register(&tools.WriteFile{Root: t.Root, Seen: tools.NewSeenMap(parent.Cfg.Root)})
			case *tools.EditFile:
				reg.Register(&tools.EditFile{Root: t.Root, Seen: tools.NewSeenMap(parent.Cfg.Root)})
			case *tools.Shell:
				reg.Register(&tools.Shell{Root: t.Root, BgAfter: t.BgAfter, BgMax: t.BgMax, Sandbox: t.Sandbox, Tasks: &tools.TaskManager{}})
			default:
				reg.Register(tl)
			}
		}
	}
	reg.Gate = func(toolName string, _ map[string]any) (bool, string) {
		if err := child.GetMode().Allowed(toolName); err != nil {
			return false, err.Error()
		}
		return true, ""
	}
	child.Reg = reg
	return child
}

var _ tools.Tool = (*ExploreTool)(nil)
