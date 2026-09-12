package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"tilde/internal/sandbox"
)

// --- background task plumbing ---

// Task is one detached shell command.
type Task struct {
	ID      string
	Command string
	LogPath string
	Started time.Time

	cmd  *exec.Cmd
	done chan struct{}

	mu       sync.Mutex
	exitCode int
}

// Running reports whether the process is still going.
func (t *Task) Running() bool {
	select {
	case <-t.done:
		return false
	default:
		return true
	}
}

// Status renders one line: running, or done with the honest exit code.
func (t *Task) Status() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case <-t.done:
		return fmt.Sprintf("done exit=%d", t.exitCode)
	default:
		return fmt.Sprintf("running %ds — log: %s", int(time.Since(t.Started).Seconds()), t.LogPath)
	}
}

// Kill stops the task. Idempotent: killing a finished task says so.
// When the command was started as its own process group (Setpgid,
// set in buildCmd), kill the whole group so descendants are reaped too —
// never just the direct bash/podman client.
func (t *Task) Kill() string {
	if !t.Running() {
		return fmt.Sprintf("task %s already finished (%s).", t.ID, t.Status())
	}
	if t.cmd.Process == nil {
		return fmt.Sprintf("task %s has no live process handle.", t.ID)
	}
	if t.cmd.SysProcAttr != nil && t.cmd.SysProcAttr.Setpgid {
		_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
	} else {
		if err := t.cmd.Process.Kill(); err != nil {
			return fmt.Sprintf("could not kill task %s: %v — it may have just exited; check status.", t.ID, err)
		}
	}
	<-t.done
	return fmt.Sprintf("killed task %s.", t.ID)
}

// TaskManager tracks background tasks for one session.
type TaskManager struct {
	mu    sync.Mutex
	next  int
	tasks map[string]*Task
	// reserved counts starts that have passed the cap check but have not
	// finished creating and registering their process yet. It closes the
	// check-then-start window when several callers start concurrently.
	reserved int
	// LogDir overrides the default per-process temp dir (tests).
	LogDir string
	// MaxTasks caps concurrently running background tasks (default 16).
	// Zero means default. Start refuses past the cap with a recovery note.
	MaxTasks int
	// dir is the lazily-created private log dir for this manager.
	// Per-process (os.MkdirTemp) so a pre-created /tmp/tilde-tasks
	// symlink farm on a multi-user host cannot redirect our logs:
	// every manager owns a 0700 dir only it can write to.
	dir string
}

// defaultMaxTasks caps concurrently running background tasks per manager.
const defaultMaxTasks = 16

// maxTasks reports the running-task cap (MaxTasks override or default).
// MaxTasks is write-once at construction, so no lock is needed to read it.
func (m *TaskManager) maxTasks() int {
	if m != nil && m.MaxTasks > 0 {
		return m.MaxTasks
	}
	return defaultMaxTasks
}

func (m *TaskManager) logDir() string {
	// Whole body under mu: the LogDir fast path must not race a
	// concurrent write, and the lazy dir must be created exactly once.
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.LogDir != "" {
		return m.LogDir
	}
	if m.dir != "" {
		return m.dir
	}
	d, err := os.MkdirTemp("", "tilde-tasks-*")
	if err != nil {
		// Fail back to the legacy shared path only when temp creation
		// itself fails; the log open below still uses O_EXCL|O_NOFOLLOW
		// so a planted symlink can never be followed/truncated.
		return filepath.Join(os.TempDir(), "tilde-tasks")
	}
	m.dir = d
	return m.dir
}

