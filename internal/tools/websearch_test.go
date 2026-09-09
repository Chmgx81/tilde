package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type searchStubTransport struct {
	calls  *int64
	body   string
	status int
	err    error
	last   *http.Request
}

func (s *searchStubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	atomic.AddInt64(s.calls, 1)
	s.last = r
	if s.err != nil {
		return nil, s.err
	}
	st := s.status
	if st == 0 {
		st = 200
	}
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", st, http.StatusText(st)),
		StatusCode: st,
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

func stubSearchTool(st *searchStubTransport) *WebSearch {
	return &WebSearch{
		AllowNet:   func() bool { return true },
		HTTPClient: &http.Client{Transport: st},
	}
}

var searchFixtureHTML = `<html><body>
<div class="result">
<h2 class="result__title"><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Falpha&amp;rut=aaa">Alpha &amp; Omega</a></h2>
<a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Falpha">Alpha snippet here.</a>
</div>
<div class="result">
<h2 class="result__title"><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Falpha&amp;rut=bbb">Alpha duplicate</a></h2>
<a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Falpha">Dup snippet.</a>
</div>
<div class="result">
<h2 class="result__title"><a rel="nofollow" class="result__a" href="https://example.org/beta">Beta <b>bold</b> title</a></h2>
<a class="result__snippet" href="https://example.org/beta">` + strings.Repeat("x", 600) + `</a>
</div>
<div class="result">
<h2 class="result__title"><a rel="nofollow" class="result__a" href="http://127.0.0.1/secret">Private</a></h2>
<a class="result__snippet" href="http://127.0.0.1/secret">Should be filtered.</a>
</div>
</body></html>`

func TestWebSearchParsing(t *testing.T) {
	var calls int64
	st := &searchStubTransport{body: searchFixtureHTML, calls: &calls}
	w := stubSearchTool(st)
	out, err := w.Exec(context.Background(), map[string]any{"query": "golang"})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	for _, want := range []string{"https://example.com/alpha", "https://example.org/beta", "Alpha & Omega", "Beta bold title", "Alpha snippet here."} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(st.last.URL.Host, "duckduckgo.com") {
		t.Fatalf("backend host = %q, want duckduckgo", st.last.URL.Host)
	}
	if ua := st.last.Header.Get("User-Agent"); ua == "" || strings.Contains(ua, "tilde") {
		t.Fatalf("want browser UA, got %q", ua)
	}
}

func TestWebSearchDedupe(t *testing.T) {
	var calls int64
	w := stubSearchTool(&searchStubTransport{body: searchFixtureHTML, calls: &calls})
	out, err := w.Exec(context.Background(), map[string]any{"query": "golang"})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if c := strings.Count(out, "https://example.com/alpha"); c != 1 {
		t.Fatalf("alpha URL appears %d times, want 1 (dedupe):\n%s", c, out)
	}
}

func TestWebSearchSnippetCap(t *testing.T) {
	var calls int64
	w := stubSearchTool(&searchStubTransport{body: searchFixtureHTML, calls: &calls})
	out, err := w.Exec(context.Background(), map[string]any{"query": "golang"})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.Contains(out, strings.Repeat("x", searchMaxSnip+1)) {
		t.Fatalf("snippet exceeds %d-char cap", searchMaxSnip)
	}
	if !strings.Contains(out, strings.Repeat("x", searchMaxSnip)+"…") {
		t.Fatalf("expected capped snippet with ellipsis")
	}
}

func TestWebSearchPrivateLinkFiltered(t *testing.T) {
	var calls int64
	w := stubSearchTool(&searchStubTransport{body: searchFixtureHTML, calls: &calls})
	out, err := w.Exec(context.Background(), map[string]any{"query": "golang"})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.Contains(out, "127.0.0.1") {
		t.Fatalf("private-IP result must be filtered:\n%s", out)
	}
}

func TestWebSearchMaxSources(t *testing.T) {
	var calls int64
	w := stubSearchTool(&searchStubTransport{body: searchFixtureHTML, calls: &calls})
	out, err := w.Exec(context.Background(), map[string]any{"query": "golang", "max_sources": float64(1)})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.Contains(out, "example.org/beta") {
		t.Fatalf("max_sources=1 must return one result:\n%s", out)
	}
}

func TestWebSearchCacheHit(t *testing.T) {
	var calls int64
	w := stubSearchTool(&searchStubTransport{body: searchFixtureHTML, calls: &calls})
	args := map[string]any{"query": "golang"}
	first, err := w.Exec(context.Background(), args)
	if err != nil {
		t.Fatalf("first exec: %v", err)
	}
	second, err := w.Exec(context.Background(), args)
	if err != nil {
		t.Fatalf("second exec: %v", err)
	}
	if first != second {
		t.Fatalf("cache hit must return identical output")
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("backend calls = %d, want 1 (second served from cache)", got)
	}
}

func TestWebSearchGateDenied(t *testing.T) {
	var calls int64
	for _, allow := range []func() bool{nil, func() bool { return false }} {
		w := &WebSearch{AllowNet: allow, HTTPClient: &http.Client{Transport: &searchStubTransport{calls: &calls, body: searchFixtureHTML}}}
		out, err := w.Exec(context.Background(), map[string]any{"query": "golang"})
		if err != nil {
			t.Fatalf("denied gate must return string, not error: %v", err)
		}
		if !strings.Contains(out, "denied") {
			t.Fatalf("want denied message, got %q", out)
		}
	}
	if got := atomic.LoadInt64(&calls); got != 0 {
		t.Fatalf("denied gate must not touch backend (calls=%d)", got)
	}
}

func TestWebSearchHostAllowListed(t *testing.T) {
	// Per-host approval passes without the session-wide opt-in; an
	// unlisted host still denies. Backend host is html.duckduckgo.com.
	var calls int64
	allow := &WebSearch{
		AllowNet:   func() bool { return false },
		HostAllow:  func(host string) bool { return host == "html.duckduckgo.com" },
		HTTPClient: &http.Client{Transport: &searchStubTransport{calls: &calls, body: searchFixtureHTML}},
	}
	out, err := allow.Exec(context.Background(), map[string]any{"query": "golang"})
	if err != nil {
		t.Fatalf("allowlisted backend host must search: %v", err)
	}
	if strings.Contains(out, "denied") {
		t.Fatalf("allowlisted backend host must not deny, got %q", out)
	}
	deny := &WebSearch{
		AllowNet:   func() bool { return false },
		HostAllow:  func(host string) bool { return host == "example.com" },
		HTTPClient: &http.Client{Transport: &searchStubTransport{calls: &calls, body: searchFixtureHTML}},
	}
	out, err = deny.Exec(context.Background(), map[string]any{"query": "golang"})
	if err != nil {
		t.Fatalf("denied gate must return string, not error: %v", err)
	}
	if !strings.Contains(out, "denied") {
		t.Fatalf("unlisted backend host must deny, got %q", out)
	}
}

func TestWebSearchEmptyQuery(t *testing.T) {
	w := &WebSearch{AllowNet: func() bool { return true }}
	for _, q := range []string{"", "   "} {
		_, err := w.Exec(context.Background(), map[string]any{"query": q})
		if err == nil {
			t.Fatalf("empty query %q must fail", q)
		}
		if strings.HasPrefix(err.Error(), "tool_unavailable:") {
			t.Fatalf("empty query is model-fixable, must not be tool_unavailable: %v", err)
		}
	}
}

func TestWebSearchBackendFailure(t *testing.T) {
	var calls int64
	w := stubSearchTool(&searchStubTransport{calls: &calls, err: fmt.Errorf("boom")})
	_, err := w.Exec(context.Background(), map[string]any{"query": "golang"})
	if err == nil || !strings.HasPrefix(err.Error(), "tool_unavailable: ") {
		t.Fatalf("backend failure must be tool_unavailable-prefixed, got %v", err)
	}
	bad := stubSearchTool(&searchStubTransport{calls: &calls, status: 500, body: "oops"})
	if _, err := bad.Exec(context.Background(), map[string]any{"query": "other"}); err == nil || !strings.HasPrefix(err.Error(), "tool_unavailable: ") {
		t.Fatalf("non-200 must be tool_unavailable-prefixed, got %v", err)
	}
}
