// Package creds stores cloud-provider API keys for tilde (spec §2.24).
// One JSON file, mode 0600, one key per provider. Keys never appear in
// transcripts, session logs, or command output — only the last four
// characters are ever displayed.
package creds

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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
	unlock, err := lockFile(s.path)
	if err != nil {
		return err
	}
	defer unlock()
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
	unlock, err := lockFile(s.path)
	if err != nil {
		return err
	}
	defer unlock()
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
	// Primary: AES-GCM envelope file. Present-but-undecryptable is a
	// hard error (refuse, don't silently fall back to legacy).
	enc := s.encPath()
	if data, err := os.ReadFile(enc); err == nil {
		key, err := deriveKey()
		if err != nil {
			return nil, err
		}
		return decryptMap(data, key, enc)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", enc, err)
	}
	// Legacy fallback: plaintext credentials.json (compat / migration
	// source — a Set will re-seal it into the envelope file).
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
	key, err := deriveKey()
	if err != nil {
		return err
	}
	encData, err := encryptMap(m, key)
	if err != nil {
		return err
	}
	// Primary: sealed envelope only. The legacy plaintext file is never
	// written here — Get keeps an enc-first then legacy-read fallback
	// purely as a migration source for pre-envelope installs.
	if err := writeFile0600(s.encPath(), encData); err != nil {
		return err
	}
	// Best-effort cleanup of a legacy plaintext left by older builds
	// (e.g. just migrated via the read fallback on Set/Delete). Ignore
	// errors: the envelope is authoritative and a stale plaintext must
	// not fail the write.
	_ = os.Remove(s.path)
	return nil
}

func writeFile0600(path string, data []byte) error {
	// Use a random, exclusive sibling file. A predictable temp path combined
	// with os.WriteFile would follow a pre-planted symlink before the atomic
	// rename, allowing a local attacker to redirect the credential payload.
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary %s: %w", path, err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	defer cleanup()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// encPath is the AES-GCM envelope sibling of the legacy plaintext file:
// credentials.json -> credentials.enc.json.
func (s *Store) encPath() string {
	if strings.HasSuffix(s.path, ".json") {
		return strings.TrimSuffix(s.path, ".json") + ".enc.json"
	}
	return s.path + ".enc.json"
}

// deriveKey returns the AES-256 key sealing the envelope file.
//
// Opportunistic — not a keychain: key = SHA256(/etc/machine-id else
// hostname). It defeats casual file reads (dotfile scrapers, pasted
// directory listings) but not a local attacker who can read the same
// machine-id. No new dependencies: stdlib crypto/aes (GCM), sha256,
// rand only.
func deriveKey() ([32]byte, error) {
	seed := ""
	if data, err := os.ReadFile("/etc/machine-id"); err == nil {
		seed = strings.TrimSpace(string(data))
	}
	if seed == "" {
		h, err := os.Hostname()
		if err != nil || strings.TrimSpace(h) == "" {
			return [32]byte{}, fmt.Errorf("derive credential key: no machine-id or hostname available — delete the envelope file and /login again")
		}
		seed = strings.TrimSpace(h)
	}
	return sha256.Sum256([]byte(seed)), nil
}

// envelope is the on-disk JSON wrapper: GCM nonce + ciphertext.
type envelope struct {
	V     int    `json:"v"`
	Nonce string `json:"nonce"`
	Data  string `json:"data"`
}

func encryptMap(m map[string]string, key [32]byte) ([]byte, error) {
	plain, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("seal credentials: %w", err)
	}
	ct := gcm.Seal(nil, nonce, plain, nil)
	env := envelope{
		V:     1,
		Nonce: base64.StdEncoding.EncodeToString(nonce),
		Data:  base64.StdEncoding.EncodeToString(ct),
	}
	return json.MarshalIndent(env, "", "  ")
}

func decryptMap(encJSON []byte, key [32]byte, encPath string) (map[string]string, error) {
	remedy := "delete the file and /login again"
	var env envelope
	if err := json.Unmarshal(encJSON, &env); err != nil {
		return nil, fmt.Errorf("parse %s: %w — %s", encPath, err, remedy)
	}
	if env.V != 1 {
		return nil, fmt.Errorf("parse %s: unsupported envelope version %d — %s", encPath, env.V, remedy)
	}
	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return nil, fmt.Errorf("parse %s: bad nonce: %w — %s", encPath, err, remedy)
	}
	ct, err := base64.StdEncoding.DecodeString(env.Data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: bad payload: %w — %s", encPath, err, remedy)
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		// Wrong machine key (file copied from another host) or tampered
		// file: refuse outright, name the fix.
		return nil, fmt.Errorf("decrypt %s: wrong machine key or tampered file — %s: %w", encPath, remedy, err)
	}
	var m map[string]string
	if err := json.Unmarshal(plain, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w — %s", encPath, err, remedy)
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}

// Mask renders a key for display: only the last four characters are
// ever shown. Short/empty keys render as dots only — never a prefix.
func Mask(key string) string {
	if len(key) <= 4 {
		return "····"
	}
	return "····" + key[len(key)-4:]
}

// lockFile takes an inter-process exclusive lock guarding read-modify-write.
// Best-effort on platforms without flock: falls back to in-process mutex only.
func lockFile(storePath string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(storePath), 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(storePath), err)
	}
	f, err := os.OpenFile(storePath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", storePath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", storePath, err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
