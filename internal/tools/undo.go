package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// UndoManager snapshots every mutating step so any single bad edit can be
// reverted without losing the rest of the session (docs/Plan.md Phase 5).
//
// Two tiers, honestly separated:
//   - write_file / edit_file: exact per-file backups (bytes before the op,
//     or a created-marker for new files). Precise, git-independent.
//   - shell_command: `git stash create` snapshot of the tracked tree.
//     Restores tracked files; untracked shell effects are reported, not
//     pretended about — the report says what's covered.
//
// Entries apply LIFO only: /undo [n] reverts the last n steps.
type UndoManager struct {
	Root string
	// MaxEntries caps the stack (default 50). Oldest beyond the cap drop
	// with the eviction recorded — a bounded stack beats unbounded disk.
	MaxEntries int

	mu    sync.Mutex
	stack []UndoEntry
	seq   int
}

// UndoEntry is one reversible step.
type UndoEntry struct {
	Seq    int
	Tool   string
	Target string // path or command, for humans
	Stash  string // git stash-create hash for shell steps ("" when none)
	// Dirty records whether tracked files differed from HEAD at snapshot
	// time. It disambiguates an empty Stash: clean tree (safe to restore
	// via HEAD) vs. failed snapshot on a dirty tree (refuse, never guess —
	// guessing wrong wipes uncommitted work).
	Dirty bool
	// After is the tracked-tree content hash right after a shell step
	// completed. Reverting requires the tree to still match: if later work
	// (or hands outside tilde) changed tracked files since, the restore
	// refuses rather than silently discarding it. Untracked files are
	// excluded — shell-undo never covered those anyway. Status strings
	// alone can't catch content swaps, so this hashes the actual diff.
	After string
	// Backups maps absolute path → content before the op.
	// Present key + nil content = the op created this file (undo deletes it).
	Backups map[string]*[]byte
	// Worktree, when set, is a worktree path added by this step:
	// undo removes it (git refuses when dirty — cannot discard work).
	Worktree string
}

func (u *UndoManager) maxEntries() int {
	if u.MaxEntries > 0 {
		return u.MaxEntries
	}
	return 50
}

// snapshotTools are auto-snapshotted on dispatch.
func snapshotTools(name string) bool {
	switch name {
	case "write_file", "edit_file", "shell_command", "git_worktree_add":
		return true
	}
	return false
}

// SnapshotFor records the pre-op state. It runs after the mode/policy
// gate and before Exec, and reports whether an entry was actually pushed —
// callers discard on Exec failure, so failed calls never pollute the stack
// (and, critically, never pop an unrelated entry).
func (u *UndoManager) SnapshotFor(tool string, args map[string]any) bool {
	if u == nil || !snapshotTools(tool) {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.seq++
	e := UndoEntry{Seq: u.seq, Tool: tool, Backups: map[string]*[]byte{}}
	switch tool {
	case "write_file", "edit_file":
		if p, ok := args["path"].(string); ok && p != "" {
			e.Target = p
			full, err := contain(u.Root, p)
			if err != nil {
				return false // Exec will refuse; leave no trace
			}
			if data, err := os.ReadFile(full); err == nil {
				cp := append([]byte(nil), data...)
				e.Backups[full] = &cp
			} else {
				e.Backups[full] = nil // didn't exist: undo deletes it
			}
		}
	case "git_worktree_add":
		// Undo = remove the added worktree. git itself refuses when the
		// worktree is dirty, so this cannot discard in-flight work.
		if p, ok := args["path"].(string); ok && p != "" {
			e.Target = p
			e.Worktree = p
		}
	case "shell_command":
		if c, ok := args["command"].(string); ok {
			e.Target = c
		}
		e.Stash = stashCreate(u.Root)
		if e.Stash == "" {
			e.Dirty = worktreeDirty(u.Root)
		}
	}
	u.stack = append(u.stack, e)
	if len(u.stack) > u.maxEntries() {
		u.stack = u.stack[len(u.stack)-u.maxEntries():]
	}
	return true
}

// DiscardLast drops the most recent entry (a call that failed to execute).
func (u *UndoManager) DiscardLast() {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.stack) > 0 {
		u.stack = u.stack[:len(u.stack)-1]
	}
}

// FinalizeLast records the post-step tracked status for shell entries so
// revert can refuse when the tree moved on since (see After).
func (u *UndoManager) FinalizeLast() {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.stack) == 0 {
		return
	}
	last := &u.stack[len(u.stack)-1]
	if last.Tool == "shell_command" {
		last.After = diffHash(u.Root)
	}
}

