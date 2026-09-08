package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenCodeNamePrefixesProvider(t *testing.T) {
	p := NewOpenCode("kimi-k2.7-code", "", "k")
	if p.Name() != "opencode/kimi-k2.7-code" {
		t.Fatalf("Name = %q", p.Name())
	}
	if p.Base != DefaultOpenCodeBase {
		t.Fatalf("Base = %q, want default %q", p.Base, DefaultOpenCodeBase)
	}
}

func TestOpenCodeDefaultsToCodingModel(t *testing.T) {
	p := NewOpenCode("", "", "k")
	want := "opencode/" + CatalogIDs("opencode")[0]
	if p.Name() != want {
		t.Fatalf("empty model should default to catalog first: got %q, want %q", p.Name(), want)
	}
}

func TestFactoryOpenCode(t *testing.T) {
	p, err := Factory("opencode", "", "", "k")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if !strings.HasPrefix(p.Name(), "opencode/") {
		t.Fatalf("Name = %q", p.Name())
	}
	if _, err := Factory("opencode", "", "", ""); err == nil {
		t.Fatal("opencode without a key must refuse to construct")
	}
}

func TestResolveOpenCodeLadder(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "zen-env-key")
	if k, src := Resolve(nil, "opencode", nil); k != "zen-env-key" || src != AuthEnv {
		t.Fatalf("env fallback: got %q %v", k, src)
	}
	store := stubStore{"opencode": "zen-stored"}
	if k, src := Resolve(store, "opencode", nil); k != "zen-stored" || src != AuthStored {
		t.Fatalf("stored must own the provider: got %q %v", k, src)
	}
}

func TestValidateOpenCodeClassifies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	ok, net, err := Validate("opencode", "bad-key", srv.URL)
	if ok || net || err == nil {
		t.Fatalf("401 must be (false,false,err), got %v %v %v", ok, net, err)
	}
}

func TestOpenCodeChatRelabelsRemedy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	}))
	defer srv.Close()
	p := NewOpenCode("m", srv.URL, "bad-key")
	_, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected 401 error")
	}
	if !strings.Contains(err.Error(), "/login opencode") {
		t.Fatalf("remedy must name /login opencode, got: %v", err)
	}
	if strings.Contains(err.Error(), "/login openai") {
		t.Fatalf("openai remedy must not leak through, got: %v", err)
	}
}

func TestOpenCodeCatalogIsChatCompletionsOnly(t *testing.T) {
	// Guardrail for the catalog discipline documented on OpenCode:
	// every listed id must come from the verified set. A future edit
	// adding a /responses or /messages model fails here on purpose.
	allowed := map[string]bool{
		"kimi-k2.7-code": true, "minimax-m3": true, "glm-5.3": true,
		"deepseek-v4-pro": true, "nemotron-3-ultra-free": true,
	}
	for _, cm := range Catalog["opencode"] {
		if !allowed[cm.ID] {
			t.Fatalf("catalog id %q is not in the verified chat/completions set — check the endpoint before listing", cm.ID)
		}
	}
}
