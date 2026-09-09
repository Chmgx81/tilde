// Package audit — P6-D retention/deletion additions (trim.go).
//
// Trim rewrites an audit.jsonl file keeping only recent events so the owner
// can wire `tilde prune` on top. Pure logic: no stdout, no confirm, no
// prompting — the caller owns --yes gating and all user output.
package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Trim rewrites path keeping events with TS at/after (now - olderThan)
// (inclusive, identical semantics to Filter with Since set to the cutoff).
// Corrupt lines reuse the ReadAll tolerance: they are dropped and counted
// in dropped, so dropped = age-expired events + corrupt lines.
//
// The rewrite is atomic (tmp file in the same directory + rename) with
// mode 0600, mirroring Open. When nothing would be dropped the file is
// left untouched (mtime and perms preserved).
//
// Guards: an empty path is a caller bug and fails loud; a missing file is
// a cron-friendly no-op returning (0, 0, nil), never an error.
func Trim(path string, olderThan time.Duration) (kept, dropped int, err error) {
	if path == "" {
		return 0, 0, fmt.Errorf("audit: empty path")
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil // cron-friendly no-op
		}
		return 0, 0, fmt.Errorf("audit: stat %s: %w", path, err)
	}

	events, skipped, err := ReadAllWithSkipped(path)
	if err != nil {
		return 0, 0, err
	}
	cutoff := time.Now().UTC().Add(-olderThan)
	keep := Filter(events, FilterOpts{Since: cutoff})
	kept = len(keep)
	dropped = len(events) - kept + skipped
	if dropped == 0 {
		return kept, 0, nil // nothing to do; don't churn mtime
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".audit-*.tmp")
	if err != nil {
		return 0, 0, fmt.Errorf("audit: create tmp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup on failure; success path renames away.
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return 0, 0, fmt.Errorf("audit: chmod tmp: %w", err)
	}
	if _, err = tmp.WriteString(RenderJSON(keep)); err != nil {
		_ = tmp.Close()
		return 0, 0, fmt.Errorf("audit: write tmp: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return 0, 0, fmt.Errorf("audit: fsync tmp: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return 0, 0, fmt.Errorf("audit: close tmp: %w", err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return 0, 0, fmt.Errorf("audit: rename tmp: %w", err)
	}
	_ = os.Chmod(path, 0o600) // heal perms post-rename; best effort
	return kept, dropped, nil
}
