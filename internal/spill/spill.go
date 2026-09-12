// Package spill — bounded tool output with an on-disk escape hatch.
//
// When a tool result would blow the inline context cap, the full text is
// written to a managed per-machine file and the inline result names its
// path. The model gets a bounded result; the operator (or a later
// `read_file`) can still retrieve everything. Files are 0600 under 0700
// dirs and pruned by age with `tilde prune --spill <age>`.
//
// This is a leaf package (stdlib only). The CALLER must scrub the text
// before handing it here — tools already own the secret patterns.
package spill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// seq makes spill filenames unique even within one nanosecond.
var seq atomic.Uint64

// Dir returns the managed spill directory: $TILDE_SPILL_DIR when set, else
// ~/.tilde/spill. Empty when neither resolves — spilling is then disabled
// and callers keep their own truncation note.
func Dir() string {
	if d := strings.TrimSpace(os.Getenv("TILDE_SPILL_DIR")); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".tilde", "spill")
}

// Save writes full to a new managed file and returns its path. full must
// already be scrubbed. Empty string means spilling is unavailable or the
// write failed — the caller then keeps a plain inline truncation.
func Save(tool, full string) string {
	dir := Dir()
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	name := fmt.Sprintf("%s-%d-%d.txt", sanitize(tool), time.Now().UnixNano(), seq.Add(1))
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ""
	}
	if _, err := f.WriteString(full); err != nil {
		f.Close()
		os.Remove(path)
		return ""
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return ""
	}
	return path
}

// Prune deletes spill files older than olderThan and returns how many were
// removed. Files, not dirs/symlinks, and a missing dir is a no-op.
func Prune(dir string, olderThan time.Duration) (int, error) {
	if dir == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := time.Now().Add(-olderThan)
	removed := 0
	for _, e := range entries {
		if e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// sanitize keeps a filename-safe tool id.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "tool"
	}
	if b.Len() > 40 {
		return b.String()[:40]
	}
	return b.String()
}
