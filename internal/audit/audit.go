// Package audit — append-only tool decision trail (P1-D).
//
// The audit log records what tool ran under which policy decision without
// ever persisting raw tool arguments: callers pass only a hash of the
// (redacted) args plus a short redacted detail string. This is enforced by
// type — AuditEvent has no map[string]any raw-args field, so there is no
// slot to put raw args in.
//
// Storage mirrors internal/session conventions: <dir>/audit.jsonl, one JSON
// object per line, flush + fsync per append, dir 0700 / file 0600 with
// best-effort heal of pre-existing perms on Open.
package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"tilde/internal/tools"
)

// AuditEvent is one audit line. TS is set to time.Now().UTC() by Append
// when zero. Tool names the tool, Decision records the policy outcome
// (e.g. allow / deny / ask). RedactedArgsHash is a caller-computed hash
// (e.g. hex sha256) of the redacted args — never the args themselves.
// Detail is a short human-readable, already-redacted note.
type AuditEvent struct {
	TS               time.Time `json:"ts"`
	Tool             string    `json:"tool"`
	Decision         string    `json:"decision"`
	RedactedArgsHash string    `json:"redacted_args_hash"`
	Detail           string    `json:"detail"`
}

// AuditLog is an open audit file. Append is safe for concurrent use.
type AuditLog struct {
	Path string
	f    *os.File
	w    *bufio.Writer
	mu   sync.Mutex
}

// Open creates dir (0700) and opens dir/audit.jsonl append-only (0600),
// healing perms on pre-existing dir/file like session.Open does.
func Open(dir string) (*AuditLog, error) {
	if dir == "" {
		return nil, fmt.Errorf("audit: empty dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("audit: cannot create %s: %w", dir, err)
	}
	_ = os.Chmod(dir, 0o700) // tighten pre-existing dirs; best effort
	path := filepath.Join(dir, "audit.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: cannot open %s: %w", path, err)
	}
	_ = os.Chmod(path, 0o600) // heal pre-existing perms; best effort
	return &AuditLog{Path: path, f: f, w: bufio.NewWriter(f)}, nil
}

// Append writes one event (never rewrites in place). Flush + fsync per
// event: audit volume is low, crash-safety first.
func (l *AuditLog) Append(event AuditEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if event.TS.IsZero() {
		event.TS = time.Now().UTC()
	}
	b, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("audit: marshal event: %w", err)
	}
	clean, _ := tools.Scrub(string(b))
	if _, err := l.w.Write(append([]byte(clean), '\n')); err != nil {
		return fmt.Errorf("audit: write event: %w", err)
	}
	if err := l.w.Flush(); err != nil {
		return fmt.Errorf("audit: flush event: %w", err)
	}
	if err := l.f.Sync(); err != nil {
		return fmt.Errorf("audit: fsync event: %w", err)
	}
	return nil
}

// Close flushes and closes, reporting a flush failure instead of hiding it.
func (l *AuditLog) Close() error {
	if err := l.w.Flush(); err != nil {
		_ = l.f.Close()
		return fmt.Errorf("audit: flush on close: %w", err)
	}
	return l.f.Close()
}
