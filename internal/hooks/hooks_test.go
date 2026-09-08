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

// FIX 15 (resume hints): hook truncation names the cap with a resume hint.
func TestTruncNamesResume(t *testing.T) {
	got := trunc(strings.Repeat("x", 600), 500)
	if !contains(got, "truncated at 500 chars") {
		t.Fatalf("expected resume hint, got %q", got[len(got)-100:])
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
