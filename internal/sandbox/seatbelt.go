// macOS sandbox backend: Apple Seatbelt via sandbox-exec. Not build-tagged
// because the dispatch is runtime.GOOS; on other platforms these are inert.
//
// Mirrors the bwrap contract on Linux: reads allowed, writes confined to the
// project root plus private temp dirs, network denied unless explicitly
// opted in (TILDE_ALLOW_NET=1 / AllowNet). Fail-closed: if sandbox-exec is
// missing, shell commands refuse rather than run unsandboxed.
package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// seatbeltAvailable reports whether sandbox-exec exists on PATH.
func seatbeltAvailable() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, err := exec.LookPath("sandbox-exec")
	return err == nil
}

// seatbeltQuote escapes a path for a Seatbelt profile string literal.
func seatbeltQuote(p string) string {
	p = strings.ReplaceAll(p, `\`, `\\`)
	return strings.ReplaceAll(p, `"`, `\"`)
}

// buildSeatbeltProfile renders the Seatbelt policy for one shell call: allow
// by default (reads, exec, syscalls), deny network unless allowNet, and
// confine writes to the project root and private temp dirs. A pure function
// so it is unit-testable without macOS.
func buildSeatbeltProfile(root string, allowNet bool) string {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n")
	if !allowNet {
		b.WriteString("(deny network*)\n")
	}
	b.WriteString("(deny file-write*)\n")
	b.WriteString("(allow file-write*\n")
	for _, p := range []string{
		root,
		"/private/tmp",
		"/private/var/tmp",
		"/private/var/folders",
		"/dev/null",
		"/dev/stdout",
		"/dev/stderr",
		"/dev/tty",
	} {
		if p == "" {
			continue
		}
		fmt.Fprintf(&b, "  (subpath \"%s\")\n", seatbeltQuote(p))
	}
	b.WriteString(")\n")
	return b.String()
}

// commandDarwin builds the sandbox-exec invocation for one shell command.
func (c *Config) commandDarwin(ctx context.Context, shellCmd string) (*exec.Cmd, error) {
	exe, err := exec.LookPath("sandbox-exec")
	if err != nil {
		return nil, fmt.Errorf("sandbox: sandbox-exec not found — macOS sandbox unavailable (install the Xcode Command Line Tools), or set TILDE_NO_SANDBOX=1 to run unsandboxed (not recommended)")
	}
	if c.Root == "" {
		return nil, fmt.Errorf("sandbox: refusing to run with an empty project dir — pass a concrete root so the write boundary is defined")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		return nil, fmt.Errorf("sandbox: bash not found on PATH — cannot run shell commands without it")
	}
	profile := buildSeatbeltProfile(c.Root, c.AllowNet || NetAllowed())
	cmd := exec.CommandContext(ctx, exe, "-p", profile, bash, "-c", shellCmd)
	cmd.Dir = c.Root
	cmd.Env = os.Environ()
	return cmd, nil
}

// seatbeltStatusLine renders the macOS sandbox state for splash/doctor.
func seatbeltStatusLine() string {
	if !seatbeltAvailable() {
		return "✗ sandbox-exec missing — shell calls will refuse to run"
	}
	if NetAllowed() {
		return "● enforced (seatbelt) + NET ALLOWED via TILDE_ALLOW_NET=1"
	}
	return "● enforced (seatbelt)"
}
