package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tilde/internal/mode"
	"tilde/internal/tools"
)

// Work session limits: single writer, bounded fan-out and time.
const (
	// maxWorkCalls caps spawn_work invocations per parent session — the
	// same pattern as explore's maxExploreCalls: each call runs a child
	// loop, so a prompt looping novel tasks would otherwise burn
	// unbounded tokens.
	maxWorkCalls = 8
	workIters    = 12
	workTimeout  = 6 * time.Minute
)

// WorkState is the single-writer session shared by the spawn/apply/
// discard tools: at most one active worktree at a time. A second spawn
// while one is pending refuses with the fix named (apply or discard).
type WorkState struct {
	mu sync.Mutex
	// Root is the parent repo root (worktree operations run here).
	Root string
	// NewChild builds a writer child loop confined to workRoot.
	// Production wires NewWorkChild; tests inject canned loops.
	NewChild func(task, workRoot string) *Loop

	active string // repo-relative or absolute path of the pending worktree
	calls  int
}

// resolve maps a repo-relative or absolute work path to a clean absolute.
func (s *WorkState) resolve(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(s.Root, p))
}

// checkActive verifies a session is pending at path. Callers must NOT
// hold s.mu.
func (s *WorkState) checkActive(path string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == "" {
		return "", fmt.Errorf("no active work session: spawn one with spawn_work {\"task\": \"...\"} first")
	}
	if s.resolve(path) != s.resolve(s.active) {
		return "", fmt.Errorf("unknown work session %q: active session is %q — pass its path to apply_work or discard_work", path, s.active)
	}
	return s.active, nil
}

// clearActive drops the reservation when it still points at path.
func (s *WorkState) clearActive(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != "" && s.resolve(s.active) == s.resolve(path) {
		s.active = ""
	}
}

// SpawnWorkTool runs one isolated writer child in a fresh git worktree.
// The child is Plan-locked except the whitelisted write tools, cannot
// spawn, and works only inside the worktree. The worktree is kept after
// the child finishes: review it with apply_work (diff + keep) or drop it
// with discard_work.
type SpawnWorkTool struct {
	State *WorkState
}

func (t *SpawnWorkTool) Name() string { return "spawn_work" }
func (t *SpawnWorkTool) Description() string {
	return "Run one isolated writer subagent in a fresh git worktree (single-writer: one active session at a time). The child can read and write inside the worktree only; review with apply_work, drop with discard_work."
}
func (t *SpawnWorkTool) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"task":   map[string]any{"type": "string", "description": "What to implement in the worktree"},
			"path":   map[string]any{"type": "string", "description": "New worktree dir (repo-relative or absolute, default .tilde-work/w<N>)"},
			"branch": map[string]any{"type": "string", "description": "Branch to check out (created with -B if missing)"},
		}, "required": []string{"task"}}
}

func (t *SpawnWorkTool) Exec(ctx context.Context, args map[string]any) (string, error) {
	st := t.State
	if st == nil || st.Root == "" {
		return "", fmt.Errorf("work sessions unavailable in this session (no work state wired)")
	}
	st.mu.Lock()
	st.calls++
	n := st.calls
	active := st.active
	st.mu.Unlock()
	if n > maxWorkCalls {
		return "", fmt.Errorf("subagent budget exhausted (%d work calls this session) — continue with direct tools instead", maxWorkCalls)
	}
	if active != "" {
		return "", fmt.Errorf("work session already active at %q: finish it with apply_work {\"path\": %q} or discard_work {\"path\": %q} before spawning another", active, active, active)
	}
	task, err := workStrArg(args, "task")
	if err != nil {
		return "", err
	}
	path := workOptStr(args, "path", fmt.Sprintf(".tilde-work/w%d", n))
	if strings.HasPrefix(path, "-") {
		return "", fmt.Errorf("refusing work path %q: must not start with - (flag injection) — send a plain directory path", path)
	}
	branch := workOptStr(args, "branch", "")
	// Reserve the single-writer slot before the child runs so a
	// concurrent spawn refuses while this one is in flight.
	st.mu.Lock()
	if st.active != "" {
		dup := st.active
		st.mu.Unlock()
		return "", fmt.Errorf("work session already active at %q: finish it with apply_work {\"path\": %q} or discard_work {\"path\": %q} before spawning another", dup, dup, dup)
	}
	st.active = path
	st.mu.Unlock()
	if st.NewChild == nil {
		st.clearActive(path)
		return "", fmt.Errorf("subagents unavailable in this session (no child factory wired)")
	}
	// Reuse the hardened worktree creation (containment + flag checks).
	addArgs := map[string]any{"path": path}
	if branch != "" {
		addArgs["branch"] = branch
	}
	if _, err := (&tools.GitWorktreeAdd{Root: st.Root}).Exec(ctx, addArgs); err != nil {
		st.clearActive(path)
		return "", err
	}
	summary, err := st.runWork(ctx, task, path, st.resolve(path))
	if err != nil {
		// Keep the reservation: partial work stays inspectable via
		// apply_work or discard_work.
		return "", fmt.Errorf("writer failed in worktree at %q: %v — inspect with apply_work {\"path\": %q} or drop it with discard_work {\"path\": %q}", path, err, path, path)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "work session ready at %s (task_id: work_%d)\n%s\nFinish with apply_work {\"path\": %q} to review the diff (kept), or discard_work {\"path\": %q} to remove it.",
		path, n, summary, path, path)
	// Child summaries are model-generated from content the parent never
	// saw: fence the batch like any other untrusted output.
	return tools.Fence(b.String()), nil
}