// diffHash fingerprints the tracked worktree content (`git diff HEAD`,
// sha256). "" on any error — callers treat unknown as "no basis", and the
// non-repo/Dirty paths speak for themselves.
func diffHash(root string) string {
	cmd := exec.Command("git", "-C", root, "diff", "HEAD", "--")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	sum := sha256.Sum256(out.Bytes())
	return hex.EncodeToString(sum[:])
}

// Depth reports how many steps can be undone.
func (u *UndoManager) Depth() int {
	if u == nil {
		return 0
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.stack)
}

// Undo reverts the last n steps, oldest-report-first. Mark is called for
// every restored path so read-state (stale detection) follows the restore.
// Only successful entries pop: failed ones are retained in LIFO order and
// reported for retry, so one bad revert never discards the rest.
func (u *UndoManager) Undo(n int, mark func(path string)) (string, error) {
	if u == nil {
		return "", fmt.Errorf("undo is unavailable in this session")
	}
	if n < 1 {
		return "", fmt.Errorf("send /undo [n] with n >= 1, e.g. /undo 2")
	}
	u.mu.Lock()
	if len(u.stack) == 0 {
		u.mu.Unlock()
		return "", fmt.Errorf("nothing to undo — no mutating steps recorded this session")
	}
	if n > len(u.stack) {
		n = len(u.stack)
	}
	entries := append([]UndoEntry(nil), u.stack[len(u.stack)-n:]...)
	u.mu.Unlock()

	ok := make([]bool, len(entries))
	var lines []string
	lines = append(lines, fmt.Sprintf("↩ undone %d step(s), newest first:", len(entries)))
	for i := len(entries) - 1; i >= 0; i-- {
		var ls []string
		ls, ok[i] = u.revert(entries[i], mark)
		lines = append(lines, ls...)
	}
	failed := 0
	for _, good := range ok {
		if !good {
			failed++
		}
	}
	if failed > 0 {
		// Retain failures for retry, preserving LIFO order; pop only
		// what actually reverted.
		u.mu.Lock()
		kept := append([]UndoEntry(nil), u.stack[:len(u.stack)-n]...)
		for i, e := range entries {
			if !ok[i] {
				kept = append(kept, e)
			}
		}
		u.stack = kept
		u.mu.Unlock()
		lines = append(lines, fmt.Sprintf("%d %s failed — retained for retry (fix the cause, then re-run /undo).", failed, pluralize(failed, "entry", "entries")))
	} else {
		u.mu.Lock()
		u.stack = u.stack[:len(u.stack)-n]
		u.mu.Unlock()
	}
	return strings.Join(lines, "\n"), nil
}

func (u *UndoManager) revert(e UndoEntry, mark func(string)) ([]string, bool) {
	var lines []string
	ok := true
	head := fmt.Sprintf("  #%d %s %s", e.Seq, e.Tool, truncStr(e.Target, 60))
	for full, content := range e.Backups {
		// Re-contain at restore time: a symlink planted after the
		// snapshot must not redirect the restore outside the project.
		if _, err := contain(u.Root, full); err != nil {
			lines = append(lines, head+fmt.Sprintf(" → refusing to restore %s: %v", full, err))
			ok = false
			continue
		}
		rel, _ := filepath.Rel(u.Root, full)
		if content == nil {
			if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
				lines = append(lines, head+fmt.Sprintf(" → could not remove created file %s: %v", rel, err))
				ok = false
			} else {
				lines = append(lines, head+fmt.Sprintf(" → removed created file %s", rel))
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			lines = append(lines, head+fmt.Sprintf(" → cannot restore %s: %v", rel, err))
			ok = false
			continue
		}
		if err := os.WriteFile(full, *content, 0o644); err != nil {
			lines = append(lines, head+fmt.Sprintf(" → cannot restore %s: %v", rel, err))
			ok = false
			continue
		}
		if mark != nil {
			mark(full)
		}
		lines = append(lines, head+fmt.Sprintf(" → restored %s (%d bytes)", rel, len(*content)))
	}
	if e.Tool == "shell_command" {
		sl, sok := u.revertShell(e, mark)
		for _, ln := range sl {
			lines = append(lines, head+ln)
		}
		if !sok {
			ok = false
		}
	}
	if e.Worktree != "" {
		if out, err := gitOut(u.Root, "worktree", "remove", "--", e.Worktree); err != nil {
			lines = append(lines, head+fmt.Sprintf(" → could not remove worktree %s: %v — output: %s", e.Worktree, err, truncStr(out, 160)))
			ok = false
		} else {
			lines = append(lines, head+fmt.Sprintf(" → removed worktree %s", e.Worktree))
		}
	}
	if len(e.Backups) == 0 && e.Tool != "shell_command" && e.Worktree == "" {
		lines = append(lines, head+" → nothing recorded (no file state captured).")
	}
	return lines, ok
}

