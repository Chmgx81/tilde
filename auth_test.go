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

// Ollama is an optional-key target now: the local daemon needs nothing,
// but `tilde login ollama` stores an Ollama Cloud key.
func TestAuthOllamaCloudKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const key = "0123456789abcdef0123456789abcdef.FAKEFAKEFAKEFAKEFAKE1234"
	t.Setenv("OLLAMA_API_KEY", key)
	if err := runAuthCmd("login", []string{"login", "ollama"}); err != nil {
		t.Fatalf("ollama login: %v", err)
	}
	store, err := openCredStore()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get("ollama"); got != key {
		t.Fatalf("stored ollama key = %q", got)
	}
	if err := runAuthCmd("logout", []string{"logout", "ollama"}); err != nil {
		t.Fatalf("ollama logout: %v", err)
	}
}

// The service-token seam: an entry in the services table becomes a
// storable target with no other code change.
func TestAuthServiceTargetSeam(t *testing.T) {
	targets := credTargetsWith([]serviceTarget{{ID: "example", EnvKey: "EXAMPLE_TOKEN"}})
	if _, ok := lookupCredTargetIn(targets, "example"); !ok {
		t.Fatal("synthetic service target must be storable")
	}
	// And it flows through the generic store like any other credential.
	home := t.TempDir()
	t.Setenv("HOME", home)
	store, err := openCredStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("example", "svc-token"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get("example"); got != "svc-token" {
		t.Fatalf("stored service token = %q", got)
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