// Start launches command in the background, streaming to a log file.
// build must return an un-started *exec.Cmd writing to out/errW.
// The task owns bgCancel and calls it when the process ends.
func (m *TaskManager) Start(command string, bgCtx context.Context, bgCancel context.CancelFunc, build func(stdout, stderr io.Writer) *exec.Cmd) (*Task, error) {
	// Commands are user/model supplied and may contain credentials in flags or
	// URLs. Keep the executable command private, but redact every copy exposed
	// through task metadata and logs.
	displayCommand, _ := Scrub(command)
	m.mu.Lock()
	if m.tasks == nil {
		m.tasks = map[string]*Task{}
	}
	m.pruneLocked() // finished tasks never accumulate across starts
	if n := len(m.tasks) + m.reserved; n >= m.maxTasks() {
		max := m.maxTasks()
		m.mu.Unlock()
		return nil, fmt.Errorf("task limit reached (%d running tasks, max %d): poll one with shell_poll {\"action\": \"status\", \"task_id\": \"...\"} or stop one with shell_poll {\"action\": \"kill\", \"task_id\": \"...\"} before starting another", n, max)
	}
	// Hold the slot across log creation and cmd.Start. A concurrent caller
	// must see this in-flight start as occupying capacity, and every failure
	// path below releases it before returning.
	m.reserved++
	m.next++
	id := fmt.Sprintf("task_%d", m.next)
	m.mu.Unlock()
	releaseReservation := func() {
		m.mu.Lock()
		m.reserved--
		m.mu.Unlock()
	}

	if err := os.MkdirAll(m.logDir(), 0o700); err != nil {
		releaseReservation()
		return nil, fmt.Errorf("cannot create task log dir: %v", err)
	}
	logPath := filepath.Join(m.logDir(), id+".log")
	// O_EXCL|O_NOFOLLOW: task ids are monotonic per manager so the file
	// must never exist yet; a pre-planted symlink or file aborts here
	// instead of truncating/following it (see dir comment above).
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		releaseReservation()
		return nil, fmt.Errorf("cannot create task log %q: %v", logPath, err)
	}
	if _, err := fmt.Fprintf(lf, "$ %s\n[started %s]\n", displayCommand, time.Now().UTC().Format(time.RFC3339)); err != nil {
		lf.Close()
		releaseReservation()
		return nil, fmt.Errorf("cannot write task log %q: %v", logPath, err)
	}

	t := &Task{ID: id, Command: displayCommand, LogPath: logPath, Started: time.Now(), done: make(chan struct{})}
	cmd := build(lf, lf)
	t.cmd = cmd
	if err := cmd.Start(); err != nil {
		lf.Close()
		releaseReservation()
		return nil, fmt.Errorf("could not start background command: %v — retry in the foreground or simplify the command", err)
	}
	go func() {
		defer close(t.done)
		defer lf.Close()
		defer bgCancel()
		err := cmd.Wait()
		t.mu.Lock()
		t.exitCode = honestCode(cmd, err)
		t.mu.Unlock()
		if _, ferr := fmt.Fprintf(lf, "\n[finished exit=%d]\n", t.exitCode); ferr != nil {
			fmt.Fprintf(os.Stderr, "tilde: task log write for %s: %v\n", t.ID, ferr)
		}
	}()
	m.mu.Lock()
	m.tasks[id] = t
	m.reserved--
	m.mu.Unlock()
	return t, nil
}

// prune drops finished tasks from the map. Callers must hold m.mu.
func (m *TaskManager) pruneLocked() {
	for id, t := range m.tasks {
		select {
		case <-t.done:
			delete(m.tasks, id)
		default:
		}
	}
}

// forget drops one finished task from the map (no-op when still running).
func (m *TaskManager) forget(id string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tasks[id]; ok {
		select {
		case <-t.done:
			delete(m.tasks, id)
		default:
		}
	}
}

// RunningIDs lists in-flight background tasks, sorted for stable output.
func (m *TaskManager) RunningIDs() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for id, t := range m.tasks {
		select {
		case <-t.done:
		default:
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// KillAll stops every running task. Used to reap a finished scope's
// strays (e.g. subagent children) so background work never outlives the
// context that started it. Returns stopped ids.
func (m *TaskManager) KillAll() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	tasks := make([]*Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		tasks = append(tasks, t)
	}
	m.mu.Unlock()
	var stopped []string
	for _, t := range tasks {
		if t.Running() {
			t.Kill()
			stopped = append(stopped, t.ID)
		}
	}
	if m != nil {
		m.mu.Lock()
		m.pruneLocked() // reaped strays leave no map residue
		m.mu.Unlock()
	}
	return stopped
}

// Get fetches a task by id, naming the fix when unknown.
func (m *TaskManager) Get(id string) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tasks[id]; ok {
		return t, nil
	}
	var ids []string
	for k := range m.tasks {
		ids = append(ids, k)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("unknown task %q and no tasks exist yet: start one with shell_command {background: true}, then poll it", id)
	}
	return nil, fmt.Errorf("unknown task %q: known tasks: %s — check the id and retry", id, strings.Join(ids, ", "))
}

