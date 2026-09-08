package creds

import (
	"os"
	"path/filepath"
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
	fi, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file mode = %o, want 600", fi.Mode().Perm())
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
	if err := os.WriteFile(s.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if _, err := s.Get("openai"); err == nil {
		t.Fatal("corrupt file must surface an error, not silent empty")
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
