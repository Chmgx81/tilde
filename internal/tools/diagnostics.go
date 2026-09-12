package tools

import (
	"bytes"
	"context"
	"fmt"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// --- diagnose (P3-B) ---
//
// Read-only, stdlib-only Go diagnostics with no exec and no language
// servers: go/parser for syntax errors and comment positions, go/format
// for gofmt-dirty detection. Walks .go files and reports gofmt-dirty
// files, parse errors, and TODO/FIXME/XXX comments as
// `path:line: kind message` lines plus summary counts.
//
// Mirrors Grep conventions: skips VCS/vendor/build dirs, >512KB files,
// and symlinks (never followed); files with findings count as seen.

type Diagnose struct {
	Root string
	Seen *SeenMap // files with findings count as seen (partial views)
	// Denied, when set, prunes deny_paths-matched files (content boundary).
	Denied func(path string) bool
}

func (t *Diagnose) Name() string { return "diagnose" }
func (t *Diagnose) Description() string {
	return "Diagnose Go files: gofmt-dirty files, parse errors, TODO/FIXME/XXX. Read-only."
}
func (t *Diagnose) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Subdir or file to diagnose, default repo root"},
			"max":  map[string]any{"type": "number", "description": "Max findings, default 50, cap 200"},
		}, "required": []string{}}
}

// diagnoseSkipDirs mirrors search.go plus common build/vendor dirs.
var diagnoseSkipDirs = map[string]bool{
	".git": true, ".hg": true, "node_modules": true,
	"dist": true, "__pycache__": true, ".venv": true, "target": true,
}

const diagnoseMaxFileBytes = 512 * 1024

type diagFinding struct {
	rel  string
	line int
	kind string // format | parse | todo
	msg  string
}

