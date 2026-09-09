package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDiag(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnoseDirtyFormat(t *testing.T) {
	root := t.TempDir()
	writeDiag(t, root, "dirty.go", "package x\n\nfunc  Foo()  {}\n")
	d := &Diagnose{Root: root}
	out, err := d.Exec(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dirty.go") || !strings.Contains(out, "format") {
		t.Fatalf("expected format finding for dirty.go, got %q", out)
	}
	if !strings.Contains(out, "gofmt -w") {
		t.Fatalf("expected gofmt fix note, got %q", out)
	}
	if !strings.Contains(out, "[summary:") {
		t.Fatalf("expected summary counts, got %q", out)
	}
}

func TestDiagnoseParseError(t *testing.T) {
	root := t.TempDir()
	writeDiag(t, root, "bad.go", "package x\n\nfunc Broken( {\n")
	d := &Diagnose{Root: root}
	out, err := d.Exec(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "bad.go:3: parse") {
		t.Fatalf("expected bad.go:3 parse error, got %q", out)
	}
}

func TestDiagnoseTodo(t *testing.T) {
	root := t.TempDir()
	writeDiag(t, root, "t.go", "package x\n\n// TODO: fix this\nfunc Ok() {}\n")
	led := NewSeenMap(root)
	d := &Diagnose{Root: root, Seen: led}
	out, err := d.Exec(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "t.go:3: todo") || !strings.Contains(out, "TODO: fix this") {
		t.Fatalf("expected t.go:3 todo finding, got %q", out)
	}
	if !led.Has("t.go") {
		t.Fatal("file with findings should count as seen")
	}
}

func TestDiagnoseClean(t *testing.T) {
	root := t.TempDir()
	writeDiag(t, root, "clean.go", "package x\n\nfunc Clean() {}\n")
	d := &Diagnose{Root: root}
	out, err := d.Exec(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "clean") {
		t.Fatalf("expected clean verdict, got %q", out)
	}
	if strings.Contains(out, "format") && strings.Contains(out, "gofmt-dirty") {
		t.Fatalf("clean file must not report format, got %q", out)
	}
}

func TestDiagnoseSkipsNodeModules(t *testing.T) {
	root := t.TempDir()
	writeDiag(t, root, "node_modules/evil.go", "package x\n\nfunc  Evil()  {}\n")
	writeDiag(t, root, "ok.go", "package x\n\nfunc Ok() {}\n")
	d := &Diagnose{Root: root}
	out, err := d.Exec(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "node_modules") {
		t.Fatalf("node_modules must be skipped, got %q", out)
	}
	if !strings.Contains(out, "clean") {
		t.Fatalf("expected clean verdict with only ok.go checked, got %q", out)
	}
}

func TestDiagnoseCapNote(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		writeDiag(t, root, fmt.Sprintf("f%d.go", i), fmt.Sprintf("package x\n\nfunc  F%d()  {}\n", i))
	}
	d := &Diagnose{Root: root}
	out, err := d.Exec(context.Background(), map[string]any{"max": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "showing first 2") || !strings.Contains(out, "truncated") {
		t.Fatalf("expected cap note, got %q", out)
	}
}
