package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBeforeAllowAndDeny(t *testing.T) {
	c := Config{Before: map[string][]string{
		"write_file": {"exit 0"},
		"rm":         {"echo nope >&2; exit 3"},
	}}
	if err := c.RunBefore(context.Background(), "write_file", "{}"); err != nil {
		t.Fatalf("allow hook blocked: %v", err)
	}
	if err := c.RunBefore(context.Background(), "rm", "{}"); err == nil {
		t.Fatal("deny hook must block")
	} else if got := err.Error(); !contains(got, "nope") {
		t.Fatalf("stderr must become the reason: %q", got)
	}
}

func TestWildcardRunsForAll(t *testing.T) {
	c := Config{Before: map[string][]string{"*": {"echo tool=$TILDE_TOOL"}}}
	if err := c.RunBefore(context.Background(), "anything", "{}"); err != nil {
		t.Fatal(err)
	}
}

func TestAfterNotesAppended(t *testing.T) {
	c := Config{After: map[string][]string{
		"write_file": {"echo needs-format", "exit 1"},
	}}
	notes := c.RunAfter(context.Background(), "write_file", "{}", "ok")
	if !contains(notes, "needs-format") || !contains(notes, "failed") {
		t.Fatalf("got %q", notes)
	}
}

func TestAfterCleanIsSilent(t *testing.T) {
	c := Config{After: map[string][]string{"write_file": {"exit 0"}}}
	if notes := c.RunAfter(context.Background(), "write_file", "{}", "ok"); notes != "" {
		t.Fatalf("got %q", notes)
	}
}

func TestNilConfigSafe(t *testing.T) {
	var c *Config
	if err := c.RunBefore(context.Background(), "x", "{}"); err != nil {
		t.Fatal(err)
	}
	if notes := c.RunAfter(context.Background(), "x", "{}", "ok"); notes != "" {
		t.Fatal(notes)
	}
}

