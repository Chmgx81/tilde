package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testBackendImage = "registry.example/toolchain@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestBackendResolveTable(t *testing.T) {
	cases := []struct {
		env     string
		want    string
		wantErr bool
	}{
		{"", "bwrap", false},
		{"bwrap", "bwrap", false},
		{"podman", "podman", false},
		{"docker", "", true},
		{"bubblewrap", "", true},
		{"PODMAN", "", true},
		{"bwrap ", "bwrap", false},
	}
	for _, tc := range cases {
		t.Setenv("TILDE_BACKEND", tc.env)
		got, err := ResolveBackend()
		if tc.wantErr {
			if err == nil {
				t.Fatalf("env %q: expected error, got %q", tc.env, got)
			}
			if !strings.Contains(err.Error(), "TILDE_BACKEND") {
				t.Fatalf("env %q: error must name the fix (TILDE_BACKEND), got: %v", tc.env, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("env %q: unexpected error: %v", tc.env, err)
		}
		if got != tc.want {
			t.Fatalf("env %q: got %q want %q", tc.env, got, tc.want)
		}
		// Package func and method agree.
		gotPkg, err := Backend()
		if err != nil || gotPkg != tc.want {
			t.Fatalf("env %q: Backend() = %q,%v want %q", tc.env, gotPkg, err, tc.want)
		}
		gotM, err := (&Config{Root: t.TempDir()}).Backend()
		if err != nil || gotM != tc.want {
			t.Fatalf("env %q: Config.Backend() = %q,%v want %q", tc.env, gotM, err, tc.want)
		}
	}
}

func TestPodmanCommandShape(t *testing.T) {
	if !PodmanAvailable() {
		t.Skip("podman not on PATH — cannot verify command shape here")
	}
	t.Setenv("TILDE_BACKEND", "podman")
	t.Setenv("TILDE_SANDBOX_IMAGE", testBackendImage)
	t.Setenv("TILDE_ALLOW_NET", "")
	t.Setenv("TILDE_NO_SANDBOX", "")

	root := t.TempDir()
	cmd, err := (&Config{Root: root}).Command(context.Background(), "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	if base := filepath.Base(cmd.Path); base != "podman" {
		t.Fatalf("podman backend must exec podman, got %q", cmd.Path)
	}
	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{"run", "--rm", "--read-only", "--network none", testBackendImage, "bash", "-c", "echo hi"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("podman command must contain %q, got %q", want, joined)
		}
	}
	if !containsPair(cmd.Args[1:], "-v", root+":"+root+":rw,Z") {
		t.Fatalf("expected -v mount, got %q", cmd.Args)
	}
	if cmd.Dir != root {
		t.Fatalf("cmd.Dir = %q want %q", cmd.Dir, root)
	}

	// AllowNet lifts only egress.
	cmd, err = (&Config{Root: root, AllowNet: true}).Command(context.Background(), "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(cmd.Args, " "); strings.Contains(joined, "--network none") {
		t.Fatalf("allowNet must omit --network none: %q", joined)
	}
}

func TestPodmanCommandDigestRequired(t *testing.T) {
	t.Setenv("TILDE_BACKEND", "podman")
	t.Setenv("TILDE_NO_SANDBOX", "")
	for _, image := range []string{"", "ubuntu:22.04", "registry.example/toolchain:latest"} {
		t.Setenv("TILDE_SANDBOX_IMAGE", image)
		_, err := (&Config{Root: t.TempDir()}).Command(context.Background(), "echo hi")
		if err == nil {
			t.Fatalf("image %q: expected digest-pin refusal", image)
		}
		if !strings.Contains(err.Error(), "TILDE_SANDBOX_IMAGE") || !strings.Contains(err.Error(), "@sha256:") {
			t.Fatalf("image %q: error must name TILDE_SANDBOX_IMAGE digest fix, got: %v", image, err)
		}
	}
}

func TestPodmanMissingBinaryFailsClosed(t *testing.T) {
	t.Setenv("TILDE_BACKEND", "podman")
	t.Setenv("TILDE_SANDBOX_IMAGE", testBackendImage)
	t.Setenv("TILDE_NO_SANDBOX", "")
	t.Setenv("PATH", t.TempDir()) // hide podman
	if PodmanAvailable() {
		t.Fatal("setup: podman should be hidden")
	}
	if Enforced() {
		t.Fatal("Enforced must be false when podman backend selected but binary missing")
	}
	if _, err := (&Config{Root: t.TempDir()}).Command(context.Background(), "echo hi"); err == nil {
		t.Fatal("expected loud refusal without podman")
	} else if !strings.Contains(err.Error(), "podman") {
		t.Fatalf("refusal must name podman, got: %v", err)
	}
	// Escape hatch still bypasses.
	t.Setenv("TILDE_NO_SANDBOX", "1")
	cmd, err := (&Config{Root: t.TempDir()}).Command(context.Background(), "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	if base := filepath.Base(cmd.Path); base != "bash" {
		t.Fatalf("TILDE_NO_SANDBOX=1 must bypass to bash, got %q", cmd.Path)
	}
}

func TestInvalidBackendFailsClosed(t *testing.T) {
	t.Setenv("TILDE_BACKEND", "docker")
	t.Setenv("TILDE_NO_SANDBOX", "")
	if Enforced() {
		t.Fatal("Enforced must be false on unknown backend")
	}
	if _, err := (&Config{Root: t.TempDir()}).Command(context.Background(), "echo hi"); err == nil {
		t.Fatal("expected error on unknown backend")
	} else if !strings.Contains(err.Error(), "TILDE_BACKEND") {
		t.Fatalf("error must name the fix, got: %v", err)
	}
	if got := StatusLine(); !strings.Contains(got, "TILDE_BACKEND") {
		t.Fatalf("status must surface backend error, got %q", got)
	}
}

func TestBwrapDefaultUnchanged(t *testing.T) {
	for _, env := range []string{"", "bwrap"} {
		t.Setenv("TILDE_BACKEND", env)
		t.Setenv("TILDE_ALLOW_NET", "")
		t.Setenv("TILDE_NO_SANDBOX", "")
		be, err := (&Config{}).Backend()
		if err != nil || be != "bwrap" {
			t.Fatalf("env %q: Backend() = %q,%v want bwrap", env, be, err)
		}
		if !Available() {
			continue
		}
		root := t.TempDir()
		cmd, err := (&Config{Root: root}).Command(context.Background(), "echo hi")
		if err != nil {
			t.Fatal(err)
		}
		if base := filepath.Base(cmd.Path); base != "bwrap" {
			t.Fatalf("env %q: default must exec bwrap, got %q", env, cmd.Path)
		}
		if joined := strings.Join(cmd.Args, " "); !strings.Contains(joined, "--unshare-net") {
			t.Fatalf("env %q: default must carry --unshare-net: %q", env, joined)
		}
	}
}

func TestStatusLineReportsBackend(t *testing.T) {
	t.Setenv("TILDE_NO_SANDBOX", "")
	t.Setenv("TILDE_ALLOW_NET", "")
	if !Available() || !PodmanAvailable() {
		t.Skip("need both bwrap and podman for status comparison")
	}
	t.Setenv("TILDE_BACKEND", "")
	if got := StatusLine(); got != "● enforced (OS)" {
		t.Fatalf("bwrap status must stay spec-exact, got %q", got)
	}
	t.Setenv("TILDE_BACKEND", "podman")
	if got := StatusLine(); !strings.Contains(got, "podman") {
		t.Fatalf("podman status must report backend name, got %q", got)
	}
	// os import guard (used via env in other tests).
	_ = os.Getenv("PATH")
}
