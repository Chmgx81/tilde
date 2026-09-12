// Package session — JSONL append-only transcript (crash-safe, replayable).
package session

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"tilde/internal/tools"
)

// Entry is one log line.
type Entry struct {
	TS   time.Time      `json:"ts"`
	Type string         `json:"type"` // user | assistant | tool_call | tool_result | system
	Data map[string]any `json:"data"`
}

// Log is an open session file. Append is safe for concurrent use — the
// agent goroutine and UI event handlers both write here.
type Log struct {
	Path string
	f    *os.File
	w    *bufio.Writer
	mu   sync.Mutex
}

// Open creates ~/.tilde/sessions/<id>.jsonl (or reopens for append).
// Logs hold full tool output (file contents included) — owner-only,
// mode 0600, like an SSH private key's neighborhood.
func Open(id string) (*Log, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("session: cannot find home dir: %w", err)
	}
	dir := filepath.Join(home, ".tilde", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("session: cannot create %s: %w", dir, err)
	}
	_ = os.Chmod(dir, 0o700) // tighten pre-existing dirs; best effort
	path := filepath.Join(dir, id+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("session: cannot open %s: %w", path, err)
	}
	_ = os.Chmod(path, 0o600) // heal pre-existing perms; best effort
	return &Log{Path: path, f: f, w: bufio.NewWriter(f)}, nil
}

// Append writes one entry (never rewrites in place). Flush + fsync per
// entry: session volume is low, crash-safety first (spec §2.23).
// A zero-value Log (nil writer/file) errors instead of panicking.
func (l *Log) Append(typ string, data map[string]any) error {
	if l == nil || l.w == nil || l.f == nil {
		return fmt.Errorf("session: log not open — cannot append %q", typ)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := Entry{TS: time.Now().UTC(), Type: typ, Data: data}
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("session: marshal entry: %w", err)
	}
	clean, _ := tools.Scrub(string(b))
	if _, err := l.w.Write(append([]byte(clean), '\n')); err != nil {
		return fmt.Errorf("session: write entry: %w", err)
	}
	if err := l.w.Flush(); err != nil {
		return fmt.Errorf("session: flush entry: %w", err)
	}
	if err := l.f.Sync(); err != nil {
		return fmt.Errorf("session: fsync entry: %w", err)
	}
	return nil
}

// Close flushes and closes, reporting a flush failure instead of hiding it.
// A zero-value Log errors instead of panicking.
func (l *Log) Close() error {
	if l == nil || l.w == nil || l.f == nil {
		return fmt.Errorf("session: log not open — nothing to close")
	}
	if err := l.w.Flush(); err != nil {
		_ = l.f.Close()
		return fmt.Errorf("session: flush on close: %w", err)
	}
	return l.f.Close()
}

// NewID generates a session id like session_a1b2c3d4 (crypto-random 16 bytes hex).
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return fmt.Sprintf("session_%x", b[:])
	}
	return fmt.Sprintf("session_%x", time.Now().UnixNano())
}

// ValidID reports whether id is safe to name a session file: non-empty,
// at most 64 characters, and only letters, digits, '_' or '-'. Separators
// and dots are rejected, so an id can never traverse out of the sessions
// directory — traversal is structurally impossible, not merely unlikely.
func ValidID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if r == '_' || r == '-' || ('a' <= r && r <= 'z') ||
			('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') {
			continue
		}
		return false
	}
	return true
}
