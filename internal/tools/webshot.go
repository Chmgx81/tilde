// Command web_shot captures a viewport screenshot of an http(s) URL with
// headless Firefox and reports the PNG path, dimensions, and byte size.
//
// SANDBOX NOTE: this tool runs OUTSIDE bwrap (like the MCP servers): it
// needs a display-less browser binary plus real network egress, neither of
// which exists inside the sandbox. Do not wrap the Firefox spawn in the
// sandbox config.
//
// TIER (for the owner wiring registry/policies after this lands): ask —
// non-mutating and Plan-OK like web_fetch, but it performs network egress
// and spawns an unsandboxed browser, so it needs explicit user approval.
// Suggested policies.yaml shape (owner to confirm): ask-tier, read-only.
//
// HONEST LIMIT: Firefox 155 headless `--screenshot` captures the VIEWPORT
// only — there is no full-page flag. The `full` arg is accepted (default
// true) for forward compatibility but currently changes nothing; size the
// viewport with width/height instead.
//
// The output PNG is for HUMAN review / future vision input: the model
// cannot see it today. Say what you need from a human, or fetch the page
// text with web_fetch.
package tools

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// WebShot screenshots a URL with headless Firefox (viewport-only).
type WebShot struct {
	AllowNet func() bool
	// HostAllow optionally approves one host (same P1-G per-host shape
	// as WebFetch): consulted when AllowNet is unset or false. SSRF
	// guards still apply after.
	HostAllow func(host string) bool
	// Runner spawns the browser; injectable for hermetic tests.
	// Default (nil) is exec.LookPath + exec.CommandContext output.
	Runner func(ctx context.Context, binary string, args ...string) ([]byte, error)
	// LookPath finds the browser binary; injectable for hermetic tests.
	// Default (nil) is exec.LookPath.
	LookPath func(string) (string, error)
}

func (t *WebShot) Name() string { return "web_shot" }
func (t *WebShot) Description() string {
	return "Screenshot an http(s) URL with headless Firefox (VIEWPORT only, no full-page mode). " +
		"Default-deny unless network is allowed. The PNG is for human review / future vision input — the model cannot see it today."
}
func (t *WebShot) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"url":     map[string]any{"type": "string"},
			"width":   map[string]any{"type": "number", "description": "Viewport width, default 1280, clamped to 640-3840"},
			"height":  map[string]any{"type": "number", "description": "Viewport height, default 900"},
			"full":    map[string]any{"type": "boolean", "description": "Accepted for forward compatibility (default true); currently a no-op: Firefox headless captures the viewport only"},
			"timeout": map[string]any{"type": "number", "description": "Seconds for the browser run, default 45, capped at 120"},
		}, "required": []string{"url"}}
}

const (
	webShotDefWidth  = 1280
	webShotMinWidth  = 640
	webShotMaxWidth  = 3840
	webShotDefHeight = 900
	webShotDefSecs   = 45
	webShotMaxSecs   = 120
)

