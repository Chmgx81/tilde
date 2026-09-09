// Package session — P6-D retention/deletion additions (prune.go).
//
// Prune deletes aged session transcripts (*.jsonl by file mtime) so the
// owner can wire `tilde prune` on top. Pure logic: no stdout, no confirm,
// no prompting — the caller owns --yes gating and all user output.
package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Prune deletes *.jsonl files in dir whose mtime is strictly older than
// (now - olderThan). The age boundary is inclusive-keep: a file with mtime
// exactly at the cutoff (age == olderThan) is kept, matching the
// Filter.Since "TS at/after Since" semantics on the audit side.
//
// keepMin protects the newest keepMin session files (by mtime, basename
// tie-break) from deletion regardless of age, so a retention run never
// empties the transcript history below that floor. keepMin <= 0 disables
// the floor.
//
// Guards: an empty dir is a caller bug and fails loud; a nonexistent dir
// is a cron-friendly no-op returning (nil, nil), never an error. Only
// regular *.jsonl files are candidates — dotfiles (".*"), non-.jsonl
// names, directories, and symlinks are never touched (the clean marker
// convention depends on this).
//
// Returns the sorted basenames of deleted files; the caller prints the
// count receipt (e.g. "pruned N session(s): ...").
func Prune(dir string, olderThan time.Duration, keepMin int) ([]string, error) {
	if dir == "" {
		return nil, fmt.Errorf("session: empty dir")
	}
	fi, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // cron-friendly no-op
		}
		return nil, fmt.Errorf("session: stat %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("session: not a directory: %s", dir)
	}
	if keepMin < 0 {
		keepMin = 0
	}

	type candidate struct {
		name  string // basename
		mtime time.Time
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("session: read %s: %w", dir, err)
	}
	var cands []candidate
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // dotfiles: clean markers etc.
		}
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("session: stat %s: %w", filepath.Join(dir, name), err)
		}
		if !info.Mode().IsRegular() {
			continue // dirs, symlinks, sockets: never touch
		}
		cands = append(cands, candidate{name: name, mtime: info.ModTime()})
	}

	// Newest first; basename tie-break for determinism.
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].mtime.Equal(cands[j].mtime) {
			return cands[i].name < cands[j].name
		}
		return cands[i].mtime.After(cands[j].mtime)
	})
	protected := make(map[string]bool, keepMin)
	for i := 0; i < keepMin && i < len(cands); i++ {
		protected[cands[i].name] = true
	}

	cutoff := time.Now().Add(-olderThan)
	var deleted []string
	for _, c := range cands {
		if protected[c.name] {
			continue
		}
		if !c.mtime.Before(cutoff) {
			continue // inside window (or exactly on it): keep
		}
		if err := os.Remove(filepath.Join(dir, c.name)); err != nil {
			if os.IsNotExist(err) {
				continue // raced away; treat as gone
			}
			return deleted, fmt.Errorf("session: remove %s: %w", c.name, err)
		}
		deleted = append(deleted, c.name)
	}
	sort.Strings(deleted)
	return deleted, nil
}
