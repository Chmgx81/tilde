package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebFetchSSRFRefused(t *testing.T) {
	allow := func() bool { return true }
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1/", http.StatusFound)
	}))
	defer redirector.Close()

	cases := []struct {
		name string
		url  string
	}{
		{"metadata", "http://169.254.169.254/latest/meta-data/"},
		{"loopback", "http://127.0.0.1/"},
		{"ipv6-loopback", "http://[::1]/"},
		{"userinfo", "http://user:pass@example.com/"},
		{"redirect-to-private", redirector.URL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &WebFetch{AllowNet: allow}
			if _, err := w.Exec(context.Background(), map[string]any{"url": tc.url}); err == nil || !strings.Contains(err.Error(), "refused") {
				t.Fatalf("%s: expected refused error, got %v", tc.name, err)
			}
		})
	}
}

// P1-G per-host approval: an allowlisted host passes the net gate without
// the session-wide opt-in (.invalid never resolves, so anything but the
// "network is disabled" denial proves the gate opened); unlisted hosts
// still get the policy-denied message.
func TestWebFetchAllowNetHost(t *testing.T) {
	w := &WebFetch{
		AllowNet:  func() bool { return false },
		HostAllow: func(host string) bool { return strings.EqualFold(host, "allowed.invalid") },
	}
	for _, url := range []string{"https://allowed.invalid/x", "https://allowed.invalid:8443/x"} {
		out, err := w.Exec(context.Background(), map[string]any{"url": url})
		if strings.Contains(out, "network is disabled") || (err != nil && strings.Contains(err.Error(), "network is disabled")) {
			t.Fatalf("allowlisted %q must pass the gate: out=%q err=%v", url, out, err)
		}
	}
	out, err := w.Exec(context.Background(), map[string]any{"url": "https://other.invalid/x"})
	if err != nil || !strings.Contains(out, "network is disabled") {
		t.Fatalf("unlisted host must stay denied: out=%q err=%v", out, err)
	}
}

var fetchFixtureHTML = `<html><head><title>Example &amp; Title</title><style>.x{color:red}</style>` +
	`<script>var evil = 1;</script></head><body><nav><a href="/menu">Menu items</a></nav>` +
	`<h1>Hello <b>World</b></h1><p>First paragraph.</p><p>Second  paragraph.</p></body></html>`

func TestWebFetchHTMLText(t *testing.T) {
	title, text := fetchHTMLToText(fetchFixtureHTML)
	if title != "Example & Title" {
		t.Fatalf("title = %q, want %q", title, "Example & Title")
	}
	for _, want := range []string{"Hello World", "First paragraph.", "Second paragraph."} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	for _, banned := range []string{"<", ">", "evil", "color:red", "Menu items"} {
		if strings.Contains(text, banned) {
			t.Fatalf("unclean output contains %q:\n%s", banned, text)
		}
	}
	if got := renderFetchText(title, text, "text"); strings.Contains(got, "\n") {
		t.Fatalf("text format must collapse whitespace to one line, got:\n%s", got)
	}
}

func TestWebFetchBinaryContentTypeRefused(t *testing.T) {
	for _, ct := range []string{"image/png", "image/jpeg", "video/mp4", "audio/mpeg", "application/octet-stream"} {
		if !fetchContentTypeBlocked(fetchContentType(ct + "; charset=binary")) {
			t.Fatalf("content-type %q must be refused", ct)
		}
		if !fetchContentTypeBlocked(fetchContentType(ct)) {
			t.Fatalf("content-type %q must be refused", ct)
		}
	}
	for _, ct := range []string{"", "text/html", "text/html; charset=utf-8", "text/plain", "application/json"} {
		if fetchContentTypeBlocked(fetchContentType(ct)) {
			t.Fatalf("content-type %q must be fetchable", ct)
		}
	}
}

func TestWebFetchMarkdown(t *testing.T) {
	title, text := fetchHTMLToText(fetchFixtureHTML)
	got := renderFetchText(title, text, "markdown")
	if !strings.HasPrefix(got, "# Example & Title\n\n") {
		t.Fatalf("markdown must prefix title header, got:\n%s", got)
	}
	if !strings.Contains(got, "First paragraph.\nSecond paragraph.") {
		t.Fatalf("markdown must preserve line breaks, got:\n%s", got)
	}
	if strings.Contains(got, "<") {
		t.Fatalf("markdown must not contain tags, got:\n%s", got)
	}
}

func TestWebFetchUAAndSchema(t *testing.T) {
	if !strings.Contains(webFetchUA, "tilde-webfetch") || !strings.Contains(webFetchUA, "Mozilla") {
		t.Fatalf("UA must keep tilde-webfetch token with Mozilla compat, got %q", webFetchUA)
	}
	props, ok := (&WebFetch{}).Schema()["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema missing properties")
	}
	if _, ok := props["format"]; !ok {
		t.Fatalf("schema must advertise format param")
	}
}
