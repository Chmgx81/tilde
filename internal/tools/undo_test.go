package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testUndoSetup(t *testing.T) (string, *SeenMap, *UndoManager, *Registry) {
	t.Helper()
	root := t.TempDir()
	seen := NewSeenMap(root)
	undo := &UndoManager{Root: root}
	reg := NewRegistry()
	reg.Register(&ReadFile{Root: root, Seen: seen})
	reg.Register(&WriteFile{Root: root, Seen: seen})
	reg.Register(&EditFile{Root: root, Seen: seen})
	reg.Register(&Shell{Root: root, Tasks: &TaskManager{LogDir: t.TempDir()}})
	reg.Undo = undo
	return root, seen, undo, reg
}

func dispatch(t *testing.T, reg *Registry, name string, args map[string]any) string {
	t.Helper()
	return reg.Dispatch(context.Background(), name, args)
}

func TestUndoWriteRestoresBytes(t *testing.T) {
	root, seen, undo, reg := testUndoSetup(t)
	os.WriteFile(filepath.Join(root, "w.txt"), []byte("original\n"), 0o644)
	dispatch(t, reg, "read_file", map[string]any{"path": "w.txt"})
	dispatch(t, reg, "write_file", map[string]any{"path": "w.txt", "content": "clobbered"})
	if undo.Depth() != 1 {
		t.Fatalf("depth=%d, want 1", undo.Depth())
	}
	report, err := undo.Undo(1, seen.Mark)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(report, "restored w.txt") {
		t.Fatalf("report: %q", report)
	}
	data, _ := os.ReadFile(filepath.Join(root, "w.txt"))
	if string(data) != "original\n" {
		t.Fatalf("content=%q", data)
	}
}

func TestUndoCreateDeletesFile(t *testing.T) {
	root, _, undo, reg := testUndoSetup(t)
	dispatch(t, reg, "write_file", map[string]any{"path": "new.txt", "content": "hi"})
	if _, err := os.Stat(filepath.Join(root, "new.txt")); err != nil {
		t.Fatal("file should exist before undo")
	}
	report, err := undo.Undo(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(report, "removed created file new.txt") {
		t.Fatalf("report: %q", report)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("created file must be gone after undo")
	}
}

func TestUndoLIFOOrder(t *testing.T) {
	root, seen, undo, reg := testUndoSetup(t)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a1"), 0o644)
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("b1"), 0o644)
	dispatch(t, reg, "read_file", map[string]any{"path": "a.txt"})
	dispatch(t, reg, "read_file", map[string]any{"path": "b.txt"})
	dispatch(t, reg, "edit_file", map[string]any{"path": "a.txt", "old_string": "a1", "new_string": "a2"})
	dispatch(t, reg, "edit_file", map[string]any{"path": "b.txt", "old_string": "b1", "new_string": "b2"})
	report, err := undo.Undo(2, seen.Mark)
	if err != nil {
		t.Fatal(err)
	}
	// Newest first: b before a.
	ib, ia := indexOf(report, "b.txt"), indexOf(report, "a.txt")
	if ib < 0 || ia < 0 || ib > ia {
		t.Fatalf("LIFO order wrong:\n%s", report)
	}
	for _, tc := range []struct{ f, want string }{{"a.txt", "a1"}, {"b.txt", "b1"}} {
		if data, _ := os.ReadFile(filepath.Join(root, tc.f)); string(data) != tc.want {
			t.Fatalf("%s=%q, want %q", tc.f, data, tc.want)
		}
	}
	if undo.Depth() != 0 {
		t.Fatalf("depth=%d, want 0", undo.Depth())
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestUndoEmptyAndBadCount(t *testing.T) {
	_, _, undo, _ := testUndoSetup(t)
	if _, err := undo.Undo(1, nil); err == nil || !contains(err.Error(), "nothing to undo") {
		t.Fatalf("got %v", err)
	}
	if _, err := undo.Undo(0, nil); err == nil || !contains(err.Error(), "n >= 1") {
		t.Fatalf("got %v", err)
	}
}

func TestUndoClampsToDepth(t *testing.T) {
	_, seen, undo, reg := testUndoSetup(t)
	dispatch(t, reg, "write_file", map[string]any{"path": "n.txt", "content": "x"})
	report, err := undo.Undo(99, seen.Mark)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(report, "undone 1 step") {
		t.Fatalf("should clamp with note: %q", report)
	}
}

func TestFailedCallLeavesNoEntry(t *testing.T) {
	_, _, undo, reg := testUndoSetup(t)
	out := dispatch(t, reg, "write_file", map[string]any{"path": "no/such/dir/x.txt", "content": ""})
	_ = out
	// Empty content is rejected by arg validation → Exec fails → discarded.
	if undo.Depth() != 0 {
		t.Fatalf("failed call polluted the stack (depth=%d): %q", undo.Depth(), out)
	}
	// Read-only tools never snapshot.
	dispatch(t, reg, "glob", map[string]any{"pattern": "*.go"})
	if undo.Depth() != 0 {
		t.Fatal("read-only call must not snapshot")
	}
}

func TestUndoShellRestoresTracked(t *testing.T) {
	root, seen, undo, reg := testUndoSetup(t)
	ctx := context.Background()
	mustGit(t, ctx, root, "init", "-b", "main")
	mustGit(t, ctx, root, "config", "user.email", "t@t.t")
	mustGit(t, ctx, root, "config", "user.name", "t")
	os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("v1\n"), 0o644)
	mustGit(t, ctx, root, "add", ".")
	mustGit(t, ctx, root, "commit", "-qm", "init")
	// Dirty the tree first so the stash path (not HEAD-fallback) is tested.
	os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("v2-dirty\n"), 0o644)
	out := dispatch(t, reg, "shell_command", map[string]any{"command": "echo v3-shell > tracked.txt && cat tracked.txt"})
	if !contains(out, "v3-shell") {
		t.Fatalf("shell did not run: %q", out)
	}
	report, err := undo.Undo(1, seen.Mark)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(report, "tracked files restored from snapshot") {
		t.Fatalf("report: %q", report)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "tracked.txt")); string(data) != "v2-dirty\n" {
		t.Fatalf("tracked=%q, want v2-dirty", data)
	}
}

