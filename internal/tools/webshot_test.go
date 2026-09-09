package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// png1x1 is a minimal IHDR-only PNG (width=1, height=1); CRC is bogus on
// purpose — parsePNGSize must not verify it.
var png1x1 = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0, 0, 0, 13, 'I', 'H', 'D', 'R',
	0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0,
	0xde, 0xad, 0xbe, 0xef,
}

const shotTestURL = "http://93.184.216.34/"

type shotCapture struct {
	binary string
	args   []string
	ctx    context.Context
}

func stubShot(t *testing.T, cap *shotCapture, png []byte) WebShot {
	t.Helper()
	if png == nil {
		png = png1x1
	}
	return WebShot{
		AllowNet: func() bool { return true },
		LookPath: func(s string) (string, error) {
			if s != "firefox" {
				t.Errorf("LookPath called with %q, want firefox", s)
			}
			return "/usr/bin/firefox", nil
		},
		Runner: func(ctx context.Context, binary string, args ...string) ([]byte, error) {
			cap.binary = binary
			cap.args = args
			cap.ctx = ctx
			out := ""
			for i, a := range args {
				if a == "--screenshot" && i+1 < len(args) {
					out = args[i+1]
				}
			}
			if out == "" {
				t.Errorf("Runner argv has no --screenshot value: %q", args)
				return nil, errors.New("no screenshot path")
			}
			if err := os.WriteFile(out, png, 0o600); err != nil {
				t.Errorf("stub writing PNG: %v", err)
				return nil, err
			}
			return []byte("ok"), nil
		},
	}
}

func shotArg(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func TestWebShotArgv(t *testing.T) {
	var cap shotCapture
	w := stubShot(t, &cap, nil)
	out, err := w.Exec(context.Background(), map[string]any{"url": shotTestURL})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if cap.binary != "/usr/bin/firefox" {
		t.Fatalf("binary = %q", cap.binary)
	}
	if len(cap.args) == 0 || cap.args[0] != "--headless" {
		t.Fatalf("argv should start with --headless: %q", cap.args)
	}
	found := false
	for _, a := range cap.args {
		if a == "--no-remote" {
			found = true
		}
		if a == "--full-page" {
			t.Fatalf("no --full-page flag exists in Firefox headless: %q", cap.args)
		}
	}
	if !found {
		t.Fatalf("argv missing --no-remote: %q", cap.args)
	}
	prof, ok := shotArg(cap.args, "--profile")
	if !ok {
		t.Fatalf("argv missing --profile: %q", cap.args)
	}
	shot, ok := shotArg(cap.args, "--screenshot")
	if !ok {
		t.Fatalf("argv missing --screenshot: %q", cap.args)
	}
	if filepath.Dir(shot) != prof {
		t.Fatalf("screenshot %q not inside profile/tmpdir %q", shot, prof)
	}
	if !strings.HasSuffix(shot, ".png") {
		t.Fatalf("screenshot path should end .png: %q", shot)
	}
	size, ok := shotArg(cap.args, "--window-size")
	if !ok || size != "1280,900" {
		t.Fatalf("default window-size should be 1280,900, got %q (argv %q)", size, cap.args)
	}
	if last := cap.args[len(cap.args)-1]; last != shotTestURL {
		t.Fatalf("URL must be last argv, got %q (argv %q)", last, cap.args)
	}
	if dl, ok := cap.ctx.Deadline(); !ok || time.Until(dl) > 121*time.Second {
		t.Fatalf("runner ctx should carry the ~45s timeout deadline, got %v ok=%v", dl, ok)
	}
	if !strings.Contains(out, "screenshot: "+shot) || !strings.Contains(out, "1x1") {
		t.Fatalf("output should name path + 1x1 dims: %q", out)
	}
	if !strings.Contains(out, "cannot see it today") {
		t.Fatalf("output must carry the vision note: %q", out)
	}
	if fi, err := os.Stat(shot); err != nil || fi.Size() != int64(len(png1x1)) {
		t.Fatalf("PNG should exist on disk at %q: %v", shot, err)
	}
}

func TestWebShotWidthClamp(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{100, "640,900"},
		{640, "640,900"},
		{1280, "1280,900"},
		{5000, "3840,900"},
	} {
		var cap shotCapture
		w := stubShot(t, &cap, nil)
		if _, err := w.Exec(context.Background(), map[string]any{"url": shotTestURL, "width": tc.in}); err != nil {
			t.Fatalf("width %v: %v", tc.in, err)
		}
		size, _ := shotArg(cap.args, "--window-size")
		if size != tc.want {
			t.Errorf("width %v: window-size = %q, want %q", tc.in, size, tc.want)
		}
	}
}

