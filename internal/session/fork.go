// Package session — P7-B session fork additions (fork.go).
//
// Fork branches a transcript at an earlier point so a session behaves like
// a version-controlled document (spec §6: undo, checkpoints, session
// forking). Pure logic: no stdout, no prompting — the caller owns all user
// output.
package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Fork copies dir/<srcID>.jsonl line-by-line up to and including the last
// entry with TS <= throughTS, then appends one
// {"type":"fork","data":{"from":srcID,"at":throughTS}} marker line. An empty
// throughTS forks the whole file (branch-from-tip). The branch is written
// under a NEW id (NewID); the source file is never modified.
//
// Durability mirrors Open: dir is MkdirAll'd 0700 (pre-existing dirs healed
// best-effort), the branch is written to a temp file in the same dir with
// mode 0600 and atomically renamed — a failure never leaves a half-written
// fork behind.
//
// Fail loud: unknown srcID, an unparseable throughTS (want RFC3339),
// corrupt source lines (error names the physical line number), and an empty
// result (throughTS older than the first entry — the error says to use a
// later cutoff).
func Fork(dir, srcID, throughTS string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("session: empty dir")
	}
	// Validate before building any path: an id with separators or dots
	// would escape the sessions directory (../../x.jsonl). Rejecting it
	// here protects every caller, not just the CLI.
	if !ValidID(srcID) {
		return "", fmt.Errorf("session: bad session id %q: letters, digits, _ and - only (max 64)", srcID)
	}
	var cutoff time.Time
	if throughTS != "" {
		var err error
		cutoff, err = time.Parse(time.RFC3339, throughTS)
		if err != nil {
			return "", fmt.Errorf("session: bad cutoff %q: want RFC3339: %w", throughTS, err)
		}
	}

	src := filepath.Join(dir, srcID+".jsonl")
	f, err := os.Open(src)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("session: unknown session %q", srcID)
		}
		return "", fmt.Errorf("session: open %s: %w", src, err)
	}
	type rawLine struct {
		no   int // physical line number (1-based), for corrupt-line errors
		text string
	}
	var lines []rawLine
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 16<<20) // entries hold full tool output; allow wide lines
	no := 0
	for scanner.Scan() {
		no++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue // tolerate trailing/blank lines; they carry no entry
		}
		lines = append(lines, rawLine{no: no, text: scanner.Text()})
	}
	scanErr := scanner.Err()
	_ = f.Close()
	if scanErr != nil {
		return "", fmt.Errorf("session: read %s: %w", src, scanErr)
	}

	// Validate every line (fail loud on any corruption) and find the last
	// entry within the cutoff. Copy is a byte-identical prefix — never
	// re-marshalled — so the branch replays exactly like the source.
	last := -1
	var firstTS time.Time
	haveFirst := false
	for i, ln := range lines {
		var e Entry
		if err := json.Unmarshal([]byte(ln.text), &e); err != nil {
			return "", fmt.Errorf("session: corrupt entry in %s.jsonl at line %d: %w", srcID, ln.no, err)
		}
		if !haveFirst {
			firstTS, haveFirst = e.TS, true
		}
		if throughTS == "" || !e.TS.After(cutoff) {
			last = i
		}
	}
	if last < 0 {
		if !haveFirst {
			return "", fmt.Errorf("session: fork %q: source has no entries", srcID)
		}
		return "", fmt.Errorf("session: fork %q: cutoff %q is older than first entry (%s); use a later cutoff",
			srcID, throughTS, firstTS.UTC().Format(time.RFC3339))
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("session: cannot create %s: %w", dir, err)
	}
	_ = os.Chmod(dir, 0o700) // tighten pre-existing dirs; best effort

	newID := NewID()
	dst := filepath.Join(dir, newID+".jsonl")
	for i := 0; i < 3; i++ {
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			break
		}
		newID = NewID() // collision (astronomically unlikely); retry
		dst = filepath.Join(dir, newID+".jsonl")
	}
	// Never rename over an existing session: if three ids still collided,
	// fail instead of clobbering whatever is there.
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("session: fork %q: could not allocate a unique branch id after 3 attempts — retry", srcID)
	}

	tmp, err := os.CreateTemp(dir, ".fork-*.tmp")
	if err != nil {
		return "", fmt.Errorf("session: fork %q: temp file: %w", srcID, err)
	}
	tmpName := tmp.Name()
	_ = os.Chmod(tmpName, 0o600)
	failed := true
	defer func() {
		if failed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	w := bufio.NewWriter(tmp)
	for _, ln := range lines[:last+1] {
		if _, err := w.WriteString(ln.text + "\n"); err != nil {
			return "", fmt.Errorf("session: fork %q: write branch: %w", srcID, err)
		}
	}
	marker, err := json.Marshal(map[string]any{
		"type": "fork",
		"data": map[string]any{"from": srcID, "at": throughTS},
	})
	if err != nil {
		return "", fmt.Errorf("session: fork %q: marshal marker: %w", srcID, err)
	}
	if _, err := w.Write(append(marker, '\n')); err != nil {
		return "", fmt.Errorf("session: fork %q: write marker: %w", srcID, err)
	}
	if err := w.Flush(); err != nil {
		return "", fmt.Errorf("session: fork %q: flush branch: %w", srcID, err)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("session: fork %q: fsync branch: %w", srcID, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("session: fork %q: close branch: %w", srcID, err)
	}
	_ = os.Chmod(tmpName, 0o600) // heal umask effects; best effort
	if err := os.Rename(tmpName, dst); err != nil {
		return "", fmt.Errorf("session: fork %q: publish branch: %w", srcID, err)
	}
	failed = false
	return newID, nil
}
