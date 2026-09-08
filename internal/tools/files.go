package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Read ceilings (Phase 3): all three, always together. Miss any one and
// some shape of file silently burns the whole context budget.
const (
	maxReadLines = 2000             // line window — an ordinary long file
	maxReadBytes = 128 * 1024       // byte cap — lockfiles, logs, dumps
	maxLineChars = 2000             // per-line clamp — one minified line
	maxReadFile  = 64 * 1024 * 1024 // refuse outright past this (hostile shape)
)

// --- read_file ---

type ReadFile struct {
	Root string
	Seen *SeenMap // nil → skip read-tracking (unit tests)

	mu   sync.Mutex
	last readCache // unchanged-read dedup (self-expiring)
}

type readCache struct {
	path       string
	offset     int
	limit      int
	mtimeUnix  int64
	size       int64
	stubServed bool // stub expires on first use: never loop the model forever
}

func (t *ReadFile) Name() string { return "read_file" }
func (t *ReadFile) Description() string {
	return "Read a file's content. Use offset/limit for pagination on long files."
}
func (t *ReadFile) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string", "description": "Repo-relative or absolute path"},
			"offset": map[string]any{"type": "integer", "description": "1-based start line, default 1"},
			"limit":  map[string]any{"type": "integer", "description": "Max lines, default 200"},
		}, "required": []string{"path"}}
}

func (t *ReadFile) Exec(_ context.Context, args map[string]any) (string, error) {
	p, err := strArg(args, "path")
	if err != nil {
		return "", err
	}
	full, err := contain(t.Root, p)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file %q not found: check the path with glob first, then retry with a corrected path", p)
		}
		return "", fmt.Errorf("cannot stat %q: %v — check permissions and retry", p, err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("%q is a directory, not a file: use glob with a pattern like %q/* to list it", p, strings.TrimSuffix(p, "/"))
	}
	if st.Size() > maxReadFile {
		return "", fmt.Errorf("file %q is %d bytes (over the %d-byte hard cap): do not read it whole — use shell_command with sed/head to sample ranges instead", p, st.Size(), maxReadFile)
	}
	offset := optInt(args, "offset", 1)
	limit := optInt(args, "limit", 200)
	if offset < 1 {
		return "", fmt.Errorf("offset must be >= 1, got %d: resend with offset >= 1", offset)
	}
	if limit < 1 || limit > maxReadLines {
		return "", fmt.Errorf("limit must be 1..%d, got %d: resend with a limit in range", maxReadLines, limit)
	}

	// Unchanged-read dedup: same file state + same window → one-line stub.
	// The stub expires on first use so a stale pointer can never loop forever.
	mtime := st.ModTime().UnixNano()
	t.mu.Lock()
	same := t.last.path == full && t.last.offset == offset && t.last.limit == limit &&
		t.last.mtimeUnix == mtime && t.last.size == st.Size()
	expired := t.last.stubServed
	if same && !expired {
		t.last.stubServed = true
		t.mu.Unlock()
		return fmt.Sprintf("[unchanged since your last read of %q lines %d..%d — content identical, not re-sent. Re-read with a different offset/limit if you need it again.]",
			p, offset, offset+limit-1), nil
	}
	t.mu.Unlock()

	data, err := readFileNoFollow(t.Root, p)
	if err != nil {
		return "", fmt.Errorf("cannot read %q: %v — check permissions and retry", p, err)
	}
	lines := strings.Split(string(data), "\n")
	if offset > len(lines) {
		return "", fmt.Errorf("offset %d is past EOF (%d lines in %q): the file is shorter than you assume — retry with offset <= %d", offset, len(lines), p, len(lines))
	}
	end := offset - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	clamped := 0
	bytes := 0
	cutByBytes := false
	resumeAt := end + 1 // exact resume offset if the byte cap cuts us short
	for i := offset - 1; i < end; i++ {
		line := lines[i]
		if len(line) > maxLineChars {
			line = line[:maxLineChars] + fmt.Sprintf("… [line %d clamped: %d more chars — narrow the read or use shell to sample]", i+1, len(lines[i])-maxLineChars)
			clamped++
		}
		row := fmt.Sprintf("%d: %s\n", i+1, line)
		if bytes+len(row) > maxReadBytes {
			cutByBytes = true
			resumeAt = i + 1 // line number to continue from — no pagination arithmetic
			break
		}
		b.WriteString(row)
		bytes += len(row)
	}
	if clamped > 0 {
		fmt.Fprintf(&b, "[note: %d long line(s) clamped at %d chars]\n", clamped, maxLineChars)
	}
	switch {
	case cutByBytes:
		fmt.Fprintf(&b, "[truncated at the %d-byte cap with %d more lines unread — re-read %q with offset=%d to continue]\n",
			maxReadBytes, len(lines)-(resumeAt-1), p, resumeAt)
	case end < len(lines):
		fmt.Fprintf(&b, "[truncated: %d more lines — re-read with offset=%d to continue]\n", len(lines)-end, end+1)
	}

	t.mu.Lock()
	t.last = readCache{path: full, offset: offset, limit: limit, mtimeUnix: mtime, size: st.Size()}
	t.mu.Unlock()
	if t.Seen != nil {
		t.Seen.Mark(p)
	}
	if note := AnnotateHighRisk(p); note != "" {
		fmt.Fprintf(&b, "\n[%s]\n", note)
	}
	return Fence(b.String()), nil
}

