package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenRouterNamePrefixesProvider(t *testing.T) {
	p := NewOpenRouter("meta-llama/llama-3.3-70b-instruct:free", "", "k")
	if p.Name() != "openrouter/meta-llama/llama-3.3-70b-instruct:free" {
		t.Fatalf("Name = %q", p.Name())
	}
	if p.Base != DefaultOpenRouterBase {
		t.Fatalf("Base = %q, want default %q", p.Base, DefaultOpenRouterBase)
	}
}

func TestOpenRouterDefaultsToFreeModel(t *testing.T) {
	p := NewOpenRouter("", "", "k")
	want := "openrouter/" + CatalogIDs("openrouter")[0]
	if p.Name() != want {
		t.Fatalf("empty model should default to first free entry: got %q, want %q", p.Name(), want)
	}
}

func TestFactoryOpenRouter(t *testing.T) {
	p, err := Factory("openrouter", "", "", "k")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if !strings.HasPrefix(p.Name(), "openrouter/") {
		t.Fatalf("Name = %q", p.Name())
	}
	if _, err := Factory("openrouter", "", "", ""); err == nil {
		t.Fatal("openrouter without a key must refuse to construct")
	}
}

func TestResolveOpenRouterLadder(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "or-env-key")
	if k, src := Resolve(nil, "openrouter", nil); k != "or-env-key" || src != AuthEnv {
		t.Fatalf("env fallback: got %q %v", k, src)
	}
	store := stubStore{"openrouter": "or-stored"}
	if k, src := Resolve(store, "openrouter", nil); k != "or-stored" || src != AuthStored {
		t.Fatalf("stored must own the provider: got %q %v", k, src)
	}
}

func TestValidateOpenRouterClassifies(t *testing.T) {
	// 401 from the OpenRouter endpoint is a rejection, not a network error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	ok, net, err := Validate("openrouter", "bad-key", srv.URL)
	if ok || net || err == nil {
		t.Fatalf("401 must be (false,false,err), got %v %v %v", ok, net, err)
	}
}

func TestOpenRouterChatRelabelsRemedy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	}))
	defer srv.Close()
	p := NewOpenRouter("m", srv.URL, "bad-key")
	_, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected 401 error")
	}
	if !strings.Contains(err.Error(), "/login openrouter") {
		t.Fatalf("remedy must name /login openrouter, got: %v", err)
	}
	if strings.Contains(err.Error(), "/login openai") {
		t.Fatalf("openai remedy must not leak through, got: %v", err)
	}
}
