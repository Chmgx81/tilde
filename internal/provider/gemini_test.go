package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeminiNamePrefixesProvider(t *testing.T) {
	p := NewGemini("gemini-2.5-flash", "", "k")
	if p.Name() != "gemini/gemini-2.5-flash" {
		t.Fatalf("Name = %q", p.Name())
	}
	if p.Base != DefaultGeminiBase {
		t.Fatalf("Base = %q, want default %q", p.Base, DefaultGeminiBase)
	}
}

func TestGeminiDefaultsToFlash(t *testing.T) {
	p := NewGemini("", "", "k")
	want := "gemini/" + CatalogIDs("gemini")[0]
	if p.Name() != want {
		t.Fatalf("empty model should default to catalog first: got %q, want %q", p.Name(), want)
	}
}

func TestGeminiKeyFallbacks(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "google-env-key")
	p := NewGemini("m", "", "")
	if p.Key != "google-env-key" {
		t.Fatalf("GOOGLE_API_KEY must work as fallback, got %q", p.Key)
	}
}

func TestFactoryGemini(t *testing.T) {
	p, err := Factory("gemini", "", "", "k")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if !strings.HasPrefix(p.Name(), "gemini/") {
		t.Fatalf("Name = %q", p.Name())
	}
	if _, err := Factory("gemini", "", "", ""); err == nil {
		t.Fatal("gemini without a key must refuse to construct")
	}
}

func TestResolveGeminiLadder(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "gem-env-key")
	t.Setenv("GOOGLE_API_KEY", "")
	if k, src := Resolve(nil, "gemini", nil); k != "gem-env-key" || src != AuthEnv {
		t.Fatalf("env fallback: got %q %v", k, src)
	}
	store := stubStore{"gemini": "gem-stored"}
	if k, src := Resolve(store, "gemini", nil); k != "gem-stored" || src != AuthStored {
		t.Fatalf("stored must own the provider: got %q %v", k, src)
	}
}

func TestValidateGeminiClassifies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	ok, net, err := Validate("gemini", "bad-key", srv.URL)
	if ok || net || err == nil {
		t.Fatalf("401 must be (false,false,err), got %v %v %v", ok, net, err)
	}
}

func TestGeminiChatRelabelsRemedy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	}))
	defer srv.Close()
	p := NewGemini("m", srv.URL, "bad-key")
	_, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected 401 error")
	}
	if !strings.Contains(err.Error(), "/login gemini") {
		t.Fatalf("remedy must name /login gemini, got: %v", err)
	}
	if strings.Contains(err.Error(), "/login openai") {
		t.Fatalf("openai remedy must not leak through, got: %v", err)
	}
}
