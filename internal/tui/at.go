package tui

import (
	"bytes"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type atRow struct {
	path     string
	rendered string // with matched chars bolded
	idx      []int  // matched rune indices (reused for the selected-row render)
}

// walkFiles lists repo files for @-reference. .git is never surfaced;
// everything else is filtered through .gitignore (see filterIgnored).
func walkFiles(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries, keep going
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, ansi.Strip(rel))
		if len(out) >= 20000 {
			return filepath.SkipAll
		}
		return nil
	})
	return filterIgnored(root, out)
}

// filterIgnored removes .gitignore'd paths. Inside a git repo this shells
// to `git check-ignore` (the ground truth, batched on stdin); outside one
// it falls back to skipping common junk dirs. A picker that surfaces
// node_modules/ results has failed at its one job.
func filterIgnored(root string, paths []string) []string {
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		if kept := checkIgnore(root, paths); kept != nil {
			return kept
		}
	}
	var out []string
	for _, p := range paths {
		top := p
		if i := strings.Index(p, string(filepath.Separator)); i >= 0 {
			top = p[:i]
		}
		switch top {
		case "node_modules", ".venv", "venv", "__pycache__", "target", "dist", "build":
			continue
		}
		out = append(out, p)
	}
	return out
}

func checkIgnore(root string, paths []string) []string {
	if len(paths) == 0 {
		return paths
	}
	cmd := exec.Command("git", "-C", root, "check-ignore", "--stdin")
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\n"))
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return nil // git itself broken — caller falls back
		}
	}
	ignored := map[string]bool{}
	for _, l := range strings.Split(out.String(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			// check-ignore C-quotes special paths ("a\"b"); unquote so
			// weird filenames actually match and stay filtered.
			if len(l) >= 2 && l[0] == '"' {
				if uq, err := strconv.Unquote(l); err == nil {
					l = uq
				}
			}
			ignored[l] = true
		}
	}
	var kept []string
	for _, p := range paths {
		if !ignored[p] {
			kept = append(kept, p)
		}
	}
	return kept
}

// fuzzyScore subsequence-matches query against target (case-insensitive).
// Higher is better: basename matches outrank directory matches, early and
// contiguous matches outrank scattered ones. Returns matched rune indices.
func fuzzyScore(query, target string) (int, []int) {
	q := []rune(strings.ToLower(query))
	t := []rune(strings.ToLower(target))
	if len(q) == 0 {
		return 0, nil
	}
	var idx []int
	ti := 0
	for _, qc := range q {
		found := false
		for ti < len(t) {
			if t[ti] == qc {
				idx = append(idx, ti)
				ti++
				found = true
				break
			}
			ti++
		}
		if !found {
			return -1, nil
		}
	}
	score := 1000 - idx[0]*4
	gaps := 0
	for i := 1; i < len(idx); i++ {
		gaps += idx[i] - idx[i-1] - 1
	}
	score -= gaps * 6
	// Bonus when the match starts at a path boundary or in the basename.
	lastSlash := strings.LastIndex(target, "/")
	if idx[0] == 0 || (lastSlash >= 0 && idx[0] == lastSlash+1) {
		score += 60
	}
	for _, ix := range idx {
		if ix > lastSlash {
			score += 2
		}
	}
	return score, idx
}

// highlight bolds matched chars (fg, bold) against muted unmatched text —
// the one extra bold case the spec allows beyond §1.4's three. Runs are
// batched into one style.Render per contiguous run instead of per rune,
// which cuts SGR sequences by an order of magnitude on typical paths.
func highlight(path string, idx []int) string {
	path = ansi.Strip(path)
	hit := map[int]bool{}
	for _, i := range idx {
		hit[i] = true
	}
	bold := lipgloss.NewStyle().Bold(true)
	mut := lipgloss.NewStyle().Foreground(fgMuted)
	var b strings.Builder
	var run strings.Builder
	runHit := false
	flush := func() {
		if run.Len() == 0 {
			return
		}
		if runHit {
			b.WriteString(bold.Render(run.String()))
		} else {
			b.WriteString(mut.Render(run.String()))
		}
		run.Reset()
	}
	ri := -1 // rune index: fuzzyScore counts runes, range yields bytes
	for _, r := range path {
		ri++
		h := hit[ri]
		if run.Len() > 0 && h != runHit {
			flush()
		}
		runHit = h
		run.WriteRune(r)
	}
	flush()
	return b.String()
}

// highlightSelected bolds matched chars on the accentSelect background —
// the selected @-row keeps its match spans instead of falling back to a
// plain truncMiddle path. Each run carries the background itself: wrapping
// an already-styled string in a Background Render would let the inner
// resets clear the fill mid-row, so the fill is composed per run here.
func highlightSelected(path string, idx []int) string {
	path = ansi.Strip(path)
	hit := map[int]bool{}
	for _, i := range idx {
		hit[i] = true
	}
	bold := lipgloss.NewStyle().Foreground(fgOnSelect).Bold(true).Background(accentSelect)
	mut := lipgloss.NewStyle().Foreground(fgOnSelect).Background(accentSelect)
	var b strings.Builder
	var run strings.Builder
	runHit := false
	flush := func() {
		if run.Len() == 0 {
			return
		}
		if runHit {
			b.WriteString(bold.Render(run.String()))
		} else {
			b.WriteString(mut.Render(run.String()))
		}
		run.Reset()
	}
	ri := -1 // rune index: fuzzyScore counts runes, range yields bytes
	for _, r := range path {
		ri++
		h := hit[ri]
		if run.Len() > 0 && h != runHit {
			flush()
		}
		runHit = h
		run.WriteRune(r)
	}
	flush()
	return b.String()
}

// atMemo caches the last (files-identity, query) → rows result so
// repeated refreshes (cursor blinks, redraws) with an unchanged query
// don't re-run fuzzyScore over the whole file list. The key is an FNV
// hash of the joined paths plus the query — a bare length collides
// across different file lists and would serve stale rows.
var (
	atMemoKey  uint64
	atMemoHave bool
	atMemoRows []atRow
)

func matchFiles(files []string, query string) []atRow {
	h := fnv.New64a()
	for _, f := range files {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	h.Write([]byte(query))
	if key := h.Sum64(); atMemoHave && key == atMemoKey {
		return atMemoRows
	}
	type scored struct {
		score int
		idx   []int
		path  string
	}
	var ss []scored
	for _, f := range files {
		if s, ix := fuzzyScore(query, f); s >= 0 {
			ss = append(ss, scored{s, ix, f})
		}
	}
	sort.Slice(ss, func(i, j int) bool {
		if ss[i].score != ss[j].score {
			return ss[i].score > ss[j].score
		}
		return ss[i].path < ss[j].path
	})
	if len(ss) > 9 {
		ss = ss[:9]
	}
	out := make([]atRow, 0, len(ss))
	for _, s := range ss {
		out = append(out, atRow{path: s.path, rendered: highlight(s.path, s.idx), idx: s.idx})
	}
	atMemoKey, atMemoHave, atMemoRows = h.Sum64(), true, out
	return out
}

// atQuery extracts the @-query: text after the last "@" that follows
// start-of-line or whitespace. Reports whether one is active.
func activeAtQuery(input string) (string, bool) {
	at := strings.LastIndex(input, "@")
	if at < 0 {
		return "", false
	}
	if at > 0 && input[at-1] != ' ' && input[at-1] != '\t' && input[at-1] != '\n' {
		return "", false
	}
	q := input[at+1:]
	if strings.ContainsAny(q, " \t\n") {
		return "", false
	}
	return q, true
}
