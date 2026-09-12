package provider

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateRejectsUnverifiable(t *testing.T) {
	cases := []struct {
		status int
		want   []string
	}{
		{404, []string{"unverified", "check-url"}},
		{429, []string{"throttled", "retry"}},
		{500, []string{"unverified", "retry"}},
		{503, []string{"unverified", "retry"}},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
		}))
		ok, _, err := Validate("openai", "k", srv.URL)
		srv.Close()
		if ok {
			t.Errorf("status %d must NOT return ok=true", c.status)
			continue
		}
		if err == nil {
			t.Errorf("status %d must return an error", c.status)
			continue
		}
		lower := strings.ToLower(err.Error())
		for _, w := range c.want {
			if !strings.Contains(lower, w) {
				t.Errorf("status %d error must name %q, got %q", c.status, w, err.Error())
			}
		}
	}
}

func TestValidateAuthStaysKeyRejected(t *testing.T) {
	for _, status := range []int{401, 403} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		ok, net, err := Validate("openai", "k", srv.URL)
		srv.Close()
		if ok || net || err == nil {
			t.Fatalf("status %d must be (false,false,err), got %v %v %v", status, ok, net, err)
		}
		if !strings.Contains(strings.ToLower(err.Error()), "key rejected") {
			t.Fatalf("status %d must stay key-rejected, got %q", status, err.Error())
		}
	}
}

// Ollama Cloud validates against /api/tags with a Bearer key.
func TestValidateOllamaCloud(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()
	ok, net, err := Validate("ollama", "cloud-key", srv.URL)
	if !ok || net || err != nil {
		t.Fatalf("validate ollama = %v %v %v", ok, net, err)
	}
	if gotPath != "/api/tags" {
		t.Fatalf("path = %q, want /api/tags", gotPath)
	}
	if gotAuth != "Bearer cloud-key" {
		t.Fatalf("auth header = %q, want Bearer cloud-key", gotAuth)
	}
	// A rejected key stays key-rejected, not a network error.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv2.Close()
	ok, net, err = Validate("ollama", "bad", srv2.URL)
	if ok || net || err == nil {
		t.Fatalf("401 must be key-rejected: %v %v %v", ok, net, err)
	}
}
