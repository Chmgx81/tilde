package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WebSearch is the keyless-first search: DuckDuckGo html endpoint only,
// no API keys, no JS. Default-deny unless network is allowed.
type WebSearch struct {
	AllowNet func() bool
	// HostAllow optionally approves one host (P1-G per-host approval
	// pattern, same as WebFetch): when set, the search backend host may
	// be queried without the session-wide opt-in. SSRF guards still apply.
	HostAllow func(host string) bool
	// HTTPClient, if non-nil, performs the backend request (tests stub it).
	HTTPClient *http.Client

	mu    sync.Mutex
	cache map[string]searchCacheEntry
}

type searchCacheEntry struct {
	output string
	expiry time.Time
}

func (t *WebSearch) Name() string { return "web_search" }
func (t *WebSearch) Description() string {
	return "Keyless web search via DuckDuckGo (no API key). Default-deny unless network is allowed."
}
func (t *WebSearch) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"query":       map[string]any{"type": "string"},
			"max_sources": map[string]any{"type": "number", "description": "Max sources to return, default 8, capped at 20"},
		}, "required": []string{"query"}}
}

const (
	searchEndpoint    = "https://html.duckduckgo.com/html/?q="
	searchBackendHost = "html.duckduckgo.com"
	searchUA          = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
	searchTimeout     = 15 * time.Second
	searchTTL         = 5 * time.Minute
	searchMaxSnip     = 500
	searchMaxBody     = 2 << 20
)

type searchResult struct {
	title   string
	url     string
	snippet string
}

var (
	searchTitleRe = regexp.MustCompile(`(?s)<a[^>]*class="result__a"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	searchSnipRe  = regexp.MustCompile(`(?s)<a[^>]*class="result__snippet"[^>]*>(.*?)</a>`)
	searchTagRe   = regexp.MustCompile(`<[^>]*>`)
)

func (t *WebSearch) Exec(ctx context.Context, args map[string]any) (string, error) {
	q, err := strArg(args, "query")
	if err != nil {
		return "", err
	}
	if q = strings.TrimSpace(q); q == "" {
		return "", fmt.Errorf("argument %q is empty: provide a non-empty search query", "query")
	}
	n := optInt(args, "max_sources", 8)
	if n < 1 {
		n = 1
	}
	if n > 20 {
		n = 20
	}
	if t.AllowNet == nil || !t.AllowNet() {
		if t.HostAllow == nil || !t.HostAllow(searchBackendHost) {
			return fmt.Sprintf("web_search denied: network is disabled by policy, so query %q was not searched. Do not retry; work from local context or ask the user.", q), nil
		}
	}
	key := searchCacheKey(q, n)
	if out, ok := t.cacheGet(key); ok {
		return out, nil
	}
	results, err := t.runSearch(ctx, q, n)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "", fmt.Errorf("tool_unavailable: web_search backend returned no usable results for %q. Do not retry; work from local context or ask the user", q)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "web_search results for %q (%d sources):\n", q, len(results))
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, r.title, r.url, r.snippet)
	}
	out := Fence(strings.TrimRight(b.String(), "\n"))
	t.cachePut(key, out)
	return out, nil
}

func (t *WebSearch) runSearch(ctx context.Context, q string, n int) ([]searchResult, error) {
	target := searchEndpoint + url.QueryEscape(q)
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("tool_unavailable: web_search backend URL is invalid: %v. Do not retry; work from local context", err)
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && searchIPBlocked(ip) {
		return nil, fmt.Errorf("tool_unavailable: web_search backend resolves to a blocked address. Do not retry; work from local context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("tool_unavailable: web_search request failed: %v. Do not retry; work from local context", err)
	}
	req.Header.Set("User-Agent", searchUA)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	client := t.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: searchTimeout, CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("stopped after 5 redirects")
			}
			return nil
		}}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tool_unavailable: web_search backend failed: %v. Do not retry; work from local context or ask the user", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("tool_unavailable: web_search backend returned status %s. Do not retry; work from local context or ask the user", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, searchMaxBody))
	if err != nil {
		return nil, fmt.Errorf("tool_unavailable: web_search backend read failed: %v. Do not retry; work from local context", err)
	}
	return parseSearchHTML(string(body), n), nil
}

func parseSearchHTML(body string, n int) []searchResult {
	titles := searchTitleRe.FindAllStringSubmatchIndex(body, -1)
	snips := searchSnipRe.FindAllStringSubmatchIndex(body, -1)
	var out []searchResult
	seen := map[string]bool{}
	for i, m := range titles {
		if len(out) >= n {
			break
		}
		link := resolveSearchLink(html.UnescapeString(body[m[2]:m[3]]))
		if link == "" || seen[link] {
			continue
		}
		title := cleanSearchText(body[m[4]:m[5]])
		if title == "" {
			title = link
		}
		end := len(body)
		if i+1 < len(titles) {
			end = titles[i+1][0]
		}
		snip := ""
		for _, s := range snips {
			if s[0] >= m[1] && s[0] < end {
				snip = cleanSearchText(body[s[2]:s[3]])
				break
			}
		}
		seen[link] = true
		out = append(out, searchResult{title: title, url: link, snippet: capSearchSnippet(snip)})
	}
	return out
}

func cleanSearchText(s string) string {
	s = searchTagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

func resolveSearchLink(href string) string {
	href = strings.TrimSpace(href)
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if strings.EqualFold(u.Host, "duckduckgo.com") && strings.HasPrefix(u.Path, "/l/") {
		if target := u.Query().Get("uddg"); target != "" {
			t2, err := url.Parse(target)
			if err != nil {
				return ""
			}
			u = t2
		}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	if u.Host == "" {
		return ""
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && searchIPBlocked(ip) {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

func capSearchSnippet(s string) string {
	if r := []rune(s); len(r) > searchMaxSnip {
		return string(r[:searchMaxSnip]) + "…"
	}
	return s
}

func searchCacheKey(q string, n int) string {
	sum := sha256.Sum256([]byte(strings.ToLower(q) + "\x00" + strconv.Itoa(n)))
	return hex.EncodeToString(sum[:])
}

func (t *WebSearch) cacheGet(key string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.cache[key]
	if !ok || time.Now().After(e.expiry) {
		return "", false
	}
	return e.output, true
}

func (t *WebSearch) cachePut(key, out string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cache == nil {
		t.cache = map[string]searchCacheEntry{}
	}
	t.cache[key] = searchCacheEntry{output: out, expiry: time.Now().Add(searchTTL)}
}

// searchIPBlocked mirrors the fetcher's private-range guard, duplicated here
// so websearch stays decoupled from webfetch internals. DNS-free by design:
// literal-IP results are filtered, everything else is display-only text.
func searchIPBlocked(ip net.IP) bool {
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
