package main

import (
	"strings"
	"testing"

	"tilde/internal/creds"
)

// TestAuthLoginFromEnv exercises the headless credential path end to end:
// the key comes from the provider's env var (never argv), lands in the
// sealed store, is readable back, and is removable.
func TestAuthLoginFromEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const key = "sk-test-abcdefghijklmnopqrstuvwxyz0123456789"
	t.Setenv("OPENCODE_API_KEY", key)

	if err := runAuthCmd("login", []string{"login", "opencode"}); err != nil {
		t.Fatalf("login: %v", err)
	}
	store, err := openCredStore()
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if got != key {
		t.Fatalf("stored key = %q, want the env key", got)
	}
	// Status renders without leaking the full key.
	if tail := creds.Mask(got); strings.Contains(tail, key) {
		t.Fatalf("mask leaked the key: %q", tail)
	}
	if err := runAuthCmd("logout", []string{"logout", "opencode"}); err != nil {
		t.Fatalf("logout: %v", err)
	}
	after, err := store.Get("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if after != "" {
		t.Fatalf("logout left key behind: %q", after)
	}
}

func TestAuthUnknownProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := runAuthCmd("login", []string{"login", "notaprovider"}); err == nil {
		t.Fatal("unknown provider must fail loud")
	}
}

func TestAuthLocalProviderNeedsNoKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := runAuthCmd("login", []string{"login", "ollama"})
	if err == nil || !strings.Contains(err.Error(), "Ollama Cloud") {
		t.Fatalf("ollama login should refuse with the remote-cloud hint, got: %v", err)
	}
}

func TestAuthVercelTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const tok = "vcp_abcdefghijklmnopqrstuvwxyz0123456789ABCDEF"
	t.Setenv("VERCEL_TOKEN", tok)
	if err := runAuthCmd("login", []string{"login", "vercel"}); err != nil {
		t.Fatalf("vercel login: %v", err)
	}
	store, err := openCredStore()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get("vercel"); got != tok {
		t.Fatalf("stored vercel token = %q", got)
	}
	if err := runAuthCmd("logout", []string{"logout", "vercel"}); err != nil {
		t.Fatalf("vercel logout: %v", err)
	}
}

func TestAuthStatusNoStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	// Bare verb prints the status matrix and exits cleanly.
	if err := runAuthCmd("login", []string{"login"}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if err := runAuthCmd("login", []string{"login", "--list"}); err != nil {
		t.Fatalf("status --list: %v", err)
	}
}
