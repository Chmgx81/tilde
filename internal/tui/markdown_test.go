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

func TestOrderedListNumbersHaveSeparator(t *testing.T) {
	md := "1. First item\n2. Second item\n3. Third item"
	got := stripANSI(renderMarkdownText(md, 60))
	if !strings.Contains(got, "1. First item") {
		t.Fatalf("ordered list 1 missing dot separator: %q", got)
	}
	if !strings.Contains(got, "2. Second item") {
		t.Fatalf("ordered list 2 missing dot separator: %q", got)
	}
	if strings.Contains(got, "1First") || strings.Contains(got, "1First item") {
		t.Fatalf("numbers must not glue to text: %q", got)
	}
}

func TestNormalizeOrderedListBareNumbers(t *testing.T) {
	in := "1Dead else branch\n2No type checks\n3No validation"
	got := stripANSI(renderMarkdownText(in, 60))
	// After normalisation these become proper list items: "1. Dead",
	// "2. No", "3. No" — each number must be separated from its text.
	if strings.Contains(got, "1Dead") {
		t.Fatalf("bare number should be normalised: %q", got)
	}
	if strings.Contains(got, "2No type checks") {
		t.Fatalf("bare number should be normalised: %q", got)
	}
	if !strings.Contains(got, "Dead else branch") {
		t.Fatalf("item text must survive normalisation: %q", got)
	}
}

func TestNormalizeOrderedListInsertsBlankLine(t *testing.T) {
	// LLMs often emit a heading followed immediately by numbered items
	// with no blank line: Glamour needs the blank to separate the list
	// from the preceding paragraph.
	in := "Key bugs/issues:\n1Dead else branch\n2No type checks"
	got := stripANSI(renderMarkdownText(in, 80))
	// The items must render as a list, not inline text. After
	// normalisation + blank-line insertion, "1. Dead" should appear
	// on its own line (not glued to "Key bugs/issues:").
	if strings.Contains(got, "issues:1.") || strings.Contains(got, "issues: 1.") {
		t.Fatalf("list must be separated from heading: %q", got)
	}
	if strings.Contains(got, "1Dead") {
		t.Fatalf("bare number not normalised: %q", got)
	}
}

func TestNormalizeOrderedListNoFalsePositives(t *testing.T) {
	// Lines like "3am" or "2nd" must not be treated as list items.
	in := "It was 3am\nHe finished 2nd\n5Redundant seed"
	got := stripANSI(renderMarkdownText(in, 80))
	if strings.Contains(got, "3. am") {
		t.Fatalf("3am must not be normalised: %q", got)
	}
	if strings.Contains(got, "2. nd") {
		t.Fatalf("2nd must not be normalised: %q", got)
	}
	// 5Redundant → 5. Redundant (uppercase after digit = list item)
	if strings.Contains(got, "5Redundant") {
		t.Fatalf("5Redundant should be normalised: %q", got)
	}
}

func TestNormalizeSkipsNonListNumbers(t *testing.T) {
	// Lowercase after number: not a list item (e.g. "3am", "2nd place").
	in := "It was 3am when it happened\nHe came 2nd in the race"
	got := renderMarkdownText(in, 60)
	if strings.Contains(got, "3. am") || strings.Contains(got, "2. nd") {
		t.Fatalf("lowercase after number must not be normalised: %q", got)
	}
}