// ApplyWorkTool shows the pending worktree's diff and keeps it on disk
// for the user to merge. It closes the single-writer session so a new
// spawn is allowed.
type ApplyWorkTool struct {
	State *WorkState
}

func (t *ApplyWorkTool) Name() string { return "apply_work" }
func (t *ApplyWorkTool) Description() string {
	return "Review the active work session: show its diff and keep the worktree on disk for merging. Closes the session."
}
func (t *ApplyWorkTool) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Worktree dir from spawn_work"},
		}, "required": []string{"path"}}
}

func (t *ApplyWorkTool) Exec(ctx context.Context, args map[string]any) (string, error) {
	st := t.State
	if st == nil || st.Root == "" {
		return "", fmt.Errorf("work sessions unavailable in this session (no work state wired)")
	}
	path, err := workStrArg(args, "path")
	if err != nil {
		return "", err
	}
	active, err := st.checkActive(path)
	if err != nil {
		return "", err
	}
	abs := st.resolve(path)
	status, _ := (&tools.GitStatus{Root: abs}).Exec(ctx, map[string]any{})
	diff, derr := (&tools.GitDiff{Root: abs}).Exec(ctx, map[string]any{})
	if derr != nil {
		return "", derr
	}
	st.clearActive(active)
	var b strings.Builder
	fmt.Fprintf(&b, "applied work at %s (kept on disk — merge it yourself)\nstatus: %s\ndiff:\n%s", path, status, diff)
	return tools.Fence(b.String()), nil
}

// DiscardWorkTool removes the pending worktree and closes the session.
// Refuses when dirty (commit/stash inside first, then retry) — never
// silently drops in-flight work.
type DiscardWorkTool struct {
	State *WorkState
}

func (t *DiscardWorkTool) Name() string { return "discard_work" }
func (t *DiscardWorkTool) Description() string {
	return "Remove the active work session's worktree and close the session. Refuses when dirty."
}
func (t *DiscardWorkTool) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Worktree dir from spawn_work"},
		}, "required": []string{"path"}}
}

func (t *DiscardWorkTool) Exec(ctx context.Context, args map[string]any) (string, error) {
	st := t.State
	if st == nil || st.Root == "" {
		return "", fmt.Errorf("work sessions unavailable in this session (no work state wired)")
	}
	path, err := workStrArg(args, "path")
	if err != nil {
		return "", err
	}
	active, err := st.checkActive(path)
	if err != nil {
		return "", err
	}
	out, err := (&tools.GitWorktreeRemove{Root: st.Root}).Exec(ctx, map[string]any{"path": path})
	if err != nil {
		return "", err // keep the reservation: nothing was removed
	}
	st.clearActive(active)
	return out, nil
}

// runWork executes the writer child to completion and returns its
// summary. Mirrors ExploreTool.runOne (timeout, last-prose capture,
// stray reaping) with a writer goal.
func (s *WorkState) runWork(ctx context.Context, task, workPath, absRoot string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, workTimeout)
	defer cancel()
	child := s.NewChild(task, absRoot)
	if child == nil {
		return "", fmt.Errorf("child factory returned nil")
	}
	var last, blocked string
	nblocked := 0
	goal := "Implement the task with file writes confined to the worktree at " + workPath + ": " + task + ". " +
		"Work this way: 1) read the relevant files with read_file, 2) make the change with write_file/edit_file, " +
		"3) end with a compact summary of what changed (under 30 lines). " +
		"Write only inside the worktree. Never repeat a call with identical arguments — if a call gives nothing useful, try different " +
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
		return "", err
	}
	summary := strings.TrimSpace(last)
	if summary == "" {
		summary = "[no findings recorded]"
	}
	if nblocked > 0 {
		summary += fmt.Sprintf("\n[note: %d writer call(s) blocked, e.g.: %s]", nblocked, blocked)
	}
	return summary, nil
}