func TestWebShotGateDenied(t *testing.T) {
	called := false
	w := &WebShot{
		Runner: func(ctx context.Context, binary string, args ...string) ([]byte, error) {
			called = true
			return nil, nil
		},
	}
	out, err := w.Exec(context.Background(), map[string]any{"url": "http://127.0.0.1/"})
	if err != nil {
		t.Fatalf("gate denial is a message, not an error: %v", err)
	}
	if !strings.Contains(out, "web_shot denied") || !strings.Contains(out, "network is disabled") {
		t.Fatalf("denied message shape: %q", out)
	}
	if called {
		t.Fatal("Runner must not run when the gate denies")
	}
}

func TestWebShotHostAllowGate(t *testing.T) {
	// HostAllow approves the loopback literal (no DNS involved), so the
	// SSRF refusal — not the policy denial — proves the gate opened.
	w := &WebShot{
		AllowNet:  func() bool { return false },
		HostAllow: func(host string) bool { return host == "127.0.0.1" },
		LookPath: func(s string) (string, error) {
			t.Error("browser lookup must not run for an SSRF target")
			return "", errors.New("unreachable")
		},
	}
	_, err := w.Exec(context.Background(), map[string]any{"url": "http://127.0.0.1/"})
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("allowlisted loopback must reach SSRF refusal, got %v", err)
	}
	// Unlisted host with a public IP literal stays policy-denied (message,
	// nil error, no browser spawn).
	called := false
	w2 := &WebShot{
		AllowNet:  func() bool { return false },
		HostAllow: func(host string) bool { return false },
		LookPath: func(s string) (string, error) {
			t.Error("browser lookup must not run when denied")
			return "", errors.New("unreachable")
		},
		Runner: func(ctx context.Context, binary string, args ...string) ([]byte, error) {
			called = true
			return nil, nil
		},
	}
	out, err := w2.Exec(context.Background(), map[string]any{"url": shotTestURL})
	if err != nil || !strings.Contains(out, "network is disabled") || called {
		t.Fatalf("unlisted host must stay denied: out=%q err=%v called=%v", out, err, called)
	}
}

func TestWebShotUserinfoAndSSRF(t *testing.T) {
	noSpawn := func(s string) (string, error) {
		t.Error("browser lookup must not run for a refused URL")
		return "", errors.New("unreachable")
	}
	allow := func() bool { return true }
	for _, tc := range []struct {
		name string
		url  string
		want string
	}{
		{"userinfo", "http://user:pass@93.184.216.34/", "userinfo"},
		{"loopback", "http://127.0.0.1/", "refused"},
		{"metadata", "http://169.254.169.254/latest/meta-data/", "refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &WebShot{AllowNet: allow, LookPath: noSpawn}
			_, err := w.Exec(context.Background(), map[string]any{"url": tc.url})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestWebShotNoFirefox(t *testing.T) {
	w := &WebShot{
		AllowNet: func() bool { return true },
		LookPath: func(s string) (string, error) { return "", errors.New("not found in PATH") },
	}
	_, err := w.Exec(context.Background(), map[string]any{"url": shotTestURL})
	if err == nil || !strings.Contains(err.Error(), "install Firefox") {
		t.Fatalf("missing firefox must fail loud naming the install fix, got %v", err)
	}
}

func TestParsePNGSize(t *testing.T) {
	w, h, err := parsePNGSize(png1x1)
	if err != nil || w != 1 || h != 1 {
		t.Fatalf("1x1 fixture: got %dx%d err=%v", w, h, err)
	}
	if _, _, err := parsePNGSize([]byte("not a png at all, just text........")); err == nil {
		t.Fatal("bad signature must error")
	}
	if _, _, err := parsePNGSize(png1x1[:10]); err == nil {
		t.Fatal("truncated input must error")
	}
	bad := append([]byte(nil), png1x1...)
	copy(bad[12:16], "IDAT")
	if _, _, err := parsePNGSize(bad); err == nil {
		t.Fatal("non-IHDR first chunk must error")
	}
}
