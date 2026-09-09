package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// --- git_status (read-only) ---

type GitStatus struct{ Root string }

func (t *GitStatus) Name() string { return "git_status" }
func (t *GitStatus) Description() string {
	return "Show git status (short) plus current branch."
}
func (t *GitStatus) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (t *GitStatus) Exec(ctx context.Context, _ map[string]any) (string, error) {
	out, err := runGit(ctx, t.Root, "status", "--short", "--branch")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "clean tree, nothing to commit.", nil
	}
	// Branch names and filenames are attacker-influenced: fence them.
	return Fence(out), nil
}

// --- git_diff (read-only) ---

type GitDiff struct{ Root string }

func (t *GitDiff) Name() string { return "git_diff" }
func (t *GitDiff) Description() string {
	return "Show unstaged/staged diff (truncated). Read-only."
}
func (t *GitDiff) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (t *GitDiff) Exec(ctx context.Context, _ map[string]any) (string, error) {
	out, err := runGit(ctx, t.Root, "diff", "--stat")
	if err != nil {
		return "", err
	}
	full, err := runGit(ctx, t.Root, "diff", "--", ".")
	if err != nil {
		return "", err
	}
	combined := strings.TrimSpace(out + "\n" + truncateGit(full, 6000))
	if combined == "" {
		return "no diff — working tree matches HEAD.", nil
	}
	// Diffs embed file content and branch names: fence them.
	return Fence(combined), nil
}

func truncateGit(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n[truncated: showing first %d bytes, %d more not shown — re-run on a narrower path (e.g. git diff -- <file>) to continue]", max, len(s)-max)
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "not a git repository") {
			return "", fmt.Errorf("not a git repository at %q: run `git init` first, or point tilde at a repo dir", dir)
		}
		return "", fmt.Errorf("git %v failed: %v — stderr: %s", args, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// --- git_worktree_list (read-only) ---

// GitWorktreeList shows isolated working trees for parallel agent work.
type GitWorktreeList struct{ Root string }

func (t *GitWorktreeList) Name() string { return "git_worktree_list" }
func (t *GitWorktreeList) Description() string {
	return "List git worktrees (isolated dirs for parallel work). Read-only."
}
func (t *GitWorktreeList) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (t *GitWorktreeList) Exec(ctx context.Context, _ map[string]any) (string, error) {
	out, err := runGit(ctx, t.Root, "worktree", "list")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "no worktrees — only the main checkout exists. Use git_worktree_add to isolate parallel work.", nil
	}
	return Fence(out), nil
}

// --- git_worktree_add (mutating) ---

// GitWorktreeAdd creates an isolated checkout at <path> on <branch>.
type GitWorktreeAdd struct{ Root string }

func (t *GitWorktreeAdd) Name() string { return "git_worktree_add" }
func (t *GitWorktreeAdd) Description() string {
	return "Create an isolated git worktree at path (optionally on a new branch) for parallel work."
}
func (t *GitWorktreeAdd) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string", "description": "New worktree dir (repo-relative or absolute)"},
			"branch": map[string]any{"type": "string", "description": "Branch to check out (created with -b if missing)"},
			"reason": map[string]any{"type": "string", "description": "Optional concise reason shown in the approval prompt; informational only"},
		}, "required": []string{"path"}}
}

func (t *GitWorktreeAdd) Exec(ctx context.Context, args map[string]any) (string, error) {
	p, err := strArg(args, "path")
	if err != nil {
		return "", err
	}
	// The worktree must stay inside the project root: an outside path
	// would move agent work where read/edit/shell containment cannot see
	// it. Flag injection is never legitimate: reject dash-led values,
	// separate paths with --.
	if strings.HasPrefix(p, "-") {
		return "", fmt.Errorf("refusing worktree path %q: must not start with - (flag injection) — send a plain directory path", p)
	}
	if _, err := contain(t.Root, p); err != nil {
		return "", err
	}
	branch := optStr(args, "branch", "")
	if strings.HasPrefix(branch, "-") {
		return "", fmt.Errorf("refusing branch %q: must not start with - (flag injection)", branch)
	}
	argv := []string{"worktree", "add"}
	if branch != "" {
		argv = append(argv, "-B", branch)
	}
	argv = append(argv, "--", p)
	out, err := runGit(ctx, t.Root, argv...)
	if err != nil {
		return "", fmt.Errorf("%v — if the path is already a worktree, list with git_worktree_list and pick a fresh path", err)
	}
	if out == "" {
		out = "worktree ready at " + p
	}
	// Branch names and paths are attacker-influenced: fence the echo.
	return Fence(out) + ". Point subsequent read/edit/shell calls at this dir to work isolated.", nil
}

// --- git_worktree_remove (mutating) ---

// GitWorktreeRemove deletes an isolated checkout. No --force: refuses
// when the worktree is dirty, and says how to proceed instead.
type GitWorktreeRemove struct{ Root string }

func (t *GitWorktreeRemove) Name() string { return "git_worktree_remove" }
func (t *GitWorktreeRemove) Description() string {
	return "Remove a git worktree. Refuses when dirty (commit or stash first)."
}
func (t *GitWorktreeRemove) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string", "description": "Worktree dir to remove"},
			"reason": map[string]any{"type": "string", "description": "Optional concise reason shown in the approval prompt; informational only"},
		}, "required": []string{"path"}}
}

func (t *GitWorktreeRemove) Exec(ctx context.Context, args map[string]any) (string, error) {
	p, err := strArg(args, "path")
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(p, "-") {
		return "", fmt.Errorf("refusing worktree path %q: must not start with - (flag injection) — send a plain directory path", p)
	}
	if _, err := runGit(ctx, t.Root, "worktree", "remove", "--", p); err != nil {
		return "", fmt.Errorf("%v — if dirty, commit/stash inside %q first, then retry", err, p)
	}
	// The echoed path is attacker-influenced: fence it.
	return Fence("removed worktree "+p) + ". (Not covered by /undo — re-add with git_worktree_add if needed.)", nil
}
