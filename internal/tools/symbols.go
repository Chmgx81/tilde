package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// --- symbol_search (P2-C) ---
//
// Read-only, stdlib-only symbol lookup with no LSP dependency.
// Two passes over candidate source files:
//  1. language-aware definition regexes per file extension;
//  2. fallback case-insensitive substring hits labeled "reference".
//
// Mirrors Grep conventions: skips VCS/vendor dirs, >512KB files, and
// symlinks (never followed); matched files are marked in SeenMap.

type SymbolSearch struct {
	Root string
	Seen *SeenMap // matched files count as seen (partial views)
}

func (t *SymbolSearch) Name() string { return "symbol_search" }
func (t *SymbolSearch) Description() string {
	return "Search code symbol definitions by name (go/py/ts/js/rs) with a reference fallback. Read-only."
}
func (t *SymbolSearch) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Symbol name substring, case-insensitive"},
			"lang":  map[string]any{"type": "string", "description": "Optional language filter: go|py|ts|js|rs"},
			"max":   map[string]any{"type": "number", "description": "Max results, default 30, cap 100"},
		}, "required": []string{"query"}}
}

// skipDirs mirrors search.go plus common build/vendor dirs.
var symbolSkipDirs = map[string]bool{
	".git": true, ".hg": true, "node_modules": true,
	"dist": true, "__pycache__": true, ".venv": true, "target": true,
}

const symbolMaxFileBytes = 512 * 1024

func symbolExtsForLang(lang string) (map[string]bool, error) {
	all := map[string][]string{
		"go": {"go"},
		"py": {"py"},
		"ts": {"ts", "tsx", "mts", "cts"},
		"js": {"js", "jsx", "mjs", "cjs"},
		"rs": {"rs"},
	}
	var wanted []string
	if lang == "" {
		for _, exts := range all {
			wanted = append(wanted, exts...)
		}
	} else {
		exts, ok := all[strings.ToLower(lang)]
		if !ok {
			return nil, fmt.Errorf("unknown lang %q: use one of go|py|ts|js|rs or omit the filter", lang)
		}
		wanted = exts
	}
	set := map[string]bool{}
	for _, e := range wanted {
		set["."+e] = true
	}
	return set, nil
}

