package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tilde/internal/policy"
)

// deny_paths must be a content boundary, not only an argument one: a broad
// grep dir must not read a denied file's contents.
func TestGrepHonorsDenyPathsContent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "secrets", "token.txt"), []byte("AKIA-SECRET-LEAK\n"), 0o644)
	os.WriteFile(filepath.Join(root, "public.txt"), []byte("public line\n"), 0o644)

	f := &policy.File{DenyPaths: []string{"**/secrets/**"}}
	g := &Grep{Root: root, Denied: func(p string) bool { return f.DeniesPath(root, p) }}

	// Searching the denied content from "." must find nothing and say why.
	if _, err := g.Exec(context.Background(), map[string]any{"pattern": "SECRET", "dir": "."}); err == nil {
		t.Fatal("denied-only match should report no matches")
	} else {
		if strings.Contains(err.Error(), "SECRET-LEAK") {
			t.Fatalf("denied file content leaked into the result: %v", err)
		}
		if !strings.Contains(err.Error(), "deny_paths-matched") {
			t.Fatalf("result should note the pruned denied file, got %v", err)
		}
	}

	// A non-denied path still searches normally.
	out, err := g.Exec(context.Background(), map[string]any{"pattern": "public"})
	if err != nil || !strings.Contains(out, "public line") {
		t.Fatalf("non-denied grep must work, got %q, %v", out, err)
	}
}

func TestGlobHonorsDenyPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "secrets", "a.key"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("x"), 0o644)

	f := &policy.File{DenyPaths: []string{"**/*.key"}}
	g := &Glob{Root: root, Denied: func(p string) bool { return f.DeniesPath(root, p) }}

	if _, err := g.Exec(context.Background(), map[string]any{"pattern": "**/*.key"}); err == nil {
		t.Fatal("a fully-denied glob should report no files")
	}
	out, err := g.Exec(context.Background(), map[string]any{"pattern": "**/*"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "a.key") {
		t.Fatalf("glob listed a denied file: %q", out)
	}
	if !strings.Contains(out, "b.txt") {
		t.Fatalf("glob must still list allowed files: %q", out)
	}
}

// The index-based walkers must prune denied files too (definitions and the
// reference fallback must not surface a denied file's symbols).
func TestSymbolSearchHonorsDenyPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "secrets", "s.go"), []byte("package secrets\n\nfunc LeakSecret() {}\n"), 0o644)
	os.WriteFile(filepath.Join(root, "ok.go"), []byte("package ok\n\nfunc FineSymbol() {}\n"), 0o644)

	f := &policy.File{DenyPaths: []string{"**/secrets/**"}}
	s := &SymbolSearch{Root: root, Denied: func(p string) bool { return f.DeniesPath(root, p) }}

	out, err := s.Exec(context.Background(), map[string]any{"query": "LeakSecret"})
	if err == nil && strings.Contains(out, "LeakSecret") {
		t.Fatalf("denied symbol leaked: %q", out)
	}
	out2, err := s.Exec(context.Background(), map[string]any{"query": "FineSymbol"})
	if err != nil || !strings.Contains(out2, "FineSymbol") {
		t.Fatalf("allowed symbol must be found, got %q, %v", out2, err)
	}
}