func TestUndoShellCleanTreeUsesHEAD(t *testing.T) {
	root, _, undo, reg := testUndoSetup(t)
	ctx := context.Background()
	mustGit(t, ctx, root, "init", "-b", "main")
	mustGit(t, ctx, root, "config", "user.email", "t@t.t")
	mustGit(t, ctx, root, "config", "user.name", "t")
	os.WriteFile(filepath.Join(root, "t.txt"), []byte("clean\n"), 0o644)
	mustGit(t, ctx, root, "add", ".")
	mustGit(t, ctx, root, "commit", "-qm", "init")
	dispatch(t, reg, "shell_command", map[string]any{"command": "echo dirty > t.txt"})
	report, err := undo.Undo(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(report, "restored to HEAD") {
		t.Fatalf("report: %q", report)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "t.txt")); string(data) != "clean\n" {
		t.Fatalf("t.txt=%q", data)
	}
}

func TestUndoShellNonRepoIsHonest(t *testing.T) {
	_, _, undo, reg := testUndoSetup(t) // plain temp dir, no git
	dispatch(t, reg, "shell_command", map[string]any{"command": "echo hi"})
	report, err := undo.Undo(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(report, "not a git repo") {
		t.Fatalf("must say what is not covered: %q", report)
	}
}

func TestUndoShellRefusesMovedTree(t *testing.T) {
	root, _, undo, reg := testUndoSetup(t)
	ctx := context.Background()
	mustGit(t, ctx, root, "init", "-b", "main")
	mustGit(t, ctx, root, "config", "user.email", "t@t.t")
	mustGit(t, ctx, root, "config", "user.name", "t")
	os.WriteFile(filepath.Join(root, "t.txt"), []byte("v1\n"), 0o644)
	mustGit(t, ctx, root, "add", ".")
	mustGit(t, ctx, root, "commit", "-qm", "init")
	dispatch(t, reg, "shell_command", map[string]any{"command": "echo v2 > t.txt"})
	// Outside hands touch a tracked file after the step completed.
	os.WriteFile(filepath.Join(root, "t.txt"), []byte("v3-outside\n"), 0o644)
	report, err := undo.Undo(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(report, "changed since this step") {
		t.Fatalf("must refuse over later work, got: %q", report)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "t.txt")); string(data) != "v3-outside\n" {
		t.Fatalf("refused restore must leave the tree alone: %q", data)
	}
}

func TestUndoShellRefusesDirtyUnsnapshotted(t *testing.T) {
	// Fabricated worst case: dirty tree, no stash hash (snapshot failed).
	// Undo must refuse, never restore HEAD over uncommitted work.
	root := t.TempDir()
	mustGit(t, context.Background(), root, "init", "-q")
	undo := &UndoManager{Root: root}
	undo.stack = []UndoEntry{{Seq: 1, Tool: "shell_command", Target: "rm -rf /", Dirty: true, Backups: map[string]*[]byte{}}}
	report, err := undo.Undo(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(report, "refusing to guess") {
		t.Fatalf("must refuse, got: %q", report)
	}
}

// FIX 13 (undo pops early): failed entries are retained for retry in LIFO
// order; only successful reverts pop.
func TestUndoFailedRetainsForRetry(t *testing.T) {
	root := t.TempDir()
	mustGit(t, context.Background(), root, "init", "-q")
	undo := &UndoManager{Root: root}
	undo.stack = []UndoEntry{{Seq: 1, Tool: "shell_command", Target: "rm -rf /", Dirty: true, Backups: map[string]*[]byte{}}}
	report, err := undo.Undo(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(report, "retained for retry") {
		t.Fatalf("must report retention, got: %q", report)
	}
	if undo.Depth() != 1 {
		t.Fatalf("failed entry must be retained (depth=%d, want 1)", undo.Depth())
	}
	// A later successful entry pops while the failed one stays, in order.
	os.WriteFile(filepath.Join(root, "k.txt"), []byte("v1\n"), 0o644)
	seen := NewSeenMap(root)
	seen.Mark(filepath.Join(root, "k.txt"))
	cp := []byte("v1\n")
	undo.mu.Lock()
	undo.seq = 2
	undo.stack = append(undo.stack, UndoEntry{Seq: 2, Tool: "edit_file", Target: "k.txt", Backups: map[string]*[]byte{filepath.Join(root, "k.txt"): &cp}})
	undo.mu.Unlock()
	os.WriteFile(filepath.Join(root, "k.txt"), []byte("v2\n"), 0o644)
	report2, err := undo.Undo(2, seen.Mark)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(report2, "restored k.txt") {
		t.Fatalf("success must still revert, got: %q", report2)
	}
	if undo.Depth() != 1 {
		t.Fatalf("only the failure stays (depth=%d, want 1)", undo.Depth())
	}
	undo.mu.Lock()
	kept := undo.stack[0]
	undo.mu.Unlock()
	if kept.Seq != 1 {
		t.Fatalf("retained entry wrong: %+v", kept)
	}
}

// --- Phase 5: stale reads ---

func TestStaleEditRefusesThenRecovers(t *testing.T) {
	root, seen, _, reg := testUndoSetup(t)
	p := filepath.Join(root, "s.txt")
	os.WriteFile(p, []byte("v1\n"), 0o644)
	dispatch(t, reg, "read_file", map[string]any{"path": "s.txt"})
	// Something else changes the file behind the agent's back.
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(p, []byte("v2-external-longer\n"), 0o644)
	out := dispatch(t, reg, "edit_file", map[string]any{"path": "s.txt", "old_string": "v1", "new_string": "v3"})
	if !contains(out, "stale read") || !contains(out, "re-read") {
		t.Fatalf("expected stale refusal, got %q", out)
	}
	// Re-read refreshes state → edit proceeds.
	dispatch(t, reg, "read_file", map[string]any{"path": "s.txt"})
	out2 := dispatch(t, reg, "edit_file", map[string]any{"path": "s.txt", "old_string": "v2-external-longer", "new_string": "v3"})
	if contains(out2, "stale read") {
		t.Fatalf("fresh read must clear staleness: %q", out2)
	}
	_ = seen
}

func TestOwnWritesNeverStale(t *testing.T) {
	_, _, _, reg := testUndoSetup(t)
	dispatch(t, reg, "write_file", map[string]any{"path": "o.txt", "content": "one"})
	out := dispatch(t, reg, "edit_file", map[string]any{"path": "o.txt", "old_string": "one", "new_string": "two"})
	if contains(out, "stale read") || contains(out, "have not read") {
		t.Fatalf("own verified writes must not read as stale: %q", out)
	}
	out2 := dispatch(t, reg, "edit_file", map[string]any{"path": "o.txt", "old_string": "two", "new_string": "three"})
	if contains(out2, "stale read") {
		t.Fatalf("chained edits must not read as stale: %q", out2)
	}
}

func TestShellWriteMakesEditStale(t *testing.T) {
	_, _, _, reg := testUndoSetup(t)
	os.WriteFile(filepath.Join(testRootOf(reg), "h.txt"), []byte("a\n"), 0o644)
	dispatch(t, reg, "read_file", map[string]any{"path": "h.txt"})
	time.Sleep(10 * time.Millisecond)
	dispatch(t, reg, "shell_command", map[string]any{"command": "echo b > h.txt"})
	out := dispatch(t, reg, "edit_file", map[string]any{"path": "h.txt", "old_string": "a", "new_string": "c"})
	if !contains(out, "stale read") {
		t.Fatalf("shell side-effect must stale the read: %q", out)
	}
}

func testRootOf(reg *Registry) string {
	t, ok := reg.Get("read_file")
	if !ok {
		panic("no read_file")
	}
	return t.(*ReadFile).Root
}

func TestUndoRestoreRefreshesSeen(t *testing.T) {
	root, seen, undo, reg := testUndoSetup(t)
	os.WriteFile(filepath.Join(root, "u.txt"), []byte("v1\n"), 0o644)
	dispatch(t, reg, "read_file", map[string]any{"path": "u.txt"})
	dispatch(t, reg, "edit_file", map[string]any{"path": "u.txt", "old_string": "v1", "new_string": "v2"})
	if _, err := undo.Undo(1, seen.Mark); err != nil {
		t.Fatal(err)
	}
	// Restored v1 with refreshed state: editing original text is allowed.
	out := dispatch(t, reg, "edit_file", map[string]any{"path": "u.txt", "old_string": "v1", "new_string": "v3"})
	if contains(out, "stale read") {
		t.Fatalf("restored state must be marked seen: %q", out)
	}
}

func TestEditFuzzyIndentationLands(t *testing.T) {
	root := testRoot(t)
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("func x() {\n        return 1\n}\n"), 0o644)
	seen := NewSeenMap(root)
	seen.Mark(filepath.Join(root, "f.txt"))
	e := &EditFile{Root: root, Seen: seen}
	// Wrong indentation (spaces vs the file's) must still land, loudly.
	out, err := e.Exec(context.Background(), map[string]any{
		"path": "f.txt", "old_string": "func x() {\n  return 1\n}", "new_string": "func x() {\n  return 2\n}"})
	if err != nil {
		t.Fatalf("fuzzy edit refused: %v", err)
	}
	if !contains(out, "ignoring indentation") {
		t.Fatalf("must disclose the fuzzy match: %q", out)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "f.txt")); string(data) != "func x() {\n  return 2\n}\n" {
		t.Fatalf("got %q", data)
	}
}

func TestEditReadPrefixesStripped(t *testing.T) {
	root := testRoot(t)
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("holds MARK token\n"), 0o644)
	seen := NewSeenMap(root)
	seen.Mark(filepath.Join(root, "f.txt"))
	e := &EditFile{Root: root, Seen: seen}
	// Echoed fenced-read prefix (`1: `) must land on the unique span,
	// loudly — this was the chain-read-first 0/5 failure cluster.
	out, err := e.Exec(context.Background(), map[string]any{
		"path": "f.txt", "old_string": "1: holds MARK token", "new_string": "holds SEALED token"})
	if err != nil {
		t.Fatalf("prefixed edit refused: %v", err)
	}
	if !contains(out, "line numbers") {
		t.Fatalf("must disclose the prefix strip: %q", out)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "f.txt")); string(data) != "holds SEALED token\n" {
		t.Fatalf("got %q", data)
	}
}