var (
	reGoMethod = regexp.MustCompile(`^\s*func\s+\([^)]+\)\s*([A-Za-z_][A-Za-z0-9_]*)\b`)
	reGoFunc   = regexp.MustCompile(`^\s*func\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	reGoType   = regexp.MustCompile(`^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	reGoConst  = regexp.MustCompile(`^\s*const\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	reGoVar    = regexp.MustCompile(`^\s*var\s+([A-Za-z_][A-Za-z0-9_]*)\b`)

	rePyDef   = regexp.MustCompile(`^\s*def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	rePyClass = regexp.MustCompile(`^\s*class\s+([A-Za-z_][A-Za-z0-9_]*)\b`)

	reTSFunc  = regexp.MustCompile(`^\s*(?:export\s+)?(?:export\s+default\s+)?function\s+(?:\*\s*)?([A-Za-z_$][A-Za-z0-9_$]*)\b`)
	reTSClass = regexp.MustCompile(`^\s*(?:export\s+)?(?:export\s+default\s+)?class\s+([A-Za-z_$][A-Za-z0-9_$]*)\b`)
	reTSIface = regexp.MustCompile(`^\s*(?:export\s+)?(?:export\s+default\s+)?interface\s+([A-Za-z_$][A-Za-z0-9_$]*)\b`)
	reTSEnum  = regexp.MustCompile(`^\s*(?:export\s+)?(?:export\s+default\s+)?enum\s+([A-Za-z_$][A-Za-z0-9_$]*)\b`)
	reTSVar   = regexp.MustCompile(`^\s*(?:export\s+)?(const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=`)

	reRsFn     = regexp.MustCompile(`^\s*(?:pub(?:\s*\([^)]*\))?\s+)?fn\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	reRsStruct = regexp.MustCompile(`^\s*(?:pub(?:\s*\([^)]*\))?\s+)?struct\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	reRsEnum   = regexp.MustCompile(`^\s*(?:pub(?:\s*\([^)]*\))?\s+)?enum\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	reRsTrait  = regexp.MustCompile(`^\s*(?:pub(?:\s*\([^)]*\))?\s+)?trait\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	reRsImpl   = regexp.MustCompile(`^\s*impl\b(.*)`)
	reRsIdent  = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
)

// tryDef attempts the per-extension definition regexes on one line.
// Returns kind, name, true on a syntactic definition match (before query filtering).
func tryDef(ext, line string) (string, string, bool) {
	switch ext {
	case ".go":
		if m := reGoMethod.FindStringSubmatch(line); m != nil {
			return "method", m[1], true
		}
		if m := reGoFunc.FindStringSubmatch(line); m != nil {
			return "func", m[1], true
		}
		if m := reGoType.FindStringSubmatch(line); m != nil {
			return "type", m[1], true
		}
		if m := reGoConst.FindStringSubmatch(line); m != nil {
			return "const", m[1], true
		}
		if m := reGoVar.FindStringSubmatch(line); m != nil {
			return "var", m[1], true
		}
	case ".py":
		if m := rePyDef.FindStringSubmatch(line); m != nil {
			return "def", m[1], true
		}
		if m := rePyClass.FindStringSubmatch(line); m != nil {
			return "class", m[1], true
		}
	case ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs":
		if m := reTSFunc.FindStringSubmatch(line); m != nil {
			return "function", m[1], true
		}
		if m := reTSClass.FindStringSubmatch(line); m != nil {
			return "class", m[1], true
		}
		if m := reTSIface.FindStringSubmatch(line); m != nil {
			return "interface", m[1], true
		}
		if m := reTSEnum.FindStringSubmatch(line); m != nil {
			return "enum", m[1], true
		}
		if m := reTSVar.FindStringSubmatch(line); m != nil {
			kind := m[1]
			if strings.Contains(line, "=>") {
				kind = "arrow"
			}
			return kind, m[2], true
		}
	case ".rs":
		if m := reRsFn.FindStringSubmatch(line); m != nil {
			return "fn", m[1], true
		}
		if m := reRsStruct.FindStringSubmatch(line); m != nil {
			return "struct", m[1], true
		}
		if m := reRsEnum.FindStringSubmatch(line); m != nil {
			return "enum", m[1], true
		}
		if m := reRsTrait.FindStringSubmatch(line); m != nil {
			return "trait", m[1], true
		}
		if m := reRsImpl.FindStringSubmatch(line); m != nil {
			if name, ok := parseRsImplName(m[1]); ok {
				return "impl", name, true
			}
		}
	}
	return "", "", false
}

// parseRsImplName extracts the implemented type name from the text after "impl".
// "Foo {" -> "Foo"; "Foo for Bar {" -> "Bar"; "<T> Foo<T> {" -> "Foo".
func parseRsImplName(rest string) (string, bool) {
	rest = strings.TrimSpace(rest)
	// Strip a leading <...> generic parameter list.
	if strings.HasPrefix(rest, "<") {
		depth := 0
		for i, r := range rest {
			switch r {
			case '<':
				depth++
			case '>':
				depth--
				if depth == 0 {
					rest = strings.TrimSpace(rest[i+1:])
					goto stripped
				}
			}
		}
		return "", false
	stripped:
	}
	// "Trait for Type" implements Type: name the type.
	if idx := strings.Index(rest, " for "); idx >= 0 {
		rest = strings.TrimSpace(rest[idx+len(" for "):])
	}
	name := reRsIdent.FindString(rest)
	if name == "" {
		return "", false
	}
	return name, true
}

type symbolHit struct {
	rel  string
	line int
	kind string
	name string
}

func (t *SymbolSearch) Exec(_ context.Context, args map[string]any) (string, error) {
	query, err := strArg(args, "query")
	if err != nil {
		return "", err
	}
	lang := strings.ToLower(strings.TrimSpace(optStr(args, "lang", "")))
	exts, err := symbolExtsForLang(lang)
	if err != nil {
		return "", err
	}
	max := optInt(args, "max", 30)
	if max <= 0 {
		max = 30
	}
	if max > 100 {
		max = 100
	}
	qLower := strings.ToLower(query)

	// Collect candidate files in walk order (deterministic: Walk reads
	// directories in lexical order), mirroring Grep's skip rules.
	var files []string
	skippedLinks := 0
	stop := false
	_ = filepath.Walk(t.Root, func(path string, info os.FileInfo, werr error) error {
		if stop {
			return filepath.SkipAll
		}
		if werr != nil {
			return nil
		}
		if info.IsDir() {
			if symbolSkipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if st, lerr := os.Lstat(path); lerr != nil || st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			skippedLinks++
			return nil
		}
		if info.Size() > symbolMaxFileBytes {
			return nil
		}
		if !exts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		files = append(files, path)
		return nil
	})

	// Pass 1: definitions whose symbol name contains the query.
	var defs []symbolHit
	defLines := map[string]bool{} // rel:line -> already emitted as def
	seenFiles := map[string]bool{}
	capped := false
outerDef:
	for _, path := range files {
		rel, _ := filepath.Rel(t.Root, path)
		ext := strings.ToLower(filepath.Ext(path))
		hits, herr := symbolDefsInFile(path, rel, ext, qLower)
		if herr != nil {
			continue
		}
		for _, h := range hits {
			if len(defs) >= max {
				capped = true
				break outerDef
			}
			defs = append(defs, h)
			defLines[fmt.Sprintf("%s:%d", h.rel, h.line)] = true
			seenFiles[h.rel] = true
		}
	}
	if len(defs) >= max {
		capped = true
	}

	// Pass 2 (fallback): case-insensitive substring hits labeled reference,
	// skipping lines already emitted as definitions, filling the budget.
	var refs []symbolHit
	if !capped {
		remain := max - len(defs)
	outerRef:
		for _, path := range files {
			rel, _ := filepath.Rel(t.Root, path)
			hits, herr := symbolRefsInFile(path, rel, qLower, defLines, remain-len(refs))
			if herr != nil {
				continue
			}
			for _, h := range hits {
				refs = append(refs, h)
				seenFiles[h.rel] = true
				if len(refs) >= remain {
					break outerRef
				}
			}
		}
		// Like Grep, the cap note fires whenever the budget is full:
		// there may be more past the edge, so say so.
		if len(defs)+len(refs) >= max {
			capped = true
		}
	}

	total := append(append([]symbolHit{}, defs...), refs...)
	sort.Slice(total, func(i, j int) bool {
		di, dj := defLines[fmt.Sprintf("%s:%d", total[i].rel, total[i].line)], defLines[fmt.Sprintf("%s:%d", total[j].rel, total[j].line)]
		if di != dj {
			return di // definitions first
		}
		if total[i].rel != total[j].rel {
			return total[i].rel < total[j].rel
		}
		return total[i].line < total[j].line
	})
	if len(total) > max {
		total = total[:max]
		capped = true
	}

	linkNote := ""
	if skippedLinks > 0 {
		plural := "ies"
		if skippedLinks == 1 {
			plural = "y"
		}
		linkNote = fmt.Sprintf("\n[note: skipped %d symlink/non-regular entr%s — links are never followed]", skippedLinks, plural)
	}
	if len(total) == 0 {
		return "", fmt.Errorf("no symbols matching %q: not an error — try a shorter query or drop the lang filter%s", query, linkNote)
	}
	lines := make([]string, 0, len(total))
	for _, h := range total {
		lines = append(lines, fmt.Sprintf("%s:%d: %s %s", h.rel, h.line, h.kind, h.name))
	}
	out := strings.Join(lines, "\n")
	if capped {
		out += fmt.Sprintf("\n[truncated: showing first %d of %d+ matches — narrow the query and retry to see the rest]", max, max)
	}
	out += linkNote
	if t.Seen != nil {
		for f := range seenFiles {
			t.Seen.Mark(f)
		}
	}
	return Fence(out), nil
}

// symbolDefsInFile scans one file for definitions whose name contains qLower.
func symbolDefsInFile(path, rel, ext, qLower string) ([]symbolHit, error) {
	if st, lerr := os.Lstat(path); lerr != nil || st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	var out []symbolHit
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 256*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		kind, name, ok := tryDef(ext, sc.Text())
		if !ok {
			continue
		}
		if !strings.Contains(strings.ToLower(name), qLower) {
			continue
		}
		out = append(out, symbolHit{rel: rel, line: lineNo, kind: kind, name: name})
	}
	if err := sc.Err(); err != nil {
		// Same receipt as search.go: lines past the 256KB cap are
		// silently unsearched unless we say so.
		out = append(out, symbolHit{rel: rel, line: lineNo + 1, kind: "note", name: fmt.Sprintf("[lines past 256KB skipped: %v — sample with read_file instead]", err)})
	}
	return out, nil
}

// symbolRefsInFile scans one file for case-insensitive substring hits,
// skipping lines already emitted as definitions. Budget caps the hits.
func symbolRefsInFile(path, rel, qLower string, skip map[string]bool, budget int) ([]symbolHit, error) {
	if budget <= 0 {
		return nil, nil
	}
	if st, lerr := os.Lstat(path); lerr != nil || st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	var out []symbolHit
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 256*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if skip[fmt.Sprintf("%s:%d", rel, lineNo)] {
			continue
		}
		text := sc.Text()
		if !strings.Contains(strings.ToLower(text), qLower) {
			continue
		}
		name := strings.TrimSpace(text)
		if len(name) > 120 {
			name = name[:120] + "…"
		}
		if name == "" {
			name = "(blank line)"
		}
		out = append(out, symbolHit{rel: rel, line: lineNo, kind: "reference", name: name})
		if len(out) >= budget {
			break
		}
	}
	if err := sc.Err(); err != nil {
		out = append(out, symbolHit{rel: rel, line: lineNo + 1, kind: "note", name: fmt.Sprintf("[lines past 256KB skipped: %v — sample with read_file instead]", err)})
	}
	return out, nil
}