// --- write_file (mutating) ---

type WriteFile struct {
	Root string
	Seen *SeenMap
}

func (t *WriteFile) Name() string { return "write_file" }
func (t *WriteFile) Description() string {
	return "Create or overwrite a whole file. Prefer edit_file for targeted changes. Overwriting requires a prior read_file of the same path."
}
func (t *WriteFile) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string", "description": "Full new file content"},
		}, "required": []string{"path", "content"}}
}

func (t *WriteFile) Exec(_ context.Context, args map[string]any) (string, error) {
	p, err := strArg(args, "path")
	if err != nil {
		return "", err
	}
	c, err := strArg(args, "content")
	if err != nil {
		return "", err
	}
	full, err := contain(t.Root, p)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(full); statErr == nil {
		// Overwrite: the agent must have seen the current content first,
		// and what it saw must still be current (stale-read detection).
		if !t.Seen.Has(p) {
			return "", fmt.Errorf("refusing to overwrite %q: you have not read it in this session — read it with read_file first (any window counts), then retry", p)
		}
		if stale, msg := t.Seen.Check(p); stale {
			return "", fmt.Errorf("%s", msg)
		}
	}
	// The before-state for an overwrite diff must be captured BEFORE
	// the write below — after it, the old content is gone. Best
	// effort: a failed pre-read only degrades the receipt, never blocks.
	existed := true
	if _, serr := os.Stat(full); os.IsNotExist(serr) {
		existed = false
	}
	var before []string
	if existed {
		if old, rerr := readFileNoFollow(t.Root, p); rerr == nil {
			before = lineSplit(string(old))
		}
	}
	// No-follow open: a link planted after contain() must not redirect
	// the write outside the project (parent dirs via the same helper).
	if err := writeFileNoFollow(t.Root, p, []byte(c), 0o644); err != nil {
		return "", fmt.Errorf("cannot write %q: %v", p, err)
	}
	t.Seen.Mark(p) // what was just written is now the seen state
	rel, _ := filepath.Rel(t.Root, full)
	after := lineSplit(c)
	if !existed {
		// New file (spec §2.11): a `+N (new file: path)` stat plus the
		// raw body for the TUI's plain numbered renderer. Bounded —
		// an unbounded new file in the transcript is flooding by
		// another name.
		body := after
		notice := ""
		if len(body) > newFileCap {
			body = body[:newFileCap]
			notice = fmt.Sprintf("\n[new file truncated in transcript: %d more lines — read the file to review]", len(after)-newFileCap)
		}
		return fmt.Sprintf("+%d (new file: %s)\n%s%s", len(after), rel, strings.Join(body, "\n"), notice), nil
	}
	if before == nil {
		return fmt.Sprintf("+%d -0 (overwrote existing file; before-state unreadable)", len(after)), nil
	}
	// Overwrite renders as an edit diff (spec §2.11): Write names the
	// tool, but what the user needs is the change, not a verb debate.
	return SpanDiff(rel, before, after, 0, len(before), len(after), " (overwrote existing file)"), nil
}

// --- edit_file (mutating, targeted) ---

type EditFile struct {
	Root string
	Seen *SeenMap
}

func (t *EditFile) Name() string { return "edit_file" }
func (t *EditFile) Description() string {
	return "Replace the first occurrence of old_string with new_string. Requires a prior read_file of the same path."
}
func (t *EditFile) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"path":       map[string]any{"type": "string"},
			"old_string": map[string]any{"type": "string"},
			"new_string": map[string]any{"type": "string"},
		}, "required": []string{"path", "old_string", "new_string"}}
}

