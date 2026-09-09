package tools

import (
	"context"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// WebFetch is the blessed fetcher: plain HTTP GET only, no search, no JS.
type WebFetch struct {
	AllowNet func() bool
	// HostAllow optionally approves one host (P1-G per-host approval
	// from policies.yaml allow_net): consulted with the request hostname
	// when AllowNet is unset or false. SSRF guards still apply after.
	HostAllow func(host string) bool
}

func (t *WebFetch) Name() string { return "web_fetch" }
func (t *WebFetch) Description() string {
	return "Fetch an http(s) URL as text (no search, no JS). Default-deny unless network is allowed."
}
func (t *WebFetch) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"url":       map[string]any{"type": "string"},
			"max_bytes": map[string]any{"type": "number", "description": "Max bytes to return, default 65536, capped at 5MB"},
			"format":    map[string]any{"type": "string", "description": "Output format: text (default) or markdown"},
		}, "required": []string{"url"}}
}

const webFetchHardCap = 5 * 1024 * 1024

// webFetchUA keeps the tilde-webfetch token with a Mozilla-compatible prefix
// so plain static pages serve full content instead of bot walls.
const webFetchUA = "Mozilla/5.0 (compatible; tilde-webfetch/1.0)"

var (
	fetchTitleRe   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	fetchHeadRe    = regexp.MustCompile(`(?is)<head[^>]*>.*?</head\s*>`)
	fetchScriptRe  = regexp.MustCompile(`(?is)<script[^>]*>.*?</script\s*>`)
	fetchStyleRe   = regexp.MustCompile(`(?is)<style[^>]*>.*?</style\s*>`)
	fetchNavRe     = regexp.MustCompile(`(?is)<nav[^>]*>.*?</nav\s*>`)
	fetchCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	fetchBlockRe   = regexp.MustCompile(`(?i)</?(p|div|br|h[1-6]|li|tr|table|section|article|header|footer|blockquote|pre|ul|ol)[^>]*>`)
	fetchTagRe     = regexp.MustCompile(`(?s)<[^>]*>`)
)

func (t *WebFetch) Exec(ctx context.Context, args map[string]any) (string, error) {
	raw, err := strArg(args, "url")
	if err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("only http(s) URLs are fetchable, got %q: resend a full https:// URL", raw)
	}
	netOK := t.AllowNet != nil && t.AllowNet()
	if !netOK && t.HostAllow != nil {
		netOK = t.HostAllow(u.Hostname())
	}
	if !netOK {
		return fmt.Sprintf("web_fetch denied: network is disabled by policy, so %q was not fetched. Do not retry; work from local context or ask the user.", raw), nil
	}
	if u.User != nil {
		return "", fmt.Errorf("refused %q: userinfo in URL is not allowed", raw)
	}
	if err := validateFetchTarget(ctx, u); err != nil {
		return "", err
	}
	format := strings.ToLower(strings.TrimSpace(optStr(args, "format", "text")))
	if format == "" {
		format = "text"
	}
	if format != "text" && format != "markdown" {
		return "", fmt.Errorf("unsupported format %q: use \"text\" or \"markdown\"", format)
	}
	limit := optInt(args, "max_bytes", 64*1024)
	if limit <= 0 {
		limit = 64 * 1024
	}
	if limit > webFetchHardCap {
		limit = webFetchHardCap
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("bad URL %q: %v — verify it with a browser or fix the spelling", raw, err)
	}
	req.Header.Set("User-Agent", webFetchUA)
	if format == "markdown" {
		req.Header.Set("Accept", "text/markdown, text/html;q=0.9, text/plain;q=0.8")
	} else {
		req.Header.Set("Accept", "text/html, text/plain;q=0.9, */*;q=0.1")
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("stopped after 5 redirects")
		}
		if req.URL.User != nil {
			return fmt.Errorf("refused %q: userinfo in URL is not allowed", req.URL.String())
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("refused %q: only http(s) URLs are fetchable", req.URL.String())
		}
		if !netOK && t.HostAllow != nil && !t.HostAllow(req.URL.Hostname()) {
			return fmt.Errorf("refused redirect to %q: host %q is not in policies.yaml allow_net", req.URL.String(), req.URL.Hostname())
		}
		if err := validateFetchTarget(req.Context(), req.URL); err != nil {
			return err
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch of %q failed: %v — the host may be unreachable; try again later or use local context", raw, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch of %q returned status %s: not an error in your call — the remote page refused; try a different URL", raw, resp.Status)
	}
	ct := fetchContentType(resp.Header.Get("Content-Type"))
	if fetchContentTypeBlocked(ct) {
		return "", fmt.Errorf("refused %q: content-type %q is not fetchable as text (audio/video/image/binary) — try a different URL", raw, ct)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return "", fmt.Errorf("reading %q failed: %v — retry with a smaller max_bytes", raw, err)
	}
	truncated := ""
	if len(body) > limit {
		body = body[:limit]
		truncated = fmt.Sprintf("\n[truncated: output capped at %d bytes of %d max — retry with a larger max_bytes]", limit, webFetchHardCap)
	}
	rawText := strings.TrimRight(string(body), "\x00")
	var text string
	if ct == "" || strings.Contains(ct, "html") {
		title, plain := fetchHTMLToText(rawText)
		text = renderFetchText(title, plain, format)
	} else {
		text = strings.TrimSpace(rawText)
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("fetch of %q returned no text: the page may be empty or non-textual — try a different URL", raw)
	}
	return Fence(text) + truncated, nil
}