func TestLoadMerge(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "p.yaml"), []byte("before:\n  write_file:\n    - exit 0\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "u.yaml"), []byte("after:\n  \"*\":\n    - exit 0\n"), 0o644)
	p, err := LoadFile(filepath.Join(dir, "p.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	u, err := LoadFile(filepath.Join(dir, "u.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := Merge(p, u)
	if len(m.Before["write_file"]) != 1 || len(m.After["*"]) != 1 {
		t.Fatalf("%+v", m)
	}
	if _, err := LoadFile(filepath.Join(dir, "missing.yaml")); err != nil {
		t.Fatal("missing file must be empty, not error")
	}
	if _, err := LoadFile("/dev/null"); err != nil {
		// /dev/null reads empty → yaml parses to empty config, no error
		t.Logf("note: %v", err)
	}
}

func TestHookSeesToolEnv(t *testing.T) {
	c := Config{Before: map[string][]string{"write_file": {"echo denied-$TILDE_TOOL >&2; exit 1"}}}
	err := c.RunBefore(context.Background(), "write_file", `{"a":1}`)
	if err == nil || !contains(err.Error(), "denied-write_file") {
		t.Fatalf("hook must see TILDE_TOOL: %v", err)
	}
}

func TestHookArgsRedactSecrets(t *testing.T) {
	c := Config{Before: map[string][]string{
		"probe": {"echo \"$TILDE_ARGS_JSON\" >&2; exit 1"},
	}}
	err := c.RunBefore(context.Background(), "probe", `{"token":"sk-ant-abcdefghij1234567890abcdef"}`)
	if err == nil || contains(err.Error(), "sk-ant-abcdefghij1234567890abcdef") {
		t.Fatalf("hook args must not expose secrets: %v", err)
	}
	if !contains(err.Error(), "REDACTED") {
		t.Fatalf("redaction marker missing: %v", err)
	}
}

// FIX 15 (resume hints): hook truncation names the cap with a resume hint.
func TestTruncNamesResume(t *testing.T) {
	got := trunc(strings.Repeat("x", 600), 500)
	if !contains(got, "truncated at 500 chars") {
		t.Fatalf("expected resume hint, got %q", got[len(got)-100:])
	}
}

// P0-3 sandbox: hook env must be stripped to a safe baseline.
func TestHookEnvStripped(t *testing.T) {
	t.Setenv("TILDE_TEST_SECRET_TOKEN", "should-not-leak")
	t.Setenv("MY_API_KEY", "should-not-leak")
	c := Config{Before: map[string][]string{
		"probe": {"echo \"tok=${TILDE_TEST_SECRET_TOKEN:-empty} key=${MY_API_KEY:-empty} tool=$TILDE_TOOL\"; exit 1"},
	}}
	err := c.RunBefore(context.Background(), "probe", "{}")
	if err == nil {
		t.Fatal("probe hook must block to expose output")
	}
	msg := err.Error()
	if !contains(msg, "tok=empty") || !contains(msg, "key=empty") {
		t.Fatalf("secrets leaked into hook env: %q", msg)
	}
	if !contains(msg, "tool=probe") {
		t.Fatalf("TILDE_TOOL must survive: %q", msg)
	}
}

// P0-3 sandbox: combined output capped at 32KB with truncation note.
func TestHookOutputCapped(t *testing.T) {
	c := Config{Before: map[string][]string{
		"big": {"head -c 100000 /dev/zero | tr '\\0' 'x'; exit 1"},
	}}
	err := c.RunBefore(context.Background(), "big", "{}")
	if err == nil {
		t.Fatal("big hook must block")
	}
	// runHook itself must cap at 32KB + note before the 500-char trunc.
	out, _ := runHook(context.Background(), "big", "{}", "", "head -c 100000 /dev/zero | tr '\\0' 'x'")
	if len(out) > maxHookOutput+256 {
		t.Fatalf("output not capped: %d bytes", len(out))
	}
	if !contains(out, "hooks sandbox output cap") {
		t.Fatalf("cap note must name the fix: %q", out[len(out)-120:])
	}
}

// P0-3 sandbox: TrustHash stable + sensitive to content.
func TestTrustHashStable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hook.sh")
	os.WriteFile(p, []byte("echo hi\n"), 0o644)
	h1, err := TrustHash(p)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := TrustHash(p)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("hash must be stable 64-hex: %q %q", h1, h2)
	}
	os.WriteFile(p, []byte("echo changed\n"), 0o644)
	h3, err := TrustHash(p)
	if err != nil {
		t.Fatal(err)
	}
	if h3 == h1 {
		t.Fatal("hash must change with content")
	}
	if _, err := TrustHash(filepath.Join(dir, "missing.sh")); err == nil {
		t.Fatal("missing file must error")
	}
}

// P0-3 sandbox: audit lines scrubbed of key-like tokens.
func TestHookAuditScrubbed(t *testing.T) {
	c := Config{After: map[string][]string{
		"leak": {"echo sk-ant-abcdefghij1234567890abcdef; exit 0"},
	}}
	notes := c.RunAfter(context.Background(), "leak", "{}", "ok")
	if contains(notes, "sk-ant-abcdefghij1234567890abcdef") {
		t.Fatalf("secret leaked into audit line: %q", notes)
	}
	if !contains(notes, "<<REDACTED:anthropic>>") {
		t.Fatalf("expected redaction marker: %q", notes)
	}
}

// Regression: a sandbox wrapper that refuses to build (here: `/` as the
// project root) returns a nil *exec.Cmd. runHook must surface that as an
// error instead of dereferencing the nil command.
func TestSandboxBuildFailureDoesNotPanic(t *testing.T) {
	t.Setenv("TILDE_NO_SANDBOX", "") // empty means "not disabled"
	before := &Config{Root: "/", Before: map[string][]string{"read_file": {"true"}}}
	if err := before.RunBefore(context.Background(), "read_file", "{}"); err == nil {
		t.Fatal("an unbuildable sandboxed hook must report an error")
	}
	after := &Config{Root: "/", After: map[string][]string{"read_file": {"true"}}}
	if notes := after.RunAfter(context.Background(), "read_file", "{}", "ok"); notes == "" {
		t.Fatal("an unbuildable sandboxed after-hook must report a note")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
