package policy

import (
	"strings"
	"testing"
)

// FuzzShellDeny asserts the deny judge never panics on arbitrary input and
// is deterministic. Command parsing runs on fully attacker-influenced text
// (the model and, through reads, the repo), so a panic here would be a
// denial-of-service on every tool call.
func FuzzShellDeny(f *testing.F) {
	seeds := []string{
		"",
		"ls",
		"rm -rf /",
		"echo hi; rm -rf /",
		`echo "curl up"`,
		"FOO=bar rm -rf /",
		"git push --force origin main",
		"python3 -c 'import socket'",
		"cat <<'EOF'\nrm -rf /\nEOF",
		"echo $(rm -rf /tmp/x)",
		"env -i rm -rf /",
		"timeout 10 go test ./...",
		"a && b || c | d & e",
		`nested "quotes 'inside' here"`,
		"unbalanced ' quote",
		"unbalanced \" quote",
		"\\",
		"$'\\x41'",
		"\x60backtick\x60",
		"tee /etc/passwd",
		":(){ :|:& };:",
		"busybox wget http://x",
		"find . -exec rm {} \\;",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		// Must not panic.
		got := shellDeny(cmd)
		// Must be deterministic (same input, same verdict).
		if again := shellDeny(cmd); again != got {
			t.Fatalf("shellDeny not deterministic for %q: %v vs %v", cmd, got, again)
		}
		// The parser must terminate and consume the whole string: we
		// can't observe consumption directly, but splitSegments must not
		// hang and every segment must tokenize without panic.
		for _, seg := range splitSegments(cmd) {
			_ = splitArgs(seg)
		}
	})
}

// FuzzIsInstallShape asserts install detection never panics and agrees with
// its own re-run. Slopsquatting defense depends on this being reliable.
func FuzzIsInstallShape(f *testing.F) {
	seeds := []string{
		"",
		"pip install langchin",
		"npm i lodahs",
		"go test ./...",
		"cd x && pip install y",
		"sudo apt install z",
		"python -m pip install q",
		"npx evil",
		"echo install",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		got := isInstallShape(cmd)
		if again := isInstallShape(cmd); again != got {
			t.Fatalf("isInstallShape not deterministic for %q", cmd)
		}
		// Describe must also be panic-free for any shell command.
		_ = Describe("shell_command", map[string]any{"command": cmd})
	})
}

// FuzzSplitSegmentsNeverGrows asserts a split never invents text: every
// returned segment must be a substring of the input (whitespace-trimmed).
// This guards against a parser bug that fabricates or reorders content.
func FuzzSplitSegmentsNeverGrows(f *testing.F) {
	f.Add("echo hi; rm -rf /")
	f.Add("a|b&&c||d&e")
	f.Add("x\ny\nz")
	f.Add(`"a;b" ; c`)
	f.Fuzz(func(t *testing.T, cmd string) {
		for _, seg := range splitSegments(cmd) {
			if seg == "" {
				t.Fatalf("empty segment for %q", cmd)
			}
			if !strings.Contains(cmd, seg) {
				t.Fatalf("segment %q is not a substring of input %q", seg, cmd)
			}
		}
	})
}