func TestEditRealPrefixNeverStripped(t *testing.T) {
	root := testRoot(t)
	// File content itself starts with `1: ` — raw matches, so no rewrite.
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("1: holds MARK token\n"), 0o644)
	seen := NewSeenMap(root)
	seen.Mark(filepath.Join(root, "f.txt"))
	e := &EditFile{Root: root, Seen: seen}
	out, err := e.Exec(context.Background(), map[string]any{
		"path": "f.txt", "old_string": "1: holds MARK token", "new_string": "x"})
	if err != nil {
		t.Fatalf("exact prefixed content must edit directly: %v", err)
	}
	if contains(out, "line numbers") {
		t.Fatalf("must not claim a strip on exact match: %q", out)
	}
}

func TestEditFuzzyAmbiguousRefuses(t *testing.T) {
	root := testRoot(t)
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("if a {\n  x\n}\nif b {\n  x\n}\n"), 0o644)
	seen := NewSeenMap(root)
	seen.Mark(filepath.Join(root, "f.txt"))
	e := &EditFile{Root: root, Seen: seen}
	// "x" alone is exact-twice → classic ambiguity refusal, not fuzzy.
	if _, err := e.Exec(context.Background(), map[string]any{
		"path": "f.txt", "old_string": "x", "new_string": "y"}); err == nil {
		t.Fatal("expected ambiguity refusal")
	}
}

