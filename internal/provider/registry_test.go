package provider

import (
	"errors"
	"os"
	"testing"
)

func TestParseModelRefForms(t *testing.T) {
	cases := []struct {
		in    string
		prov  string
		model string
		ok    bool
	}{
		{"openai/gpt-4o-mini", "openai", "gpt-4o-mini", true},
		{"anthropic/claude-sonnet-4", "anthropic", "claude-sonnet-4", true},
		{"ollama/llama3.2", "ollama", "llama3.2", true},
		{"llama3.2", "", "", false},
		{"", "", "", false},
		{"/model", "", "", false},
		{"openai/", "", "", false},
	}
	for _, c := range cases {
		p, m, ok := ParseModelRef(c.in)
		if ok != c.ok || p != c.prov || m != c.model {
			t.Errorf("ParseModelRef(%q) = %q,%q,%v — want %q,%q,%v", c.in, p, m, ok, c.prov, c.model, c.ok)
		}
	}
}

type stubStore map[string]string

func (s stubStore) Get(id string) (string, error) {
	if k, ok := s[id]; ok {
		return k, nil
	}
	return "", errors.New("none")
}

// Ollama is optional-key: keyless local works, a stored/env key means Cloud.
func TestResolveOllamaOptionalKey(t *testing.T) {
	t.Setenv("OLLAMA_API_KEY", "")
	if k, src := Resolve(nil, "ollama", nil); k != "" || src != AuthNone {
		t.Fatalf("keyless ollama: got %q %v", k, src)
	}
	if _, err := Factory("ollama", "", "", ""); err != nil {
		t.Fatalf("keyless ollama must construct: %v", err)
	}
	store := stubStore{"ollama": "cloud-key-1234"}
	if k, src := Resolve(store, "ollama", nil); k != "cloud-key-1234" || src != AuthStored {
		t.Fatalf("stored ollama key: got %q %v", k, src)
	}
	t.Setenv("OLLAMA_API_KEY", "env-cloud-5678")
	if k, src := Resolve(nil, "ollama", nil); k != "env-cloud-5678" || src != AuthEnv {
		t.Fatalf("env ollama key: got %q %v", k, src)
	}
}

func TestLoginIDsIncludeOllama(t *testing.T) {
	login := false
	for _, id := range LoginIDs() {
		if id == "ollama" {
			login = true
		}
	}
	if !login {
		t.Fatal("LoginIDs must include ollama (Ollama Cloud)")
	}
	for _, id := range CloudIDs() {
		if id == "ollama" {
			t.Fatal("CloudIDs must stay key-required only")
		}
	}
}

func TestResolveLadderOrder(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key-1234")
	t.Setenv("ANTHROPIC_API_KEY", "")
	// No store, no flag: env wins.
	if k, src := Resolve(nil, "openai", nil); k != "env-key-1234" || src != AuthEnv {
		t.Fatalf("env fallback: got %q %v", k, src)
	}
	// Stored beats env (a stored credential owns the provider).
	store := stubStore{"openai": "stored-key-5678"}
	if k, src := Resolve(store, "openai", nil); k != "stored-key-5678" || src != AuthStored {
		t.Fatalf("stored must own the provider: got %q %v", k, src)
	}
	// Explicit flag outranks everything for this process.
	if k, src := Resolve(store, "openai", map[string]string{"openai": "flag-key-9999"}); k != "flag-key-9999" || src != AuthFlag {
		t.Fatalf("flag outranks all: got %q %v", k, src)
	}
	// Nothing anywhere: AuthNone, empty key.
	if k, src := Resolve(store, "anthropic", nil); k != "" || src != AuthNone {
		t.Fatalf("absence must be AuthNone: got %q %v", k, src)
	}
}

func TestStatusMasksKeys(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-supersecret-4242")
	t.Setenv("ANTHROPIC_API_KEY", "short")
	for _, st := range Status(nil, nil) {
		if st.Provider.ID != "openai" {
			continue
		}
		if st.KeyTail != "…4242" {
			t.Fatalf("status must show only the tail, got %q", st.KeyTail)
		}
		if containsAll(os.Getenv("OPENAI_API_KEY")) {
		}
		_ = st
	}
}

func containsAll(string) bool { return true }

func TestFactoryDefaultsAndNames(t *testing.T) {
	p, err := Factory("ollama", "", "", "")
	if err != nil {
		t.Fatalf("ollama factory: %v", err)
	}
	if p.Name() != "ollama/qwen3.8-4b:16k" {
		t.Fatalf("ollama default model: %q", p.Name())
	}
	p, err = Factory("openai", "", "", "k")
	if err != nil {
		t.Fatalf("openai factory: %v", err)
	}
	if p.Name() != "openai/gpt-5.2" {
		t.Fatalf("openai default model: %q", p.Name())
	}
	p, err = Factory("anthropic", "", "", "k")
	if err != nil {
		t.Fatalf("anthropic factory: %v", err)
	}
	if p.Name() != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("anthropic default model: %q", p.Name())
	}
	if _, err := Factory("openai", "", "", ""); err == nil {
		t.Fatal("openai without a key must refuse to construct")
	}
	if _, err := Factory("nope", "", "", "k"); err == nil {
		t.Fatal("unknown provider must refuse")
	}
}

func TestValidateClassifiesErrors(t *testing.T) {
	// Unreachable endpoint → network error, never "rejected".
	ok, net, err := Validate("openai", "k", "http://127.0.0.1:1/v1")
	if ok || !net || err == nil {
		t.Fatalf("unreachable must be (false,true,err), got %v %v %v", ok, net, err)
	}
	// Unknown provider is a named error up front.
	if _, _, err := Validate("nope", "k", ""); err == nil {
		t.Fatal("unknown provider must fail validate")
	}
}