// --- shell_command (always sandbox-wrapped in production) ---

type Shell struct {
	Root string
	// BgAfter auto-backgrounds commands still running after this long
	// (default 30s) — the agent is never blocked babysitting a build.
	BgAfter time.Duration
	// BgMax caps a background task's total runtime (default 30m).
	BgMax time.Duration
	// Sandbox, when non-nil, wraps execution in bwrap (docs/Plan.md Phase 2).
	// Main always sets it; tests leave it nil to exercise raw semantics.
	Sandbox *sandbox.Config
	// Tasks tracks background jobs. Nil → the shared fallback below.
	Tasks *TaskManager
}

func (t *Shell) Name() string { return "shell_command" }
func (t *Shell) Description() string {
	return "Run a shell command in the project directory. Returns exit code, stdout, stderr. Commands running longer than the detach threshold (default 30s, or timeout:) move to the background and return a task id + log path instead of blocking; poll with shell_poll. When approval is required, optionally provide a concise reason to show the user."
}
func (t *Shell) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"command":    map[string]any{"type": "string", "description": "Shell command, e.g. go test ./..."},
			"reason":     map[string]any{"type": "string", "description": "Optional concise reason shown in the approval prompt; informational only"},
			"background": map[string]any{"type": "boolean", "description": "Start detached immediately; returns task id + log path"},
			"timeout":    map[string]any{"type": "integer", "description": "Seconds before the call detaches to background (default 30, capped by the background max)"},
		}, "required": []string{"command"}}
}

func (t *Shell) timeouts() (detach, max time.Duration) {
	detach = 30 * time.Second
	if t.BgAfter != 0 {
		detach = t.BgAfter
	}
	max = t.BgMax
	if max == 0 {
		max = 30 * time.Minute
	}
	return detach, max
}

func (t *Shell) manager() *TaskManager {
	if t.Tasks != nil {
		return t.Tasks
	}
	return sharedTasks
}