func TestInflightBackgroundStalesReads(t *testing.T) {
	root := testRoot(t)
	seen := NewSeenMap(root)
	mgr := &TaskManager{LogDir: t.TempDir()}
	seen.Tasks = mgr
	p := filepath.Join(root, "f.txt")
	os.WriteFile(p, []byte("v1\n"), 0o644)
	seen.Mark("f.txt")
	s := &Shell{Root: root, BgMax: time.Minute, Tasks: mgr}
	out, err := s.Exec(context.Background(), map[string]any{"command": "sleep 30", "background": true})
	if err != nil || !contains(out, "task_1") {
		t.Fatalf("bg start: %q %v", out, err)
	}
	e := &EditFile{Root: root, Seen: seen}
	_, err = e.Exec(context.Background(), map[string]any{"path": "f.txt", "old_string": "v1", "new_string": "v2"})
	if err == nil || !contains(err.Error(), "background task_1") {
		t.Fatalf("in-flight task must stale the read: %v", err)
	}
	for _, id := range mgr.KillAll() {
		_ = id
	}
	// Settled: the same edit proceeds.
	if _, err := e.Exec(context.Background(), map[string]any{"path": "f.txt", "old_string": "v1", "new_string": "v2"}); err != nil {
		t.Fatalf("settled task must allow: %v", err)
	}
}

