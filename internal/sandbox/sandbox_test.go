package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func requireBwrap(t *testing.T) {
	t.Helper()
	if !Available() {
		t.Skip("bwrap not on PATH — cannot verify OS-level containment here")
	}
	if Disabled() {
		t.Skip("TILDE_NO_SANDBOX=1 — containment deliberately off")
	}
	// Some hosted Ubuntu runners expose bubblewrap but prohibit the network
	// namespace capability it needs to initialize loopback. Keep those hosts
	// from turning a capability limitation into a false code failure; the
	// production path still fails closed when bwrap cannot execute.
	cmd, err := (&Config{Root: t.TempDir()}).Command(context.Background(), "true")
	if err != nil {
		t.Skipf("bwrap unavailable: %v", err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		text := string(out)
		if strings.Contains(text, "RTM_NEWADDR") || strings.Contains(text, "Operation not permitted") {
			t.Skipf("bwrap host capability unavailable: %s", strings.TrimSpace(text))
		}
		t.Fatalf("bwrap probe failed: %v: %s", err, text)
	}
}

func run(t *testing.T, root, cmd string) string {
	t.Helper()
	c := &Config{Root: root}
	ec, err := c.Command(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ec.CombinedOutput()
	if err != nil {
		// Non-zero exits are data, not test failures — return output anyway.
		return string(out) + "\n[exit-err: " + err.Error() + "]"
	}
	return string(out)
}

func TestWriteOutsideRootIsContained(t *testing.T) {
	requireBwrap(t)
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "pwned")
	run(t, root, "touch "+outside+" 2>&1; echo done")
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("BREAKOUT: sandboxed command wrote outside root to %s", outside)
	}
	// Absolute-path write lands in the throwaway tmpfs root, not the host.
	out := run(t, root, "touch /evil-tilde-test && ls /evil-tilde-test && echo visible-inside")
	if !strings.Contains(out, "visible-inside") {
		t.Fatalf("unexpected: write to / failed inside sandbox: %s", out)
	}
	if _, err := os.Stat("/evil-tilde-test"); err == nil {
		t.Fatalf("BREAKOUT: /evil-tilde-test leaked onto the host root")
	}
}

func TestWriteInsideRootWorks(t *testing.T) {
	requireBwrap(t)
	root := t.TempDir()
	out := run(t, root, "echo hi > hello.txt && cat hello.txt")
	if !strings.Contains(out, "hi") {
		t.Fatalf("legit project write failed: %s", out)
	}
}

func TestNetworkEgressDenied(t *testing.T) {
	requireBwrap(t)
	root := t.TempDir()
	out := run(t, root, "curl -s -m 10 -o /dev/null -w '%{http_code}' https://example.com 2>&1; echo; getent hosts example.com 2>&1; echo dns-exit=$?")
	if strings.Contains(out, "200") {
		t.Fatalf("NETWORK LEAK: egress succeeded inside sandbox: %s", out)
	}
}

func TestEnvVarExfilTrickFails(t *testing.T) {
	requireBwrap(t)
	// Classic wrapper trick: smuggle the secret into the command line and
	// try to write it outside the project. Must stay contained.
	root := t.TempDir()
	secret := "TILDE_SECRET_MARKER"
	t.Setenv("TILDE_PROBE", secret)
	outside := filepath.Join(t.TempDir(), "exfil")
	run(t, root, "echo $TILDE_PROBE > "+outside+" 2>&1; echo done")
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("BREAKOUT: env-smuggled write escaped to %s", outside)
	}
}

func TestToolchainVisible(t *testing.T) {
	requireBwrap(t)
	root := t.TempDir()
	out := run(t, root, "go version && bash --version | head -1")
	if !strings.Contains(out, "go version") {
		t.Fatalf("toolchain not usable inside sandbox: %s", out)
	}
}

func TestFailClosedWithoutBwrap(t *testing.T) {
	if Available() {
		t.Skip("bwrap present — nothing to fail closed on")
	}
	c := &Config{Root: t.TempDir()}
	if _, err := c.Command(context.Background(), "echo hi"); err == nil {
		t.Fatal("expected loud refusal without bwrap, got nil error")
	}
}

func TestStatusLineMatchesSpec(t *testing.T) {
	// Spec §2.1 mockup: `Sandbox: ● enforced (OS)` — no suffix.
	t.Setenv("TILDE_ALLOW_NET", "")
	t.Setenv("TILDE_NO_SANDBOX", "")
	if !Available() {
		t.Skip("bwrap missing")
	}
	if got := StatusLine(); got != "● enforced (OS)" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("TILDE_ALLOW_NET", "1")
	if got := StatusLine(); !strings.Contains(got, "NET ALLOWED") {
		t.Fatalf("opt-in must be visible, got %q", got)
	}
}
