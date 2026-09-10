package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSavePlanCreatesFile(t *testing.T) {
	root := t.TempDir()
	p := &SavePlan{Root: root}
	ctx := context.Background()
	out, err := p.Exec(ctx, map[string]any{"title": "Add Login Flow", "content": "# Plan\n\n1. do things\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, ".tilde/plans/add-login-flow.md") {
		t.Fatalf("result must name the saved path, got %q", out)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".tilde", "plans", "add-login-flow.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "# Plan\n\n1. do things\n" {
		t.Fatalf("bad file content: %q", raw)
	}
	if p.Name() != "save_plan" {
		t.Fatalf("bad tool name: %q", p.Name())
	}
}

func TestSavePlanSlugSanitization(t *testing.T) {
	p := &SavePlan{Root: t.TempDir()}
	ctx := context.Background()
	out, err := p.Exec(ctx, map[string]any{"title": "  Fix: Auth!! / Retry @Home  ", "content": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "fix-auth-retry-home.md") {
		t.Fatalf("slug not sanitized, got %q", out)
	}
	if _, err := p.Exec(ctx, map[string]any{"title": "!!!", "content": "x"}); err == nil {
		t.Fatal("empty slug must error")
	}
}

func TestSavePlanDotDotSanitized(t *testing.T) {
	// Sanitize-then-verify invariant: slugs can never contain / or ..
	// after planSlug, so traversal titles sanitize (contain()+prefix
	// verify owns containment) — only a title with no alphanumerics
	// errors, on the empty slug.
	p := &SavePlan{Root: t.TempDir()}
	ctx := context.Background()
	if _, err := p.Exec(ctx, map[string]any{"title": "..", "content": "x"}); err == nil {
		t.Fatal(".. title must error on the empty slug")
	}
	out, err := p.Exec(ctx, map[string]any{"title": "a .. b", "content": "x"})
	if err != nil {
		t.Fatalf("dotted title must sanitize, got %v", err)
	}
	if !contains(out, "a-b.md") {
		t.Fatalf("dotted title must sanitize to a-b, got %q", out)
	}
}

func TestSavePlanOverwriteSucceeds(t *testing.T) {
	root := t.TempDir()
	p := &SavePlan{Root: root}
	ctx := context.Background()
	if _, err := p.Exec(ctx, map[string]any{"title": "Revise Me", "content": "v1"}); err != nil {
		t.Fatal(err)
	}
	out, err := p.Exec(ctx, map[string]any{"title": "Revise Me", "content": "v2"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(strings.ToLower(out), "overwrite") && !contains(strings.ToLower(out), "revis") {
		t.Fatalf("result must state overwrite/revision, got %q", out)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".tilde", "plans", "revise-me.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "v2" {
		t.Fatalf("overwrite failed, got %q", raw)
	}
}

func TestPlanFileStaysUnderPlansDir(t *testing.T) {
	root := t.TempDir()
	p := &SavePlan{Root: root}
	ctx := context.Background()
	for _, title := range []string{"/abs/path", "../../escape", "a/b/c"} {
		if _, err := p.Exec(ctx, map[string]any{"title": title, "content": "x"}); err != nil {
			t.Fatalf("sanitized title %q should save inside plans, got %v", title, err)
		}
	}
	var stray []string
	filepath.Walk(root, func(fp string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && !contains(fp, filepath.Join(root, ".tilde", "plans")) {
			stray = append(stray, fp)
		}
		return nil
	})
	if len(stray) > 0 {
		t.Fatalf("plan wrote outside plans dir: %v", stray)
	}
}