func (t *Shell) buildCmd(ctx context.Context, cmdStr string, stdout, stderr io.Writer) (*exec.Cmd, string, error) {
	var warn string
	if t.Sandbox != nil {
		sb := *t.Sandbox
		sb.Root = t.Root
		cmd, err := sb.Command(ctx, cmdStr)
		if err != nil {
			return nil, "", err // fail closed, with the fix named inside
		}
		if sandbox.Disabled() {
			warn = "[warning: sandbox disabled via TILDE_NO_SANDBOX=1 — command ran unsandboxed]\n"
		}
		cmd.Stdout, cmd.Stderr = stdout, stderr
		// New process group so a delayed kill can reap the whole tree
		// (the bwrap/lazy process and any descendants), not just the
		// direct child — same guard the hooks path uses.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return cmd, warn, nil
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", cmdStr)
	cmd.Dir = t.Root
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd, "", nil
}

// maxShellCommandBytes bounds one command string. Far above any real
// command (even a generous heredoc) and far below pathological input, so a
// runaway payload cannot bloat the session log and model context in a
// single call. The refusal names the fix.
const maxShellCommandBytes = 64 * 1024

func (t *Shell) Exec(ctx context.Context, args map[string]any) (string, error) {
	cmdStr, err := strArg(args, "command")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(cmdStr) == "" {
		return "", fmt.Errorf("command is blank: send a non-empty shell command")
	}
	if len(cmdStr) > maxShellCommandBytes {
		return "", fmt.Errorf("command is %d bytes (over the %d-byte cap): split it into smaller commands, or write the long text to a file with write_file and reference that file", len(cmdStr), maxShellCommandBytes)
	}
	detachDefault, max := t.timeouts()
	detachAfter := detachDefault
	if s := optInt(args, "timeout", 0); s > 0 {
		detachAfter = time.Duration(s) * time.Second
		if detachAfter > max {
			detachAfter = max
		}
	}
	// The per-call timeout shortens the default; it never kills — the task
	// keeps running under the background cap (BgMax).
	explicitBG := false
	if b, ok := args["background"].(bool); ok && b {
		explicitBG = true
	}

	mgr := t.manager()
	// Detached tasks must outlive the requesting turn: derive from a
	// cancellation-free parent so a later turn cancel doesn't kill a task
	// the caller was invited to poll across turns. Still bounded by BgMax.
	bgCtx, bgCancel := context.WithTimeout(context.WithoutCancel(ctx), max)

	task, terr := mgr.Start(cmdStr, bgCtx, bgCancel, func(stdout, stderr io.Writer) *exec.Cmd {
		cmd, _, buildErr := t.buildCmd(bgCtx, cmdStr, stdout, stderr)
		if buildErr != nil {
			// Surface build failures (e.g. missing bwrap) as a failed task
			// rather than a nil command: wrap in a stub that exits loudly.
			stub := exec.CommandContext(bgCtx, "bash", "-c", fmt.Sprintf("echo %q >&2; exit 127", buildErr.Error()))
			stub.Stdout, stub.Stderr = stdout, stderr
			return stub
		}
		return cmd
	})
	if terr != nil {
		bgCancel()
		return "", terr
	}

	if explicitBG {
		return fmt.Sprintf("task %s started in the background.\nlog: %s\nPoll with shell_poll {\"action\": \"log\", \"task_id\": %q}; stop with {\"action\": \"kill\"}.",
			task.ID, task.LogPath, task.ID), nil
	}

	timer := time.NewTimer(detachAfter)
	defer timer.Stop()
	select {
	case <-task.done:
		return t.foregroundResult(task, cmdStr), nil
	case <-timer.C:
		return fmt.Sprintf("task %s still running after %s — moved to the background instead of blocking.\nlog: %s\nPoll with shell_poll {\"action\": \"log\", \"task_id\": %q}; stop with {\"action\": \"kill\"}.",
			task.ID, detachAfter, task.LogPath, task.ID), nil
	case <-ctx.Done():
		killNote := task.Kill()
		bgCancel()
		return "", fmt.Errorf("command cancelled while starting: %s — resend it with \"background\": true to run detached", killNote)
	}
}

// foregroundResult formats a completed command: honest exit, benign-exit
// annotations, untrusted-output fencing. Harness notes stay outside the fence.
func (t *Shell) foregroundResult(task *Task, cmdStr string) string {
	task.mu.Lock()
	code := task.exitCode
	task.mu.Unlock()
	// A completed foreground task is fully consumed here — drop it so the
	// map only ever holds live or detached-background work.
	t.manager().forget(task.ID)
	raw, rerr := os.ReadFile(task.LogPath)
	var body string
	if rerr != nil {
		body = fmt.Sprintf("[cannot read task log %s: %v]", task.LogPath, rerr)
	} else {
		body = string(raw)
	}
	// Strip the "$ cmd / [started] / [finished]" wrapper lines for display.
	lines := strings.Split(body, "\n")
	var disp []string
	for _, ln := range lines {
		if strings.HasPrefix(ln, "$ ") || strings.HasPrefix(ln, "[started ") || strings.HasPrefix(ln, "[finished ") {
			continue
		}
		disp = append(disp, ln)
	}
	out := strings.TrimRight(strings.Join(disp, "\n"), "\n")
	if len(out) > 8000 {
		shown := out[:8000]
		// The log keeps the "$ cmd" + "[started]" wrapper lines, so the
		// shown output starts at log line 3: resume from there.
		resume := 3 + strings.Count(shown, "\n")
		out = shown + fmt.Sprintf("\n[truncated: output capped at 8000 chars (full stream in %s) — poll it with shell_poll {\"action\": \"log\", \"task_id\": %q, \"offset\": %d} to continue, or narrow the command and retry]", task.LogPath, task.ID, resume)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "exit=%d\n", code)
	b.WriteString(Fence(nonEmpty(out, "[no output]")))
	if note := benignNote(cmdStr, code); note != "" {
		b.WriteString("\n")
		b.WriteString(note)
	}
	return b.String()
}

// honestCode maps a finished process to its true exit code: a signal-killed
// process reports 128+N (SIGKILL → 137), never a fake success or -1.
func honestCode(cmd *exec.Cmd, runErr error) int {
	if runErr == nil {
		return 0
	}
	if cmd.ProcessState == nil {
		return 127 // never started: loud non-zero, not silence
	}
	if st, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
		if st.Signaled() {
			return 128 + int(st.Signal())
		}
		return st.ExitStatus()
	}
	return cmd.ProcessState.ExitCode()
}

