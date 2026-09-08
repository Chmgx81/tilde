// Package sandbox — OS-level isolation for shell calls (docs/Plan.md Phase 2).
//
// Every shell_command runs under bubblewrap on Linux: the sandbox root is a
// fresh tmpfs, the toolchain is bound read-only, only the project dir is
// bound read-write, and network egress is denied by default (--unshare-net).
// Filesystem-only isolation is not sufficient (a compromised agent can still
// exfiltrate over the network), so both boundaries are enforced together.
//
// Fail-closed: if bwrap is missing, commands refuse to run with a loud
// error naming the fix — unless TILDE_NO_SANDBOX=1 is set, which disables
// the sandbox with an explicit warning line on every call.
package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Config tunes one sandboxed command.
type Config struct {
	Root string
	// AllowNet lifts the --unshare-net default for one call (e.g. fetching
	// modules). Deny-by-default; the caller must opt in explicitly.
	// TILDE_ALLOW_NET=1 lifts it session-wide instead (loud, visible in
	// the status line); the policy layer still judges each command.
	AllowNet bool
}

// Disabled reports whether the TILDE_NO_SANDBOX escape hatch is set.
func Disabled() bool { return os.Getenv("TILDE_NO_SANDBOX") == "1" }

// Available reports whether bwrap exists on PATH.
func Available() bool {
	_, err := exec.LookPath("bwrap")
	return err == nil
}

// Enforced reports whether commands will actually be sandboxed.
func Enforced() bool { return !Disabled() && Available() }

// Command builds the bwrap-wrapped bash invocation. It does not start it.
func (c *Config) Command(ctx context.Context, shellCmd string) (*exec.Cmd, error) {
	if Disabled() {
		cmd := exec.CommandContext(ctx, "bash", "-c", shellCmd)
		cmd.Dir = c.Root
		return cmd, nil
	}
	if !Available() {
		return nil, fmt.Errorf("sandbox: bwrap not found on PATH — install bubblewrap (e.g. `apt install bubblewrap`), or set TILDE_NO_SANDBOX=1 to run unsandboxed (not recommended)")
	}
	if c.Root == "" {
		return nil, fmt.Errorf("sandbox: refusing to run with an empty project dir — pass a concrete root so the write boundary is defined")
	}
	if filepath.Clean(c.Root) == "/" {
		return nil, fmt.Errorf("sandbox: refusing to run with / as the project dir — that binds the whole host read-write. cd into the project and retry")
	}
	args := []string{
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/lib", "/lib",
		"--ro-bind", "/lib64", "/lib64",
	}
	// Deliberate tradeoff, narrowed: the sandbox needs name resolution
	// and TLS roots (notably when TILDE_ALLOW_NET=1 lifts the net ban),
	// so bind only the /etc entries that serve that — never the whole
	// /etc. /etc/passwd and /etc/group stay world-readable outside the
	// sandbox too (the agent already runs as the user), so no privilege
	// boundary is crossed by exposing them read-only; everything else
	// under /etc (shadow, sudoers.d, machine-id, …) stays invisible.
	// Missing sources are skipped: bwrap fails on absent bind sources.
	for _, p := range []string{
		"/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf",
		"/etc/passwd", "/etc/group", "/etc/services",
		"/etc/ssl", "/etc/pki", "/etc/ca-certificates",
	} {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--ro-bind", p, p)
		}
	}
	// Optional toolchain prefixes: read-only, only when present (bwrap
	// fails on missing sources). Same trust as /usr — user-runnable
	// binaries the agent could already invoke outside the sandbox.
	for _, dir := range []string{"/usr/local", "/opt"} {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			args = append(args, "--ro-bind", dir, dir)
		}
	}
	// Own PID namespace: bwrap reaps the tree, and killing the task kills
	// everything in it — daemonized grandchildren (setsid, double-fork)
	// cannot outlive the task to exfiltrate later.
	args = append(args, "--proc", "/proc", "--dev", "/dev",
		"--tmpfs", "/tmp", "--tmpfs", "/home/sb",
		"--bind", c.Root, c.Root, "--chdir", c.Root,
		"--die-with-parent", "--unshare-pid", "--clearenv",
		"--setenv", "PATH", "/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin",
		"--setenv", "HOME", "/home/sb", "--setenv", "GOCACHE", "/home/sb/.cache")
	if !c.AllowNet && !NetAllowed() {
		args = append(args, "--unshare-net")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		return nil, fmt.Errorf("sandbox: bash not found on PATH — cannot run shell commands without it")
	}
	args = append(args, bash, "-c", shellCmd)
	cmd := exec.CommandContext(ctx, "bwrap", args...)
	cmd.Dir = c.Root // belt-and-braces; bwrap --chdir is authoritative
	return cmd, nil
}

// StatusLine renders the sandbox state for splash/status surfaces.
// Default form matches the spec §2.1 mockup exactly
// (`Sandbox: ● enforced (OS)`); only the exceptional opt-in state spends
// extra ink — the default already says it in the safety prose above.
func StatusLine() string {
	switch {
	case Disabled():
		return "○ sandbox disabled (TILDE_NO_SANDBOX=1)"
	case Available():
		if NetAllowed() {
			return "● enforced (OS) + NET ALLOWED via TILDE_ALLOW_NET=1"
		}
		return "● enforced (OS)"
	default:
		return "✗ bwrap missing — shell calls will refuse to run"
	}
}

// NetAllowed reports the session-wide egress opt-in. Policy still judges
// the COMMAND (curl/wget always deny); this lifts only the sandbox's
// network ban so allowed commands (go build, npm install) can fetch.
func NetAllowed() bool { return os.Getenv("TILDE_ALLOW_NET") == "1" }