// revertShell restores tracked files to the pre-step snapshot, marking
// every restored path so read-state follows the restore (no false
// "stale read" on the next edit of exactly what was just restored).
// ok=false means the entry must be retained for retry.
func (u *UndoManager) revertShell(e UndoEntry, mark func(string)) ([]string, bool) {
	markNames := func(names []string) {
		if mark == nil {
			return
		}
		for _, n := range names {
			if n = strings.TrimSpace(n); n != "" {
				mark(filepath.Join(u.Root, n))
			}
		}
	}
	if e.Stash != "" {
		if e.After != "" {
			if cur := diffHash(u.Root); cur != "" && cur != e.After {
				return []string{" → tracked tree changed since this step ran — refusing to restore over later work. /undo the later steps first, or commit/stash, then retry."}, false
			}
		}
		names, _ := gitNames(u.Root, "show", e.Stash, "--name-only", "--pretty=format:")
		if out, err := gitOut(u.Root, "checkout", e.Stash, "--", "."); err != nil {
			return []string{fmt.Sprintf(" → tracked restore failed: %v — output: %s. Check git status and restore by hand.", err, truncStr(out, 200))}, false
		}
		markNames(names)
		return []string{" → tracked files restored from snapshot; untracked shell effects (created/deleted files) are NOT covered — review with git status."}, true
	}
	// Empty stash means the tracked tree was clean at snapshot time:
	// restoring "clean" is checkout of HEAD. If the root isn't a repo at
	// all, say so instead of failing obscurely. If the tree was dirty but
	// the snapshot failed, refuse — restoring HEAD would wipe uncommitted
	// work, and guessing is the one unforgivable undo behavior.
	if !isGitRepo(u.Root) {
		return []string{" → no snapshot (not a git repo) — nothing to restore. Review the shell output above."}, true
	}
	if e.Dirty {
		return []string{" → no snapshot was captured for this step (git could not snapshot the dirty tree) — refusing to guess. Restore by hand from the shell output above."}, false
	}
	if e.After != "" {
		if cur := diffHash(u.Root); cur != "" && cur != e.After {
			return []string{" → tracked tree changed since this step ran — refusing to restore over later work. /undo the later steps first, or commit/stash, then retry."}, false
		}
	}
	names, _ := gitNames(u.Root, "diff", "--name-only", "HEAD", "--")
	if out, err := gitOut(u.Root, "checkout", "HEAD", "--", "."); err != nil {
		return []string{fmt.Sprintf(" → restore failed: %v — output: %s.", err, truncStr(out, 200))}, false
	}
	markNames(names)
	return []string{" → tracked files restored to HEAD (tree was clean before this step); untracked shell effects NOT covered — review with git status."}, true
}

// gitNames runs git and splits stdout into lines.
func gitNames(root string, args ...string) ([]string, error) {
	out, err := gitOut(root, args...)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, ln := range strings.Split(out, "\n") {
		if strings.TrimSpace(ln) != "" {
			names = append(names, ln)
		}
	}
	return names, nil
}

// worktreeDirty reports whether tracked files differ from HEAD.
// Errors fail toward true (refuse to restore) — never toward a wipe.
func worktreeDirty(root string) bool {
	for _, args := range [][]string{{"diff", "--quiet"}, {"diff", "--cached", "--quiet"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if err := cmd.Run(); err != nil {
			return true
		}
	}
	return false
}

// stashCreate snapshots the tracked tree as a dangling commit (no refs
// touched, nothing pushed). Returns "" when the tree is clean or git
// fails — callers must consult Dirty to tell those cases apart.
// Transient lock contention (a status refresh colliding mid-write) gets
// brief retries; genuine failures still return "".
func stashCreate(root string) string {
	var out bytes.Buffer
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(100 * time.Millisecond)
		}
		out.Reset()
		cmd := exec.Command("git", "-C", root, "stash", "create")
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			continue
		}
		if hash := strings.TrimSpace(out.String()); hash != "" {
			return hash
		}
		return ""
	}
	return ""
}

func isGitRepo(root string) bool {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--git-dir")
	return cmd.Run() == nil
}

func gitOut(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return strings.TrimSpace(stdout.String() + stderr.String()), err
}

func truncStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func pluralize(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
