// Package verify — the "verify before done" gate.
//
// A coding agent that edits files and then declares success without running
// the project's tests is the most common way it ships a regression. This
// package resolves one authoritative verification command for the project
// and the loop uses it to require (or nudge) a run before a turn that
// changed code is allowed to finish.
//
// Resolution order: $TILDE_VERIFY_CMD, then `.tilde/verify.yaml`
// (`command:`), then conventional project markers. Nothing here executes
// anything — the model runs the command through shell_command, so the
// normal sandbox and policy tiers still apply.
package verify

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Gate modes, set by $TILDE_VERIFY (case-insensitive).
const (
	Off    = "off"    // never nudge
	Warn   = "warn"   // bounded reminder, then finish (default)
	Strict = "strict" // refuse to finish if verification is still outstanding
)

// Mode returns the configured gate mode. Unrecognized values fall back to
// Warn (fail toward verifying).
func Mode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TILDE_VERIFY"))) {
	case Off:
		return Off
	case Strict:
		return Strict
	default:
		return Warn
	}
}

// Command resolves the authoritative verification command for root, or ""
// when the project declares/implies none (gate inactive).
func Command(root string) string {
	if root == "" {
		return ""
	}
	if c := strings.TrimSpace(os.Getenv("TILDE_VERIFY_CMD")); c != "" {
		return c
	}
	if c := configCommand(root); c != "" {
		return c
	}
	return detect(root)
}

type fileConfig struct {
	Command string `yaml:"command"`
}

func configCommand(root string) string {
	data, err := os.ReadFile(filepath.Join(root, ".tilde", "verify.yaml"))
	if err != nil {
		return ""
	}
	var c fileConfig
	if yaml.Unmarshal(data, &c) != nil {
		return ""
	}
	return strings.TrimSpace(c.Command)
}

// detect picks a conventional test command from project markers, first
// match. Deliberately conservative: a wrong guess is worse than no gate.
func detect(root string) string {
	switch {
	case exists(root, "go.mod"):
		return "go test ./..."
	case exists(root, "Cargo.toml"):
		return "cargo test"
	case exists(root, "pytest.ini"), exists(root, "pyproject.toml"), exists(root, "setup.py"):
		return "pytest"
	}
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil && hasTestScript(string(data)) {
		return "npm test"
	}
	if makeHasTest(root) {
		return "make test"
	}
	return ""
}

func exists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}

// hasTestScript is a light-touch check for a non-empty `"test"` script in
// package.json (no full JSON parse needed to answer the one question).
func hasTestScript(body string) bool {
	i := strings.Index(body, `"test"`)
	if i < 0 {
		return false
	}
	rest := strings.TrimLeft(body[i+len(`"test"`):], " \t\r\n")
	rest = strings.TrimPrefix(rest, ":")
	rest = strings.TrimLeft(rest, " \t\r\n")
	return strings.HasPrefix(rest, `"`) && !strings.HasPrefix(rest, `""`)
}

func makeHasTest(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		return false
	}
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(ln, "test:") || strings.HasPrefix(ln, "test :") {
			return true
		}
	}
	return false
}

// Matches reports whether ran is exactly the resolved verify command.
func Matches(verifyCmd, ran string) bool {
	return verifyCmd != "" && strings.TrimSpace(ran) == strings.TrimSpace(verifyCmd)
}

// RanOK reports whether a shell result records a successful exit — the
// `exit=0` line the shell tool emits.
func RanOK(result string) bool {
	return strings.Contains(result, "exit=0")
}
