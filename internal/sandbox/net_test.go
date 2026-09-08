package sandbox

import (
	"context"
	"strings"
	"testing"
)

// Egress default: sandbox denies network unless explicitly lifted.
func TestNetDeniedByDefault(t *testing.T) {
	t.Setenv("TILDE_ALLOW_NET", "")
	cmd, err := (&Config{Root: t.TempDir()}).Command(context.Background(), "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "--unshare-net") {
		t.Fatalf("default must carry --unshare-net: %q", joined)
	}
}

// Egress opt-in lifts the sandbox ban per-call or session-wide — the
// policy layer (not the sandbox) still judges the command itself.
func TestNetOptInLiftsBan(t *testing.T) {
	t.Setenv("TILDE_ALLOW_NET", "")
	cmd, err := (&Config{Root: t.TempDir(), AllowNet: true}).Command(context.Background(), "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(cmd.Args, " "); strings.Contains(joined, "--unshare-net") {
		t.Fatalf("per-call opt-in must drop --unshare-net: %q", joined)
	}
	t.Setenv("TILDE_ALLOW_NET", "1")
	if !NetAllowed() {
		t.Fatal("TILDE_ALLOW_NET=1 must report allowed")
	}
	cmd, err = (&Config{Root: t.TempDir()}).Command(context.Background(), "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(cmd.Args, " "); strings.Contains(joined, "--unshare-net") {
		t.Fatalf("session opt-in must drop --unshare-net: %q", joined)
	}
}