func (t *Diagnose) Exec(_ context.Context, args map[string]any) (string, error) {
	sub := optStr(args, "path", ".")
	max := optInt(args, "max", 50)
	if max <= 0 {
		max = 50
	}
	if max > 200 {
		max = 200
	}
	base := t.Root
	if sub != "." && sub != "" {
		var err error
		if base, err = contain(t.Root, sub); err != nil {
			return "", err
		}
	}

	var files []string
	skippedLinks := 0
	st, serr := os.Lstat(base)
	if serr != nil {
		return "", fmt.Errorf("cannot diagnose %q: %v — check the path with glob first, then retry", sub, serr)
	}
	switch {
	case st.Mode()&os.ModeSymlink != 0:
		return "", fmt.Errorf("refusing %q: symlink — links are never followed, point at a real file or dir inside the project and retry", sub)
	case !st.IsDir():
		if !st.Mode().IsRegular() {
			return "", fmt.Errorf("refusing %q: not a regular file — point at a real .go file inside the project and retry", sub)
		}
		if t.Denied != nil && t.Denied(base) {
			return "", fmt.Errorf("refusing %q: matches a deny_paths glob — choose a path outside the denied patterns", sub)
		}
		if st.Size() > diagnoseMaxFileBytes {
			return "", fmt.Errorf("file %q is over the 512KB cap: not diagnosed — sample it with read_file instead", sub)
		}
		if strings.ToLower(filepath.Ext(base)) != ".go" {
			return "", fmt.Errorf("nothing to diagnose in %q: not a .go file — point at a .go file or a directory and retry", sub)
		}
		files = []string{base}
	default:
		_ = filepath.Walk(base, func(path string, info os.FileInfo, werr error) error {
			if werr != nil {
				return nil // skip unreadable entries, keep going
			}
			if info.IsDir() {
				if diagnoseSkipDirs[info.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			// Symlink/irregular escape: Walk does not follow links, but
			// opening one would — Lstat each entry and skip anything
			// that is not a plain regular file.
			if lst, lerr := os.Lstat(path); lerr != nil || lst.Mode()&os.ModeSymlink != 0 || !lst.Mode().IsRegular() {
				skippedLinks++
				return nil
			}
			if t.Denied != nil && t.Denied(path) {
				return nil // deny_paths: never diagnose a denied file's contents
			}
			if info.Size() > diagnoseMaxFileBytes {
				return nil // skip huge files; read_file samples them instead
			}
			if strings.ToLower(filepath.Ext(path)) != ".go" {
				return nil
			}
			files = append(files, path)
			return nil
		})
	}
	if len(files) == 0 {
		return "", fmt.Errorf("no .go files to diagnose under %q: not an error — try a broader path", sub)
	}

	var findings []diagFinding
	seenFiles := map[string]bool{}
	checked := 0
	capped := false
	nFormat, nParse, nTodo := 0, 0, 0
outer:
	for _, path := range files {
		rel, _ := filepath.Rel(t.Root, path)
		checked++
		for _, f := range diagnoseOne(path, rel) {
			findings = append(findings, f)
			seenFiles[rel] = true
			switch f.kind {
			case "format":
				nFormat++
			case "parse":
				nParse++
			case "todo":
				nTodo++
			}
			if len(findings) >= max {
				capped = true
				break outer
			}
		}
	}

	linkNote := ""
	if skippedLinks > 0 {
		plural := "ies"
		if skippedLinks == 1 {
			plural = "y"
		}
		linkNote = fmt.Sprintf("\n[note: skipped %d symlink/non-regular entr%s — links are never followed]", skippedLinks, plural)
	}
	if len(findings) == 0 {
		out := fmt.Sprintf("[diagnose clean: %d file(s) checked — no gofmt-dirty files, no parse errors, no TODO/FIXME/XXX]", checked)
		return Fence(out + linkNote), nil
	}
	lines := make([]string, 0, len(findings))
	for _, f := range findings {
		lines = append(lines, fmt.Sprintf("%s:%d: %s %s", f.rel, f.line, f.kind, f.msg))
	}
	out := strings.Join(lines, "\n")
	out += fmt.Sprintf("\n[summary: %d file(s) checked, %d gofmt-dirty, %d parse error(s), %d todo(s)]", checked, nFormat, nParse, nTodo)
	if capped {
		out += fmt.Sprintf("\n[truncated: showing first %d of %d+ findings — narrow the path or raise max and retry to see the rest]", max, max)
	}
	out += linkNote
	if t.Seen != nil {
		for f := range seenFiles {
			t.Seen.Mark(f)
		}
	}
	return Fence(out), nil
}

// diagnoseOne reads one vetted .go file and returns its findings.
// Parse errors come first (gofmt is meaningless on broken syntax), then
// a single gofmt-dirty note, then TODO/FIXME/XXX comments.
func diagnoseOne(path, rel string) []diagFinding {
	if st, lerr := os.Lstat(path); lerr != nil || st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	fset := token.NewFileSet()
	f, perr := parser.ParseFile(fset, path, src, parser.ParseComments|parser.AllErrors)
	if perr != nil {
		var out []diagFinding
		if list, ok := perr.(scanner.ErrorList); ok {
			for _, e := range list {
				out = append(out, diagFinding{rel: rel, line: e.Pos.Line, kind: "parse", msg: e.Msg})
			}
		} else {
			out = append(out, diagFinding{rel: rel, line: 1, kind: "parse", msg: perr.Error()})
		}
		// Broken syntax has no AST: still surface TODO markers by line scan.
		out = append(out, scanTodoLines(rel, string(src))...)
		sort.Slice(out, func(i, j int) bool {
			if out[i].line != out[j].line {
				return out[i].line < out[j].line
			}
			return out[i].kind < out[j].kind // parse before todo on one line
		})
		return out
	}
	var out []diagFinding
	if formatted, ferr := format.Source(src); ferr == nil {
		if !bytes.Equal(formatted, src) {
			out = append(out, diagFinding{rel: rel, line: 1, kind: "format",
				msg: fmt.Sprintf("gofmt-dirty (run gofmt -w %s to fix; gofmt -d %s shows the diff)", rel, rel)})
		}
	}
	for _, g := range f.Comments {
		for _, c := range g.List {
			if !hasTodoMarker(c.Text) {
				continue
			}
			pos := fset.Position(c.Pos())
			text := strings.TrimSpace(c.Text)
			if len(text) > 120 {
				text = text[:120] + "…"
			}
			out = append(out, diagFinding{rel: rel, line: pos.Line, kind: "todo", msg: text})
		}
	}
	return out
}

// hasTodoMarker reports the standard uppercase TODO/FIXME/XXX markers.
func hasTodoMarker(text string) bool {
	return strings.Contains(text, "TODO") || strings.Contains(text, "FIXME") || strings.Contains(text, "XXX")
}

// scanTodoLines is the parse-failure fallback: line-oriented TODO scan
// when no AST positions exist.
func scanTodoLines(rel, src string) []diagFinding {
	var out []diagFinding
	for i, line := range strings.Split(src, "\n") {
		if !hasTodoMarker(line) {
			continue
		}
		text := strings.TrimSpace(line)
		if len(text) > 120 {
			text = text[:120] + "…"
		}
		out = append(out, diagFinding{rel: rel, line: i + 1, kind: "todo", msg: text})
	}
	return out
}