// NewWorkChild builds a production writer child loop: same provider and
// log, workRoot as its file root, Plan mode with only write_file and
// edit_file whitelisted, no spawning and no worktree tools (the writer
// stays inside its worktree — it cannot open new checkouts).
func NewWorkChild(parent *Loop, task, workRoot string) *Loop {
	child := &Loop{
		Prov: parent.Prov,
		Log:  parent.Log,
		Cfg: Config{
			MaxIters: workIters, DoomRepeats: 3, Root: workRoot,
			Mode: mode.Plan, Pol: parent.Cfg.Pol,
			AskUser:       func(string, map[string]any) bool { return false },
			FlushParallel: parent.Cfg.FlushParallel,
			// The write whitelist: Plan-locked except these two, enforced
			// at both the loop gate and the registry gate below.
			PlanAllow: []string{"write_file", "edit_file"},
		},
	}
	_ = task
	childTasks := &tools.TaskManager{}
	reg := tools.NewRegistry()
	// One shared read-state store for all child file tools — exactly
	// like the parent wiring (main.go): a read marks, a later write
	// checks the SAME map. Per-tool maps would refuse every overwrite
	// as unseen.
	seen := tools.NewSeenMap(workRoot)
	if parent.Reg != nil {
		for _, n := range parent.Reg.Names() {
			switch n {
			case "spawn_explore", "spawn_work", "apply_work", "discard_work":
				continue // depth cap: writers never spawn
			case "git_worktree_add", "git_worktree_remove":
				continue // writer stays inside its worktree
			}
			tl, ok := parent.Reg.Get(n)
			if !ok {
				continue
			}
			// Fresh per-child instances for stateful tools, re-rooted
			// at the worktree so reads/writes cannot escape it.
			// (Struct copies would copy mutexes — construct.)
			switch t := tl.(type) {
			case *tools.ReadFile:
				reg.Register(&tools.ReadFile{Root: workRoot, Seen: seen})
			case *tools.Grep:
				reg.Register(&tools.Grep{Root: workRoot, Seen: seen})
			case *tools.Glob:
				reg.Register(&tools.Glob{Root: workRoot})
			case *tools.WriteFile:
				reg.Register(&tools.WriteFile{Root: workRoot, Seen: seen})
			case *tools.EditFile:
				reg.Register(&tools.EditFile{Root: workRoot, Seen: seen})
			case *tools.Shell:
				reg.Register(&tools.Shell{Root: workRoot, BgAfter: t.BgAfter, BgMax: t.BgMax, Sandbox: t.Sandbox, Tasks: childTasks})
			case *tools.ShellPoll:
				reg.Register(&tools.ShellPoll{Tasks: childTasks})
			case *tools.GitStatus:
				reg.Register(&tools.GitStatus{Root: workRoot})
			case *tools.GitDiff:
				reg.Register(&tools.GitDiff{Root: workRoot})
			case *tools.GitWorktreeList:
				reg.Register(&tools.GitWorktreeList{Root: workRoot})
			default:
				reg.Register(tl)
			}
		}
	}
	reg.Gate = func(toolName string, args map[string]any) (bool, string) {
		// The write whitelist: Plan-locked except these two.
		if toolName == "write_file" || toolName == "edit_file" {
			return true, ""
		}
		if err := child.GetMode().AllowedCall(toolName, args); err != nil {
			return false, err.Error()
		}
		return true, ""
	}
	child.Reg = reg
	return child
}

// workStrArg extracts a required string arg with a model-readable error.
func workStrArg(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return "", fmt.Errorf("missing required argument %q: pass %q as a string", key, key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string, got %T with value %v: resend as a plain string", key, v, v)
	}
	if strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("argument %q is empty: provide a non-empty value", key)
	}
	return s, nil
}

// workOptStr extracts an optional string arg.
func workOptStr(args map[string]any, key, def string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return def
}

var (
	_ tools.Tool = (*SpawnWorkTool)(nil)
	_ tools.Tool = (*ApplyWorkTool)(nil)
	_ tools.Tool = (*DiscardWorkTool)(nil)
)
