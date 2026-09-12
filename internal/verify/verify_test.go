package verify

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModeParsing(t *testing.T) {
	cases := map[string]string{
		"": Warn, "warn": Warn, "WARN": Warn, " off ": Off,
		"off": Off, "strict": Strict, "STRICT": Strict, "bogus": Warn,
	}
	for in, want := range cases {
		t.Setenv("TILDE_VERIFY", in)
		if got := Mode(); got != want {
			t.Errorf("Mode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCommandResolution(t *testing.T) {
	t.Setenv("TILDE_VERIFY_CMD", "make check")
	if got := Command(t.TempDir()); got != "make check" {
		t.Fatalf("env override = %q", got)
	}
	t.Setenv("TILDE_VERIFY_CMD", "")

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644)
	if got := Command(dir); got != "go test ./..." {
		t.Fatalf("go.mod = %q", got)
	}
	// An explicit .tilde/verify.yaml wins over markers.
	os.MkdirAll(filepath.Join(dir, ".tilde"), 0o755)
	os.WriteFile(filepath.Join(dir, ".tilde", "verify.yaml"), []byte("command: ./scripts/check.sh\n"), 0o644)
	if got := Command(dir); got != "./scripts/check.sh" {
		t.Fatalf("config = %q", got)
	}
	if got := Command(t.TempDir()); got != "" {
		t.Fatalf("no markers = %q, want empty", got)
	}
	if got := Command(""); got != "" {
		t.Fatalf("empty root = %q, want empty", got)
	}

	pm := t.TempDir()
	os.WriteFile(filepath.Join(pm, "package.json"), []byte(`{"scripts":{"test":"vitest"}}`), 0o644)
	if got := Command(pm); got != "npm test" {
		t.Fatalf("package.json = %q", got)
	}
	pe := t.TempDir()
	os.WriteFile(filepath.Join(pe, "package.json"), []byte(`{"scripts":{"test":""}}`), 0o644)
	if got := Command(pe); got != "" {
		t.Fatalf("empty test script = %q, want empty", got)
	}

	mk := t.TempDir()
	os.WriteFile(filepath.Join(mk, "Makefile"), []byte("build:\n\techo b\ntest:\n\techo t\n"), 0o644)
	if got := Command(mk); got != "make test" {
		t.Fatalf("Makefile = %q", got)
	}
}

func TestMatchesAndRanOK(t *testing.T) {
	if !Matches("go test ./...", "  go test ./...  ") {
		t.Fatal("trimmed equality must match")
	}
	if Matches("go test ./...", "go test ./... -run X") {
		t.Fatal("a different invocation must not count as verified")
	}
	if Matches("", "") {
		t.Fatal("empty verify command must never match")
	}
	if !RanOK("exit=0\nok") {
		t.Fatal("exit=0 must read as success")
	}
	if RanOK("exit=1\nboom") {
		t.Fatal("exit=1 must not read as success")
	}
}
