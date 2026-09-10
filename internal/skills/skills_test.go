package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const goodSkill = `---
name: review
description: Review a diff
---
# Body here
Do the thing.
`

func TestParseGood(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.md")
	os.WriteFile(p, []byte(goodSkill), 0o644)
	sk, err := ParseFile(p, "project")
	if err != nil {
		t.Fatal(err)
	}
	if sk.Name != "review" || sk.Description != "Review a diff" || sk.Scope != "project" {
		t.Fatalf("%+v", sk)
	}
	if sk.Body == "" || sk.Body == goodSkill {
		t.Fatal("body must be frontmatter-stripped content")
	}
}

func TestParseNameDefaultsToFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "stem.md")
	os.WriteFile(p, []byte("---\ndescription: Default the skill name from its filename\n---\nbody\n"), 0o644)
	sk, err := ParseFile(p, "user")
	if err != nil {
		t.Fatal(err)
	}
	if sk.Name != "stem" {
		t.Fatalf("got %q", sk.Name)
	}
}

func TestParseMissingDescription(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.md")
	os.WriteFile(p, []byte("---\nname: x\n---\nbody\n"), 0o644)
	if _, err := ParseFile(p, "project"); err == nil {
		t.Fatal("expected description error")
	}
}

func TestParseUnclosedFrontmatter(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.md")
	os.WriteFile(p, []byte("---\nname: x\nbody without close"), 0o644)
	if _, err := ParseFile(p, "project"); err == nil {
		t.Fatal("expected unclosed error")
	}
}

func TestParseEmptyBody(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.md")
	os.WriteFile(p, []byte("---\nname: x\ndescription: d\n---\n"), 0o644)
	if _, err := ParseFile(p, "project"); err == nil {
		t.Fatal("expected empty-body error")
	}
}

func TestScanProjectWinsAndBadFileSurvives(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSkill(t, filepath.Join(home, ".tilde", "skills"), "same.md", "---\nname: same\ndescription: User-scoped duplicate for clash test\n---\nuser body\n")
	writeSkill(t, filepath.Join(home, ".tilde", "skills"), "uonly.md", "---\nname: uonly\ndescription: User-only skill for listing tests\n---\nbody\n")
	writeSkill(t, filepath.Join(root, ".tilde", "skills"), "same.md", "---\nname: same\ndescription: Project-scoped duplicate for clash test\n---\nproject body\n")
	writeSkill(t, filepath.Join(root, ".tilde", "skills"), "broken.md", "---\nname: broken\n")
	ix, err := Scan(root)
	if err == nil {
		t.Fatal("expected broken-file error surfaced")
	}
	got, ok := ix.Get("same")
	if !ok || got.Scope != "project" || got.Body != "project body" {
		t.Fatalf("project must win: %+v", got)
	}
	if _, ok := ix.Get("uonly"); !ok {
		t.Fatal("user skill missing")
	}
	if names := ix.Names(); len(names) != 2 {
		t.Fatalf("broken file must not kill index, names=%v", names)
	}
}

func TestScanMissingDirsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ix, err := Scan(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.List()) != 0 {
		t.Fatal("expected empty index")
	}
}

func TestLoadToolHitAndMiss(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	writeSkill(t, filepath.Join(root, ".tilde", "skills"), "r.md", goodSkill)
	ix, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	lt := &LoadTool{Index: ix}
	out, err := lt.Exec(context.Background(), map[string]any{"name": "review"})
	if err != nil || out == "" || !contains(out, "Do the thing.") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := lt.Exec(context.Background(), map[string]any{"name": "nope"}); err == nil {
		t.Fatal("expected unknown-skill error")
	}
	empty := &LoadTool{}
	if _, err := empty.Exec(context.Background(), map[string]any{"name": "x"}); err == nil {
		t.Fatal("expected no-skills error")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestDescriptionFloorAndBodyCap(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "short.md", "---\nname: s\ndescription: hi\n---\nbody\n")
	if _, err := ParseFile(filepath.Join(dir, "short.md"), "project"); err == nil {
		t.Fatal("stub description must be refused")
	}
	big := "---\nname: big\ndescription: A sufficiently long description here\n---\n" + strings.Repeat("word ", 8001)
	writeSkill(t, dir, "big.md", big)
	if _, err := ParseFile(filepath.Join(dir, "big.md"), "project"); err == nil {
		t.Fatal("oversized body must be refused")
	}
}

// FIX 10 (skills trust): project descriptions render fenced for prompts;
// user descriptions stay raw. Description itself stays raw (pickers/tests).
func TestPromptLineFencesProjectOnly(t *testing.T) {
	proj := Skill{Name: "p", Description: "Project skill for testing", Scope: "project"}
	if got := proj.PromptLine(); !contains(got, "begin untrusted output") || !contains(got, "Project skill for testing") {
		t.Fatalf("project line must fence, got %q", got)
	}
	user := Skill{Name: "u", Description: "User skill for testing", Scope: "user"}
	if got := user.PromptLine(); contains(got, "begin untrusted output") || !contains(got, "User skill for testing") {
		t.Fatalf("user line must stay raw, got %q", got)
	}
}

func TestBundledSkillsAreEmbeddedAndVerified(t *testing.T) {
	builtins, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	if len(builtins) < 3 {
		t.Fatalf("expected core bundled skills, got %d", len(builtins))
	}
	ix, err := ScanDirs("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, sk := range builtins {
		if sk.Scope != "bundled" || !sk.Verified || sk.Version == "" || !strings.HasPrefix(sk.Path, "embedded://") {
			t.Fatalf("bundled provenance incomplete: %+v", sk)
		}
		ix.AddBuiltin(sk)
	}
	for _, name := range []string{"using-agent-skills", "security-hardening", "tui-design"} {
		sk, ok := ix.Get(name)
		if !ok || sk.Scope != "bundled" {
			t.Fatalf("missing bundled skill %q: %+v", name, sk)
		}
	}
}

func TestParseFileEmbeddedSource(t *testing.T) {
	builtins, err := Bundled()
	if err != nil || len(builtins) == 0 {
		t.Fatalf("need bundled skills for this test: %v", err)
	}
	got, err := ParseFile("embedded://bundled/"+builtins[0].Name+".md", "bundled")
	if err != nil {
		t.Fatalf("embedded source must resolve: %v", err)
	}
	if got.Name != builtins[0].Name || got.Body == "" {
		t.Fatalf("wrong skill back: %+v", got.Name)
	}
	if _, err := ParseFile("embedded://bundled/no-such-skill.md", "bundled"); err == nil {
		t.Fatal("unknown embedded skill must error")
	}
}
