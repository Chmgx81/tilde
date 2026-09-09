package creds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tmpStore(t *testing.T) *Store {
	t.Helper()
	return New(filepath.Join(t.TempDir(), ".tilde", "credentials.json"))
}

func TestRoundTrip(t *testing.T) {
	s := tmpStore(t)
	if err := s.Set("openai", "sk-test-1234"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	k, err := s.Get("openai")
	if err != nil || k != "sk-test-1234" {
		t.Fatalf("Get = %q, %v", k, err)
	}
	// Overwrite.
	if err := s.Set("openai", "sk-test-5678"); err != nil {
		t.Fatalf("Set overwrite: %v", err)
	}
	k, _ = s.Get("openai")
	if k != "sk-test-5678" {
		t.Fatalf("overwrite Get = %q", k)
	}
}

func TestFileMode0600(t *testing.T) {
	s := tmpStore(t)
	if err := s.Set("openai", "k"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	fi, err := os.Stat(s.encPath())
	if err != nil {
		t.Fatalf("Stat %s: %v", s.encPath(), err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("credential file %s mode = %o, want 600", s.encPath(), fi.Mode().Perm())
	}
}

func TestWriteDoesNotUsePredictableTempPath(t *testing.T) {
	s := tmpStore(t)
	tmpPath := s.encPath() + ".tmp"
	outside := filepath.Join(t.TempDir(), "redirected")
	if err := os.MkdirAll(filepath.Dir(tmpPath), 0o700); err != nil {
		t.Fatalf("create credential directory: %v", err)
	}
	if err := os.Symlink(outside, tmpPath); err != nil {
		t.Fatalf("create temp-path symlink: %v", err)
	}
	if err := s.Set("openai", "k"); err != nil {
		t.Fatalf("Set with planted temp symlink: %v", err)
	}
	if _, err := os.Lstat(tmpPath); err != nil {
		t.Fatalf("predictable temp symlink disappeared unexpectedly: %v", err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("credential write followed planted temp symlink; outside=%v", err)
	}
}

func TestDeleteIdempotent(t *testing.T) {
	s := tmpStore(t)
	if err := s.Delete("openai"); err != nil {
		t.Fatalf("delete absent must be a no-op: %v", err)
	}
	if err := s.Set("openai", "k"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Delete("openai"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if k, _ := s.Get("openai"); k != "" {
		t.Fatalf("deleted key still reads: %q", k)
	}
}

func TestRefuseEmptyKey(t *testing.T) {
	s := tmpStore(t)
	if err := s.Set("openai", "   "); err == nil {
		t.Fatal("empty key must be refused")
	}
}

func TestCorruptFileErrors(t *testing.T) {
	s := tmpStore(t)
	if err := s.Set("openai", "k"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Corrupt the envelope primary: must surface an error, not silent empty.
	if err := os.WriteFile(s.encPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if _, err := s.Get("openai"); err == nil {
		t.Fatal("corrupt envelope must surface an error, not silent empty")
	}
	// Corrupt legacy with no envelope present: must also surface an error.
	os.Remove(s.encPath())
	if err := os.WriteFile(s.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if _, err := s.Get("openai"); err == nil {
		t.Fatal("corrupt file must surface an error, not silent empty")
	}
}

func TestEncRoundTrip(t *testing.T) {
	s := tmpStore(t)
	if err := s.Set("openai", "sk-test-enc-1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := os.Stat(s.encPath()); err != nil {
		t.Fatalf("envelope file missing: %v", err)
	}
	k, err := s.Get("openai")
	if err != nil || k != "sk-test-enc-1" {
		t.Fatalf("Get via enc = %q, %v", k, err)
	}
}

func TestLegacyFallbackRead(t *testing.T) {
	s := tmpStore(t)
	dir := filepath.Dir(s.Path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(s.Path(), []byte(`{"openai":"sk-legacy-1"}`), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	k, err := s.Get("openai")
	if err != nil || k != "sk-legacy-1" {
		t.Fatalf("legacy fallback Get = %q, %v", k, err)
	}
}

func TestWrongKeyRefuses(t *testing.T) {
	s := tmpStore(t)
	if err := s.Set("openai", "sk-test-1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Tamper with the sealed payload so GCM auth fails (simulates a
	// file sealed under a different machine key).
	data, err := os.ReadFile(s.encPath())
	if err != nil {
		t.Fatalf("read enc: %v", err)
	}
	if len(data) > 0 {
		data[len(data)-2] ^= 0xff
	}
	if err := os.WriteFile(s.encPath(), data, 0o600); err != nil {
		t.Fatalf("tamper enc: %v", err)
	}
	_, err = s.Get("openai")
	if err == nil {
		t.Fatal("tampered/wrong-key envelope must be refused, not silently read")
	}
	msg := err.Error()
	if !strings.Contains(msg, "delete") || !strings.Contains(msg, "/login") {
		t.Fatalf("wrong-key error must name the fix (delete + /login), got: %v", err)
	}
}

func TestMask(t *testing.T) {
	if got := Mask("sk-supersecret-4242"); got != "····4242" {
		t.Fatalf("Mask = %q", got)
	}
	if got := Mask("abc"); got == "abc" || got == "" {
		t.Fatalf("short key must not leak: %q", got)
	}
	if got := Mask(""); got == "" {
		t.Fatal("empty key must render as dots, not empty")
	}
}

func TestNilSafe(t *testing.T) {
	var s *Store
	if _, err := s.Get("openai"); err == nil {
		t.Fatal("nil store Get must error, not panic")
	}
	if err := s.Set("openai", "k"); err == nil {
		t.Fatal("nil store Set must error, not panic")
	}
	if err := s.Delete("openai"); err == nil {
		t.Fatal("nil store Delete must error, not panic")
	}
	if s.Path() == "" {
		t.Fatal("nil store Path must return a placeholder")
	}
}
