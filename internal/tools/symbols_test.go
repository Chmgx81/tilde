package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSym(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSymbolGoDefs(t *testing.T) {
	root := t.TempDir()
	writeSym(t, root, "a.go", `package x

func Alpha() {}
func (r Receiver) MethodAlpha() {}
func (r *Receiver) PtrAlpha() {}
type Beta struct{}
const Gamma = 1
var Delta = 2
`)
	s := &SymbolSearch{Root: root}
	out, err := s.Exec(context.Background(), map[string]any{"query": "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"func Alpha", "method MethodAlpha", "method PtrAlpha"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	out2, err := s.Exec(context.Background(), map[string]any{"query": "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "type Beta") {
		t.Fatalf("missing type Beta in %q", out2)
	}
}

func TestSymbolPyTsDefs(t *testing.T) {
	root := t.TempDir()
	writeSym(t, root, "m.py", "def my_handler():\n    pass\n\nclass MyWidget:\n    pass\n")
	writeSym(t, root, "app.ts", "export function myLoader() {}\nexport class MyPanel {}\ninterface MyShape {}\nenum MyMode {}\nexport const myArrow = (x) => x\n")
	s := &SymbolSearch{Root: root}
	out, err := s.Exec(context.Background(), map[string]any{"query": "my"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"def my_handler", "class MyWidget", "function myLoader", "class MyPanel", "interface MyShape", "enum MyMode", "arrow myArrow"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	// lang filter narrows to python only.
	outPy, err := s.Exec(context.Background(), map[string]any{"query": "my_", "lang": "py"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outPy, "def my_handler") || strings.Contains(outPy, "myLoader") {
		t.Fatalf("lang filter failed: %q", outPy)
	}
}

func TestSymbolReferenceFallback(t *testing.T) {
	root := t.TempDir()
	writeSym(t, root, "r.go", "package x\n\n// zebrafallback mentioned only in a comment\nfunc Unrelated() {}\n")
	s := &SymbolSearch{Root: root}
	out, err := s.Exec(context.Background(), map[string]any{"query": "zebrafallback"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "reference") {
		t.Fatalf("expected reference fallback, got %q", out)
	}
}

func TestSymbolCapNote(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, "func CapSym%d() {}\n", i)
	}
	writeSym(t, root, "c.go", b.String())
	s := &SymbolSearch{Root: root}
	out, err := s.Exec(context.Background(), map[string]any{"query": "capsym", "max": float64(3)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "showing first 3") || !strings.Contains(out, "narrow the query") {
		t.Fatalf("expected cap note naming narrower-query fix, got %q", out)
	}
}

func TestSymbolSkipsNodeModules(t *testing.T) {
	root := t.TempDir()
	writeSym(t, root, "node_modules/evil.js", "function SkippedSym() {}\n")
	writeSym(t, root, "ok.js", "function SkippedSym() {}\n")
	s := &SymbolSearch{Root: root}
	out, err := s.Exec(context.Background(), map[string]any{"query": "skippedsym"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ok.js") {
		t.Fatalf("expected ok.js hit, got %q", out)
	}
	if strings.Contains(out, "node_modules") {
		t.Fatalf("node_modules must be skipped, got %q", out)
	}
}

func TestSymbolHitCountsAsSeen(t *testing.T) {
	root := t.TempDir()
	writeSym(t, root, "s.go", "package x\nfunc SeenSym() {}\n")
	led := NewSeenMap(root)
	s := &SymbolSearch{Root: root, Seen: led}
	if _, err := s.Exec(context.Background(), map[string]any{"query": "seensym"}); err != nil {
		t.Fatal(err)
	}
	if !led.Has("s.go") {
		t.Fatal("symbol hit should count as seen")
	}
}
