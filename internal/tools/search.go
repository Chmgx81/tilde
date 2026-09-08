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

// --- grep ---

type Grep struct {
	Root string
	Seen *SeenMap // matched files count as seen (partial views)
}

func (t *Grep) Name() string { return "grep" }
func (t *Grep) Description() string {
	return "Search file contents for a substring (case-sensitive). Respects .gitignore lightly by skipping .git/."
}
func (t *Grep) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string"},
			"dir":     map[string]any{"type": "string", "description": "Subdir to search, default repo root"},
		}, "required": []string{"pattern"}}
}

func (t *Grep) Exec(_ context.Context, args map[string]any) (string, error) {
	pat, err := strArg(args, "pattern")
	if err != nil {
		return "", err
	}
	dir := optStr(args, "dir", ".")
	base := t.Root
	if dir != "." && dir != "" {
		if base, err = contain(t.Root, dir); err != nil {
			return "", err
		}
	}
	var matches []string
	count := 0
	stopped := false
	skippedLinks := 0
	seenFiles := map[string]bool{}
	err = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if stopped {
			return filepath.SkipAll
		}
		if err != nil {
			return nil // skip unreadable entries, keep going
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "node_modules" || info.Name() == ".hg" {
				return filepath.SkipDir
			}
			return nil
		}
		// Symlink/irregular escape: Walk does not follow links, but
		// opening one would — a link->outside read (or a fifo block)
		// must never happen. Lstat each entry and skip anything that
		// is not a plain regular file.
		if st, lerr := os.Lstat(path); lerr != nil || st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			skippedLinks++
			return nil
		}
		if info.Size() > 512*1024 {
			return nil // skip huge files; read_file samples them instead
		}
		if err := scanOne(path, pat, t.Root, &matches, seenFiles, &count); err != nil {
			if err == errLimit {
				stopped = true
				return filepath.SkipAll
			}
			return nil
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("search failed in %q: %v — verify the dir exists with glob first", dir, err)
	}
	linkNote := ""
	if skippedLinks > 0 {
		plural := "ies"
		if skippedLinks == 1 {
			plural = "y"
		}
		linkNote = fmt.Sprintf("\n[note: skipped %d symlink/non-regular entr%s — links are never followed]", skippedLinks, plural)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no matches for %q in %q: not an error — try a shorter substring or a different dir%s", pat, dir, linkNote)
	}
	// File header first: weak models re-run grep to "get the list" when
	// matches bury it per-line (measured doom-loop cluster on fan-out
	// tasks). The list is already in context — name it up front.
	files := make([]string, 0, len(seenFiles))
	for f := range seenFiles {
		files = append(files, f)
	}
	sort.Strings(files)
	out := fmt.Sprintf("[files (%d): %s]\n", len(files), strings.Join(files, ", ")) + strings.Join(matches, "\n")
	if count >= 50 {
		out += "\n[truncated: showing first 50 of 50+ matches — narrow the pattern or dir and retry to see the rest]"
	}
	out += linkNote
	if t.Seen != nil {
		for f := range seenFiles {
			t.Seen.Mark(f)
		}
	}
	return Fence(out), nil
}

var errLimit = fmt.Errorf("match limit reached")

// scanOne searches a single file, closing it before returning — never
// held open across a walk (EMFILE on large repos). Over-long lines past
// the scanner cap are reported, never silently unsearched.
func scanOne(path, pat, root string, matches *[]string, seenFiles map[string]bool, count *int) error {
	// Re-check at open time: a link swapped in between the walk and the
	// open must still not be followed (TOCTOU, best-effort).
	if st, lerr := os.Lstat(path); lerr != nil || st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 256*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if strings.Contains(sc.Text(), pat) {
			rel, _ := filepath.Rel(root, path)
			*matches = append(*matches, fmt.Sprintf("%s:%d: %s", rel, lineNo, sc.Text()))
			seenFiles[rel] = true
			*count++
			if *count >= 50 {
				return errLimit
			}
		}
	}
	if err := sc.Err(); err != nil {
		rel, _ := filepath.Rel(root, path)
		*matches = append(*matches, fmt.Sprintf("%s: [lines past 256KB skipped: %v — sample with read_file instead]", rel, err))
		*count++
		if *count >= 50 {
			return errLimit
		}
	}
	return nil
}

// --- glob ---

type Glob struct{ Root string }

func (t *Glob) Name() string { return "glob" }
func (t *Glob) Description() string {
	return "List files matching a glob pattern, e.g. **/*.go. Respects .git/ skip."
}
func (t *Glob) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string", "description": "Glob relative to repo root"},
		}, "required": []string{"pattern"}}
}

func (t *Glob) Exec(_ context.Context, args map[string]any) (string, error) {
	pat, err := strArg(args, "pattern")
	if err != nil {
		return "", err
	}
	var rels []string
	if strings.Contains(pat, "**") {
		rels = walkGlob(t.Root, pat)
	} else {
		hits, gerr := filepath.Glob(filepath.Join(t.Root, pat))
		if gerr != nil {
			return "", fmt.Errorf("bad glob pattern %q: %v — try e.g. \"**/*.go\"", pat, gerr)
		}
		for _, h := range hits {
			if rel, rerr := filepath.Rel(t.Root, h); rerr == nil {
				rels = append(rels, rel)
			}
		}
	}
	var kept []string
	capped := false
	for _, rel := range rels {
		if rel == "." || strings.HasPrefix(rel, ".git/") || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			continue // never surface .git or anything outside root
		}
		kept = append(kept, rel)
		if len(kept) >= 50 {
			capped = true
			break
		}
	}
	if len(kept) == 0 {
		return "", fmt.Errorf("no files match %q: not an error — try a broader pattern like \"**/*.go\"", pat)
	}
	out := strings.Join(kept, "\n")
	if capped {
		out += "\n[truncated: showing first 50 of 50+ files — narrow the pattern and retry to see the rest]"
	}
	// Filenames are attacker-influenced: fence them.
	return Fence(out), nil
}

// walkGlob matches ** patterns by translating to regex: ** spans
// separators, * and ? stay within one path segment.
func walkGlob(root, pat string) []string {
	rx, err := globRegex(pat)
	if err != nil {
		return nil
	}
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if rel, rerr := filepath.Rel(root, path); rerr == nil {
			if rx.MatchString(filepath.ToSlash(rel)) {
				out = append(out, rel)
			}
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func globRegex(pat string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pat); {
		switch {
		case strings.HasPrefix(pat[i:], "**/"):
			b.WriteString("(.*/)?")
			i += 3
		case strings.HasPrefix(pat[i:], "**"):
			b.WriteString(".*")
			i += 2
		case pat[i] == '*':
			b.WriteString("[^/]*")
			i++
		case pat[i] == '?':
			b.WriteString("[^/]")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(pat[i])))
			i++
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