// fetchContentType normalizes a Content-Type header to its bare media type.
func fetchContentType(raw string) string {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(raw, ";")[0]))
	return ct
}

// fetchContentTypeBlocked reports binary media types refused as non-text.
func fetchContentTypeBlocked(ct string) bool {
	if strings.HasPrefix(ct, "audio/") || strings.HasPrefix(ct, "video/") || strings.HasPrefix(ct, "image/") {
		return true
	}
	return ct == "application/octet-stream"
}

// fetchHTMLToText strips script/style/nav blocks and tags, returning the
// cleaned <title> and the whitespace-collapsed body text.
func fetchHTMLToText(body string) (string, string) {
	title := ""
	if m := fetchTitleRe.FindStringSubmatch(body); m != nil {
		title = cleanFetchInline(m[1])
	}
	s := fetchScriptRe.ReplaceAllString(body, " ")
	s = fetchStyleRe.ReplaceAllString(s, " ")
	s = fetchNavRe.ReplaceAllString(s, " ")
	s = fetchHeadRe.ReplaceAllString(s, " ")
	s = fetchCommentRe.ReplaceAllString(s, " ")
	s = fetchBlockRe.ReplaceAllString(s, "\n")
	s = fetchTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	var kept []string
	for _, ln := range lines {
		if ln = strings.Join(strings.Fields(ln), " "); ln != "" {
			kept = append(kept, ln)
		}
	}
	return title, strings.Join(kept, "\n")
}

// cleanFetchInline strips tags/entities inside short inline strings (titles).
func cleanFetchInline(s string) string {
	s = fetchTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

// renderFetchText formats extracted text; markdown preserves line breaks and
// prefixes the title as an "# " header, all with stdlib only.
func renderFetchText(title, text, format string) string {
	if format != "markdown" {
		return strings.Join(strings.Fields(text), " ")
	}
	lines := strings.Split(text, "\n")
	var kept []string
	for _, ln := range lines {
		if ln = strings.Join(strings.Fields(ln), " "); ln != "" {
			kept = append(kept, ln)
		}
	}
	text = strings.Join(kept, "\n")
	if title != "" {
		if text != "" {
			return "# " + title + "\n\n" + text
		}
		return "# " + title
	}
	return text
}

func validateFetchTarget(ctx context.Context, u *url.URL) error {
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("refused %q: missing host", u.String())
	}
	if ip := net.ParseIP(host); ip != nil {
		if fetchIPBlocked(ip) {
			return fmt.Errorf("refused %q: resolves to private/loopback/link-local address", u.String())
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("refused %q: DNS lookup failed: %v", u.String(), err)
	}
	for _, a := range addrs {
		if fetchIPBlocked(a.IP) {
			return fmt.Errorf("refused %q: resolves to private/loopback/link-local address", u.String())
		}
	}
	return nil
}

func fetchIPBlocked(ip net.IP) bool {
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return true
	}
	if !ip.IsGlobalUnicast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 0 {
		return true
	}
	return false
}