func (t *EditFile) Exec(_ context.Context, args map[string]any) (string, error) {
	p, err := strArg(args, "path")
	if err != nil {
		return "", err
	}
	if !t.Seen.Has(p) {
		return "", fmt.Errorf("refusing to edit %q: you have not read it in this session — read it with read_file first (any window counts), then retry with exact text", p)
	}
	if stale, msg := t.Seen.Check(p); stale {
		return "", fmt.Errorf("%s", msg)
	}
	oldS, err := strArg(args, "old_string")
	if err != nil {
		return "", err
	}
	newS, ok := args["new_string"]
	if !ok || newS == nil {
		return "", fmt.Errorf("missing required argument \"new_string\": pass it as a string (may be empty to delete)")
	}
	newStr, ok := newS.(string)
	if !ok {
		return "", fmt.Errorf("argument \"new_string\" must be a string, got %T: resend as a plain string", newS)
	}
	// Containment is re-checked inside the no-follow opens below; this
	// early check keeps the refusal error identical for outside paths.
	if _, err := contain(t.Root, p); err != nil {
		return "", err
	}
	data, err := readFileNoFollow(t.Root, p)
	if err != nil {
		return "", fmt.Errorf("cannot read %q before editing: %v — verify the path with glob first", p, err)
	}
	content := string(data)
	n := strings.Count(content, oldS)
	actualOld, actualN := oldS, n
	actualNote := ""
	if n == 0 {
		// Repair, don't reject: local models constantly botch indentation.
		// If the text matches uniquely ignoring per-line whitespace, take
		// it — and say so, so the model verifies rather than trusts.
		if span, ok := fuzzySpan(content, oldS); ok {
			actualOld = span
			actualN = 1
			actualNote = " (matched ignoring indentation — verify the result)"
		} else if stripped, ok := stripReadPrefixes(content, oldS); ok {
			// Measured failure (eval chain-read-first 0/5): weak models
			// echo the fenced read format's `N: ` line prefixes into
			// old_string. Take the stripped span only when the raw text
			// matches nothing and the stripped text matches exactly once.
			actualOld = stripped
			actualN = 1
			actualNote = " (dropped read-output line numbers — verify the result)"
		} else {
			return "", fmt.Errorf("old_string not found in %q: re-read the file with read_file to get exact text (whitespace matters; send file text only, without the `N: ` line-number prefixes from read output), then retry", p)
		}
	}
	if actualN > 1 {
		return "", fmt.Errorf("old_string matches %d times in %q: include more surrounding context to make it unique, then retry", actualN, p)
	}
	updated := strings.Replace(content, actualOld, newStr, 1)
	if err := writeFileNoFollow(t.Root, p, []byte(updated), 0o644); err != nil {
		return "", fmt.Errorf("cannot write %q: %v", p, err)
	}
	t.Seen.Mark(p) // what was just written is now the seen state
	// The transcript shows the change, not prose about it (spec §2.11):
	// a `+N -M` stat line plus one unified hunk. The model reads the
	// same payload in context — the diff IS the verification, so the
	// old "re-read to verify" nudge is retired.
	before := lineSplit(content)
	after := lineSplit(updated)
	oldBlock := lineSplit(actualOld)
	newBlock := lineSplit(newStr)
	start := spanStart(content, actualOld)
	if start < 0 {
		start = 0
	}
	return SpanDiff(p, before, after, start, len(oldBlock), len(newBlock), actualNote), nil
}

// stripReadPrefixes drops fenced-read `N: ` line prefixes from old when
// the raw text matches nothing and the stripped text matches exactly
// once. ok=false otherwise — legitimately prefixed file content (raw
// matches) never rewrites.
func stripReadPrefixes(content, old string) (span string, ok bool) {
	lines := strings.Split(old, "\n")
	stripped := make([]string, 0, len(lines))
	changed := false
	for _, ln := range lines {
		s := ln
		i := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i > 0 && i < len(s) && s[i] == ':' && i+1 < len(s) && s[i+1] == ' ' {
			s = s[i+2:]
			changed = true
		}
		stripped = append(stripped, s)
	}
	if !changed {
		return "", false
	}
	cand := strings.Join(stripped, "\n")
	if strings.Count(content, cand) != 1 {
		return "", false
	}
	return cand, true
}

// fuzzySpan finds old (multi-line) in content comparing whitespace-folded
// lines, returning the exact original span. ok=false unless exactly one
// folded match exists — ambiguity still refuses.
func fuzzySpan(content, old string) (span string, ok bool) {
	if strings.TrimSpace(old) == "" {
		return "", false // all-whitespace spans never fuzzy-match
	}
	var want []string
	for _, ln := range strings.Split(old, "\n") {
		want = append(want, foldLine(ln))
	}
	lines := strings.Split(content, "\n")
	// Byte offsets of each line start for span extraction.
	offs := make([]int, len(lines)+1)
	for i, ln := range lines {
		offs[i+1] = offs[i] + len(ln) + 1
	}
	var hits [][2]int
	for i := 0; i+len(want) <= len(lines); i++ {
		match := true
		for j, w := range want {
			if foldLine(lines[i+j]) != w {
				match = false
				break
			}
		}
		if match {
			hits = append(hits, [2]int{offs[i], offs[i+len(want)] - 1})
			if len(hits) > 1 {
				return "", false
			}
		}
	}
	if len(hits) != 1 {
		return "", false
	}
	return content[hits[0][0]:hits[0][1]], true
}

func foldLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
