package sandbox

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestBuildSeatbeltProfile(t *testing.T) {
	p := buildSeatbeltProfile("/home/u/proj", false)
	if !strings.Contains(p, "(deny network*)") {
		t.Fatalf("network must be denied by default:\n%s", p)
	}
	if !strings.Contains(p, "(deny file-write*)") || !strings.Contains(p, `(subpath "/home/u/proj")`) {
		t.Fatalf("writes must be denied then allowlisted to the root:\n%s", p)
	}
	for _, want := range []string{"/private/tmp", "/dev/null", "/dev/stdout", "/dev/stderr"} {
		if !strings.Contains(p, want) {
			t.Fatalf("profile must allow %s:\n%s", want, p)
		}
	}

	pNet := buildSeatbeltProfile("/home/u/proj", true)
	if strings.Contains(pNet, "(deny network*)") {
		t.Fatalf("AllowNet must lift the network denial:\n%s", pNet)
	}

	pEsc := buildSeatbeltProfile(`/tmp/a"b\c`, false)
	if strings.Contains(pEsc, `a"b`) || strings.Contains(pEsc, `b\c"`) {
		t.Fatalf("path quotes/backslashes must be escaped:\n%s", pEsc)
	}
}

func TestCommandDarwinFailsClosedWithoutSandboxExec(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS may have sandbox-exec")
	}
	c := &Config{Root: t.TempDir()}
	if _, err := c.commandDarwin(context.Background(), "echo hi"); err == nil {
		t.Fatal("missing sandbox-exec must fail closed")
	}
}
