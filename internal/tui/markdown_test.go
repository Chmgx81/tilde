package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/glamour/ansi"
)

func TestTildeChromaPalette(t *testing.T) {
	c := tildeChroma()
	if c == nil {
		t.Fatal("chroma mapping must exist")
	}
	// Transparent backgrounds everywhere: no tinted slabs, ever.
	v := reflect.ValueOf(*c)
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i).Interface().(ansi.StylePrimitive)
		if f.BackgroundColor != nil {
			t.Fatalf("field %s must not set a background", v.Type().Field(i).Name)
		}
	}
	col := func(p ansi.StylePrimitive) string {
		if p.Color == nil {
			return ""
		}
		return *p.Color
	}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"Text", col(c.Text), string(fg)},
		{"Comment", col(c.Comment), string(fgDim)},
		{"LiteralString", col(c.LiteralString), string(success)},
		{"LiteralNumber", col(c.LiteralNumber), string(amber)},
		{"Error", col(c.Error), string(danger)},
		{"GenericDeleted", col(c.GenericDeleted), string(danger)},
		{"GenericInserted", col(c.GenericInserted), string(success)},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want palette %q", tc.name, tc.got, tc.want)
		}
	}
	if c.Keyword.Bold == nil || !*c.Keyword.Bold || col(c.Keyword) != string(fg) {
		t.Errorf("keywords must be bold fg, got %+v", c.Keyword)
	}
}

func TestCodeBlockKeepsContent(t *testing.T) {
	// Highlighted or fallback, content is never lost.
	for _, fence := range []string{"go", "python", "nosuchlang", ""} {
		in := "```" + fence + "\nfunc main() {\n}\n```"
		got := stripANSI(renderMarkdownText(in, 60))
		if !strings.Contains(got, "func main()") {
			t.Fatalf("fence %q lost content: %q", fence, got)
		}
	}
}

func TestToolCodeFenceUsesMarkdownRenderer(t *testing.T) {
	got := stripANSI(renderToolResult("--- begin untrusted output ---\n```go\nfunc main() {}\n```\n--- end untrusted output ---"))
	if !strings.Contains(got, "func main() {}") {
		t.Fatalf("fenced tool output lost code: %q", got)
	}
	if strings.Contains(got, "```go") {
		t.Fatalf("code fence should be rendered, not shown as raw markup: %q", got)
	}
}