// benignNote annotates common non-error exits so models trained on
// "nonzero = failure" don't spin retry loops.
func benignNote(cmdStr string, code int) string {
	if code == 0 {
		return ""
	}
	first := ""
	if f := strings.Fields(cmdStr); len(f) > 0 {
		first = filepath.Base(f[0])
	}
	switch first {
	case "grep":
		if code == 1 {
			return "[note: exit 1 from grep means \"no matches found\" — not an error. Do not retry; broaden the pattern instead.]"
		}
		return "[note: grep exit 2 is a real error (bad pattern or unreadable path) — fix and retry.]"
	case "diff":
		if code == 1 {
			return "[note: exit 1 from diff means \"differences found\" — not an error.]"
		}
	}
	return ""
}

func nonEmpty(s, alt string) string {
	if strings.TrimSpace(s) == "" {
		return alt
	}
	return s
}

// --- shell_poll ---

// ShellPoll inspects or stops background tasks: status | log | kill.
type ShellPoll struct {
	Tasks *TaskManager
}

func (t *ShellPoll) Name() string { return "shell_poll" }
func (t *ShellPoll) Description() string {
	return "Check a background shell task: action status (running/done), log (paginated output), or kill."
}
func (t *ShellPoll) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"action":  map[string]any{"type": "string", "description": "status | log | kill"},
			"task_id": map[string]any{"type": "string"},
			"offset":  map[string]any{"type": "integer", "description": "log: 1-based start line, default 1"},
			"limit":   map[string]any{"type": "integer", "description": "log: max lines, default 100"},
		}, "required": []string{"action", "task_id"}}
}

func (t *ShellPoll) manager() *TaskManager {
	if t.Tasks != nil {
		return t.Tasks
	}
	return sharedTasks
}

// sharedTasks is the fallback manager for tools built without one.
// Production injects an explicit instance; this keeps every other
// construction (tests, subagent children without wiring) on one shared
// manager so tasks stay pollable instead of stranding. TaskManager is
// internally mutex-guarded, so sharing is race-safe.
var sharedTasks = &TaskManager{}

func (t *ShellPoll) Exec(_ context.Context, args map[string]any) (string, error) {
	action, err := strArg(args, "action")
	if err != nil {
		return "", err
	}
	id, err := strArg(args, "task_id")
	if err != nil {
		return "", err
	}
	task, err := t.manager().Get(id)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(action) {
	case "status":
		return fmt.Sprintf("task %s (%s): %s", id, task.Command, task.Status()), nil
	case "kill":
		return task.Kill(), nil
	case "log":
		return t.log(task, args)
	default:
		return "", fmt.Errorf("unknown action %q: send status, log, or kill", action)
	}
}

func (t *ShellPoll) log(task *Task, args map[string]any) (string, error) {
	data, err := os.ReadFile(task.LogPath)
	if err != nil {
		return "", fmt.Errorf("cannot read log for %s: %v — the task may have just started; retry status first", task.ID, err)
	}
	lines := strings.Split(string(data), "\n")
	offset := optInt(args, "offset", 1)
	limit := optInt(args, "limit", 100)
	if offset < 1 {
		return "", fmt.Errorf("offset must be >= 1, got %d", offset)
	}
	if limit < 1 || limit > 2000 {
		return "", fmt.Errorf("limit must be 1..2000, got %d", limit)
	}
	if offset > len(lines) {
		return fmt.Sprintf("task %s: %s — log has %d lines; nothing past offset %d yet. Poll again later.", task.ID, task.Status(), len(lines), offset), nil
	}
	end := offset - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}
	out := strings.Join(lines[offset-1:end], "\n")
	if end < len(lines) {
		out += fmt.Sprintf("\n[%d more log lines — poll with offset=%d to continue]", len(lines)-end, end+1)
	}
	return fmt.Sprintf("task %s: %s\n", task.ID, task.Status()) + Fence(out), nil
}
