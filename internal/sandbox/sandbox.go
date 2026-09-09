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
	"strings"
)

// Config tunes one sandboxed command.
type Config struct {
	Root string
	// AllowNet lifts the --unshare-net default for one call (e.g. fetching
	// modules). Deny-by-default; the caller must opt in explicitly.
	// TILDE_ALLOW_NET=1 lifts it session-wide instead (loud, visible in
	// the status line); the policy layer still judges each command.
	// Per-host policy allowlists (policies.yaml allow_net) apply at the
	// web_fetch gate only and never lift this ban: shell egress stays
	// all-or-nothing per call / session-wide by construction (bwrap has
	// no per-host egress shape).
	AllowNet bool
}

// Backend names selected via TILDE_BACKEND.
const (
	BackendBwrap  = "bwrap"
	BackendPodman = "podman"
)

// ResolveBackend maps TILDE_BACKEND to a backend name: "" or "bwrap" is
// the default bwrap backend, "podman" selects the podman backend, and
// anything else errors naming the fix.
func ResolveBackend() (string, error) {
	v := strings.TrimSpace(os.Getenv("TILDE_BACKEND"))
	switch v {
	case "", BackendBwrap:
		return BackendBwrap, nil
	case BackendPodman:
		return BackendPodman, nil
	default:
		return "", fmt.Errorf("sandbox: unknown backend %q — set TILDE_BACKEND to \"bwrap\" or \"podman\" (empty means bwrap)", v)
	}
}

// Backend resolves this command's sandbox backend (see ResolveBackend).
func Backend() (string, error) { return ResolveBackend() }

// Backend resolves this command's sandbox backend (see ResolveBackend).
func (c *Config) Backend() (string, error) { return ResolveBackend() }

// Disabled reports whether the TILDE_NO_SANDBOX escape hatch is set.
func Disabled() bool { return os.Getenv("TILDE_NO_SANDBOX") == "1" }

// Available reports whether bwrap exists on PATH.
func Available() bool {
	_, err := exec.LookPath("bwrap")
	return err == nil
}

// Enforced reports whether commands will actually be sandboxed.
// It follows the selected backend: bwrap checks for bwrap, podman checks
// for podman. An unknown backend fails closed (false); StatusLine and
// Command surface the error naming the fix.
func Enforced() bool {
	if Disabled() {
		return false
	}
	be, err := ResolveBackend()
	if err != nil {
		return false
	}
	if be == BackendPodman {
		return PodmanAvailable()
	}
	return Available()
}

// Command builds the sandbox-wrapped bash invocation. It does not start it.
// The backend comes from Backend(): the default bwrap path is unchanged,
// while "podman" delegates to the podman backend (BuildPodmanArgs via
// PodmanConfig) with the digest-pinned image from TILDE_SANDBOX_IMAGE.
func (c *Config) Command(ctx context.Context, shellCmd string) (*exec.Cmd, error) {
	if Disabled() {
		cmd := exec.CommandContext(ctx, "bash", "-c", shellCmd)
		cmd.Dir = c.Root
		return cmd, nil
	}
	be, err := c.Backend()
	if err != nil {
		return nil, err
	}
	if be == BackendPodman {
		image := os.Getenv("TILDE_SANDBOX_IMAGE")
		if !strings.Contains(image, "@sha256:") {
			return nil, fmt.Errorf("sandbox: podman backend needs TILDE_SANDBOX_IMAGE pinned with a digest (name@sha256:<hex>) — set TILDE_SANDBOX_IMAGE to a digest-pinned image and retry (got %q)", image)
		}
		if c.Root != "" && filepath.Clean(c.Root) == "/" {
			return nil, fmt.Errorf("sandbox: refusing to run with / as the project dir — that binds the whole host read-write. cd into the project and retry")
		}
		pc := &PodmanConfig{Root: c.Root, AllowNet: c.AllowNet, Image: image}
		return pc.Command(ctx, shellCmd)
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
// The podman backend reports its own name (`● enforced (podman)`).
func StatusLine() string {
	if Disabled() {
		return "○ sandbox disabled (TILDE_NO_SANDBOX=1)"
	}
	be, err := ResolveBackend()
	if err != nil {
		return "✗ " + err.Error()
	}
	if be == BackendPodman {
		if !PodmanAvailable() {
			return "✗ podman missing — shell calls will refuse to run"
		}
		if NetAllowed() {
			return "● enforced (podman) + NET ALLOWED via TILDE_ALLOW_NET=1"
		}
		return "● enforced (podman)"
	}
	switch {
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
