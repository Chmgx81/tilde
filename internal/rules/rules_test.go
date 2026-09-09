package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPriorityFirstWins(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "AGENTS.md", "# agents rules\nNever push on Fridays.")
	writeFile(t, root, "CLAUDE.md", "# claude rules")
	writeFile(t, root, ".tilde/RULES.md", "# tilde rules")
	r, ok := Load(root)
	if !ok {
		t.Fatal("expected rules found")
	}
	if want := filepath.Join(root, "AGENTS.md"); r.Path != want {
		t.Fatalf("first candidate must win: got %q want %q", r.Path, want)
	}
	if !strings.Contains(r.Body, "Never push") {
		t.Fatalf("body must be file content: %q", r.Body)
	}
	if r.Truncated {
		t.Fatal("small file must not truncate")
	}
}

func TestLoadFallsThrough(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "CLAUDE.md", "# claude rules")
	r, ok := Load(root)
	if !ok || r.Path != filepath.Join(root, "CLAUDE.md") {
		t.Fatalf("missing AGENTS.md must fall through to CLAUDE.md: %+v %v", r, ok)
	}

	root2 := t.TempDir()
	writeFile(t, root2, ".tilde/RULES.md", "# tilde rules")
	r, ok = Load(root2)
	if !ok || r.Path != filepath.Join(root2, ".tilde", "RULES.md") {
		t.Fatalf("must reach third candidate: %+v %v", r, ok)
	}
}

func TestLoadMissingAll(t *testing.T) {
	if _, ok := Load(t.TempDir()); ok {
		t.Fatal("empty dir must report not found, not error")
	}
	if _, ok := Load(""); ok {
		t.Fatal("empty root must report not found")
	}
}

func TestLoadEmptyIgnored(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "AGENTS.md", "")
	writeFile(t, root, "CLAUDE.md", "   \n\t\n")
	writeFile(t, root, ".tilde/RULES.md", "# real rules")
	r, ok := Load(root)
	if !ok {
		t.Fatal("empty files must be skipped, non-empty third must win")
	}
	if want := filepath.Join(root, ".tilde", "RULES.md"); r.Path != want {
		t.Fatalf("got %q want %q", r.Path, want)
	}

	all := t.TempDir()
	writeFile(t, all, "AGENTS.md", "\n")
	if _, ok := Load(all); ok {
		t.Fatal("only-empty candidates must report not found")
	}
}

func TestLoadTruncationMarkerAndSize(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("x", MaxBytes+4000) + "\nNever do X.\n"
	writeFile(t, root, "AGENTS.md", big)
	r, ok := Load(root)
	if !ok {
		t.Fatal("expected rules found")
	}
	if !r.Truncated {
		t.Fatal("over-cap file must set Truncated")
	}
	if !strings.Contains(r.Body, "[... truncated at 8KB — move detail to skills]") {
		t.Fatalf("body must carry the truncation marker naming the fix: tail %q", r.Body[len(r.Body)-100:])
	}
	if want := MaxBytes + len(TruncNote); len(r.Body) > want {
		t.Fatalf("capped body len=%d exceeds cap+marker=%d", len(r.Body), want)
	}
	// Small file stays byte-identical: no silent rewrite.
	root2 := t.TempDir()
	writeFile(t, root2, "CLAUDE.md", "small\n")
	r2, ok := Load(root2)
	if !ok || r2.Body != "small\n" || r2.Truncated {
		t.Fatalf("small file must pass through untouched: %+v %v", r2, ok)
	}
}

func TestLoadNoPathEscape(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "CLAUDE.md", "rules")
	r, ok := Load(root)
	if !ok {
		t.Fatal("expected rules found")
	}
	rel, err := filepath.Rel(root, r.Path)
	if err != nil {
		t.Fatal(err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("returned path escapes root: %q", r.Path)
	}
	if !filepath.IsAbs(r.Path) {
		t.Fatalf("returned path must be absolute/root-joined: %q", r.Path)
	}
}

func TestPromptBlockFenced(t *testing.T) {
	r := Rules{Path: "/repo/AGENTS.md", Body: "# Rules\n```go\ncode\n```\nNever do X."}
	block := r.PromptBlock()
	if !strings.Contains(block, "Project rules (/repo/AGENTS.md)") {
		t.Fatalf("block must carry provenance header: %q", block)
	}
	// Body held triple backticks: fence must have lengthened so exactly
	// the two outer fences use the long run and content is intact.
	if !strings.HasPrefix(block[strings.Index(block, "\n")+1:], "````markdown") {
		t.Fatalf("fence must lengthen past embedded backticks: %q", block)
	}
	if got := strings.Count(block, "````"); got != 2 {
		t.Fatalf("want exactly 2 lengthened fences, got %d: %q", got, block)
	}
	if !strings.Contains(block, "Never do X.") {
		t.Fatalf("content must survive fencing: %q", block)
	}

	plain := Rules{Path: "p", Body: "Never do X."}
	b := plain.PromptBlock()
	if !strings.Contains(b, "```markdown\nNever do X.\n```") {
		t.Fatalf("plain body gets plain ```markdown fence: %q", b)
	}
}