func (t *WebShot) Exec(ctx context.Context, args map[string]any) (string, error) {
	raw, err := strArg(args, "url")
	if err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("only http(s) URLs are screenshottable, got %q: resend a full https:// URL", raw)
	}
	netOK := t.AllowNet != nil && t.AllowNet()
	if !netOK && t.HostAllow != nil {
		netOK = t.HostAllow(u.Hostname())
	}
	if !netOK {
		return fmt.Sprintf("web_shot denied: network is disabled by policy, so %q was not captured. Do not retry; work from local context or ask the user.", raw), nil
	}
	if u.User != nil {
		return "", fmt.Errorf("refused %q: userinfo in URL is not allowed", raw)
	}
	// Shared SSRF helpers from webfetch.go (same package): IP literal,
	// DNS-resolution, and private/loopback/link-local rules. Firefox
	// follows redirects on its own, so this pre-check covers only the
	// initial target: a public start URL can still redirect the browser
	// to a private/loopback host. Treat screenshots of untrusted pages
	// as untrusted input — never screenshot URLs from untrusted content
	// without user approval (ask-tier), and prefer web_fetch (which
	// re-validates every redirect) when only the text is needed.
	if err := validateFetchTarget(ctx, u); err != nil {
		return "", err
	}
	width := optInt(args, "width", webShotDefWidth)
	if width < webShotMinWidth {
		width = webShotMinWidth
	}
	if width > webShotMaxWidth {
		width = webShotMaxWidth
	}
	height := optInt(args, "height", webShotDefHeight)
	if height <= 0 {
		height = webShotDefHeight
	}
	if v, ok := args["full"]; ok && v != nil {
		if _, ok := v.(bool); !ok {
			return "", fmt.Errorf("argument \"full\" must be a boolean, got %T with value %v: resend as true/false", v, v)
		}
		// Accepted but currently a no-op (viewport-only, see header).
	}
	secs := optInt(args, "timeout", webShotDefSecs)
	if secs <= 0 {
		secs = webShotDefSecs
	}
	if secs > webShotMaxSecs {
		secs = webShotMaxSecs
	}

	look := t.LookPath
	if look == nil {
		look = exec.LookPath
	}
	bin, err := look("firefox")
	if err != nil {
		return "", fmt.Errorf("web_shot needs Firefox but `firefox` was not found in PATH: install Firefox (e.g. `apt install firefox` or `dnf install firefox`) and retry — no screenshot was taken")
	}

	// Per-call private temp dir (0700 via MkdirTemp), never
	// caller-controlled: no path traversal surface. Left on disk so the
	// human can review the PNG.
	dir, err := os.MkdirTemp("", "tilde-shot-*")
	if err != nil {
		return "", fmt.Errorf("cannot create screenshot dir: %v", err)
	}
	out := filepath.Join(dir, "shot.png")
	argv := []string{
		"--headless", "--no-remote", "--profile", dir,
		"--window-size", fmt.Sprintf("%d,%d", width, height),
		"--screenshot", out,
		u.String(),
	}
	run := t.Runner
	if run == nil {
		run = func(rctx context.Context, binary string, rargs ...string) ([]byte, error) {
			return exec.CommandContext(rctx, binary, rargs...).Output()
		}
	}
	rctx, cancel := context.WithTimeout(ctx, time.Duration(secs)*time.Second)
	defer cancel()
	if _, err := run(rctx, bin, argv...); err != nil {
		return "", fmt.Errorf("web_shot of %q failed: %v — the page may need more time (raise timeout:) or Firefox may have failed; the URL itself was validated", raw, err)
	}
	rawPNG, err := os.ReadFile(out)
	if err != nil {
		return "", fmt.Errorf("firefox ran but no screenshot appeared at %s: %v — retry or check the URL renders", out, err)
	}
	dims := "size-unknown"
	if w, h, perr := parsePNGSize(rawPNG); perr == nil {
		dims = fmt.Sprintf("%dx%d", w, h)
	}
	return fmt.Sprintf("screenshot: %s %s %dB\nnote: PNG is for human review / future vision input — the model cannot see it today; describe what you need from a human or fetch text with web_fetch.",
		out, dims, len(rawPNG)), nil
}

// parsePNGSize reads width/height from the PNG IHDR chunk with stdlib
// encoding/binary only (no image dependency). CRC is not verified.
func parsePNGSize(b []byte) (int, int, error) {
	if len(b) < 24 {
		return 0, 0, fmt.Errorf("too short for a PNG IHDR: %d bytes", len(b))
	}
	if string(b[:8]) != "\x89PNG\r\n\x1a\n" {
		return 0, 0, fmt.Errorf("bad PNG signature")
	}
	if binary.BigEndian.Uint32(b[8:12]) < 13 || string(b[12:16]) != "IHDR" {
		return 0, 0, fmt.Errorf("first chunk is not IHDR")
	}
	w := binary.BigEndian.Uint32(b[16:20])
	h := binary.BigEndian.Uint32(b[20:24])
	if w == 0 || h == 0 {
		return 0, 0, fmt.Errorf("zero IHDR dimensions %dx%d", w, h)
	}
	return int(w), int(h), nil
}
