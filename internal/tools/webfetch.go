package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WebFetch is the blessed fetcher: plain HTTP GET only, no search, no JS.
type WebFetch struct {
	AllowNet func() bool
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
		}, "required": []string{"url"}}
}

const webFetchHardCap = 5 * 1024 * 1024

func (t *WebFetch) Exec(ctx context.Context, args map[string]any) (string, error) {
	raw, err := strArg(args, "url")
	if err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("only http(s) URLs are fetchable, got %q: resend a full https:// URL", raw)
	}
	if t.AllowNet == nil || !t.AllowNet() {
		return fmt.Sprintf("web_fetch denied: network is disabled by policy, so %q was not fetched. Do not retry; work from local context or ask the user.", raw), nil
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
	req.Header.Set("User-Agent", "tilde-webfetch/1.0")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch of %q failed: %v — the host may be unreachable; try again later or use local context", raw, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch of %q returned status %s: not an error in your call — the remote page refused; try a different URL", raw, resp.Status)
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
	text := strings.TrimRight(string(body), "\x00")
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("fetch of %q returned no text: the page may be empty or non-textual — try a different URL", raw)
	}
	return Fence(text) + truncated, nil
}