func TestWorktreeAddUndoRemoves(t *testing.T) {
	root := testRoot(t)
	ctx := context.Background()
	mustGit(t, ctx, root, "init", "-b", "main")
	mustGit(t, ctx, root, "config", "user.email", "t@t.t")
	mustGit(t, ctx, root, "config", "user.name", "t")
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o644)
	mustGit(t, ctx, root, "add", ".")
	mustGit(t, ctx, root, "commit", "-qm", "init")
	led := NewSeenMap(root)
	undo := &UndoManager{Root: root}
	reg := NewRegistry()
	reg.Register(&GitWorktreeAdd{Root: root})
	reg.Register(&GitWorktreeRemove{Root: root})
	reg.Undo = undo
	_ = led
	wtPath := filepath.Join(root, "wt-undo")
	reg.Dispatch(ctx, "git_worktree_add", map[string]any{"path": wtPath})
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree not created: %v", err)
	}
	report, err := undo.Undo(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(report, "removed worktree") {
		t.Fatalf("report: %q", report)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatal("worktree dir must be gone")
	}
}

func TestWorktreeDashRejected(t *testing.T) {
	root := testRoot(t)
	a := &GitWorktreeAdd{Root: root}
	if _, err := a.Exec(context.Background(), map[string]any{"path": "--force"}); err == nil {
		t.Fatal("dash-led path must be rejected")
	}
	r := &GitWorktreeRemove{Root: root}
	if _, err := r.Exec(context.Background(), map[string]any{"path": "-x"}); err == nil {
		t.Fatal("dash-led path must be rejected")
	}
}

func TestSnapshotContainFailurePushesNothing(t *testing.T) {
	root := testRoot(t)
	undo := &UndoManager{Root: root}
	reg := NewRegistry()
	reg.Register(&WriteFile{Root: root})
	reg.Register(&ReadFile{Root: root})
	reg.Undo = undo
	reg.Dispatch(context.Background(), "write_file", map[string]any{"path": "ok.txt", "content": "hi"})
	if undo.Depth() != 1 {
		t.Fatalf("depth=%d", undo.Depth())
	}
	// Escaping path: snapshot records nothing AND must not pop ok.txt.
	out := reg.Dispatch(context.Background(), "write_file", map[string]any{"path": "../../nope.txt", "content": "x"})
	if !contains(out, "outside the project root") {
		t.Fatalf("got %q", out)
	}
	if undo.Depth() != 1 {
		t.Fatalf("failed snapshot popped an unrelated entry (depth=%d)", undo.Depth())
	}
}

func TestShellUndoRefreshesSeen(t *testing.T) {
	root, seen, undo, reg := testUndoSetup(t)
	ctx := context.Background()
	mustGit(t, ctx, root, "init", "-b", "main")
	mustGit(t, ctx, root, "config", "user.email", "t@t.t")
	mustGit(t, ctx, root, "config", "user.name", "t")
	os.WriteFile(filepath.Join(root, "s.txt"), []byte("v1\n"), 0o644)
	mustGit(t, ctx, root, "add", ".")
	mustGit(t, ctx, root, "commit", "-qm", "init")
	dispatch(t, reg, "read_file", map[string]any{"path": "s.txt"})
	dispatch(t, reg, "shell_command", map[string]any{"command": "echo v2 > s.txt && cat s.txt"})
	if _, err := undo.Undo(1, seen.Mark); err != nil {
		t.Fatal(err)
	}
	// Restored v1 with refreshed state: editing original text is allowed.
	out := dispatch(t, reg, "edit_file", map[string]any{"path": "s.txt", "old_string": "v1", "new_string": "v3"})
	if contains(out, "stale read") {
		t.Fatalf("restored state must be marked seen: %q", out)
	}
}
