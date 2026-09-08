// Package creds stores cloud-provider API keys for tilde (spec §2.24).
// One JSON file, mode 0600, one key per provider. Keys never appear in
// transcripts, session logs, or command output — only the last four
// characters are ever displayed.
package creds

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Store is the on-disk credential file. Safe for concurrent use; the
// TUI and the agent loop may both touch it.
type Store struct {
	mu   sync.Mutex
	path string
}

// New returns a store rooted at path (normally ~/.tilde/credentials.json).
func New(path string) *Store { return &Store{path: path} }

// DefaultPath is ~/.tilde/credentials.json for the current user.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".tilde", "credentials.json"), nil
}

// Get returns the stored key for providerID ("" when none). Nil-receiver
// safe: a nil store (creds disabled, unbound tests) reads as empty
// rather than panicking — the ladder then falls through to env.
func (s *Store) Get(providerID string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("no credential store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return "", err
	}
	return m[providerID], nil
}

// Set stores (or replaces) the key and enforces 0600 on the file —
// also after every write, so a pre-existing lax file is healed.
// Nil-receiver safe: reports the missing store instead of panicking.
func (s *Store) Set(providerID, key string) error {
	if s == nil {
		return fmt.Errorf("no credential store")
	}
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("refusing to store an empty key for %s", providerID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return err
	}
	m[providerID] = key
	return s.write(m)
}

// Delete removes the key (idempotent — deleting an absent key is fine,
// logout must not require a prior login). Nil-receiver safe.
func (s *Store) Delete(providerID string) error {
	if s == nil {
		return fmt.Errorf("no credential store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return err
	}
	delete(m, providerID)
	return s.write(m)
}

// Path exposes the file location for status lines. Nil-safe.
func (s *Store) Path() string {
	if s == nil {
		return "(no credential store)"
	}
	return s.path
}

func (s *Store) read() (map[string]string, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		// A corrupt credential file must not crash startup; report it —
		// the user decides whether to overwrite via /login.
		return nil, fmt.Errorf("parse %s: %w — delete the file and /login again", s.path, err)
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}

func (s *Store) write(m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(s.path), err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Mask renders a key for display: only the last four characters are
// ever shown. Short/empty keys render as dots only — never a prefix.
func Mask(key string) string {
	if len(key) <= 4 {
		return "····"
	}
	return "····" + key[len(key)-4:]
}
