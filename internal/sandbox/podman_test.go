package sandbox

import (
	"strings"
	"testing"
)

const testPodmanImage = "registry.example/toolchain@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestPodmanDigestRequired(t *testing.T) {
	for _, image := range []string{
		"",
		"ubuntu:22.04",
		"registry.example/toolchain:latest",
		"registry.example/toolchain@sha1:abc",
		"registry.example/toolchain@sha256:short",
		"registry.example/toolchain@sha256:" + strings.Repeat("g", 64),
	} {
		if _, err := BuildPodmanArgs("/proj", false, image, "echo", "hi"); err == nil {
			t.Fatalf("expected digest-pin refusal for image %q, got nil error", image)
		} else if !strings.Contains(err.Error(), "@sha256:") {
			t.Fatalf("error must name the digest fix, got: %v", err)
		}
	}
	if _, err := BuildPodmanArgs("/proj", false, testPodmanImage, "echo", "hi"); err != nil {
		t.Fatalf("pinned image must be accepted, got: %v", err)
	}
}

func TestPodmanNetworkNoneDefault(t *testing.T) {
	args, err := BuildPodmanArgs("/proj", false, testPodmanImage, "echo", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if !containsPair(args, "--network", "none") {
		t.Fatalf("default must deny egress with --network none, got %q", args)
	}
}

func TestPodmanNetAllowedVariant(t *testing.T) {
	args, err := BuildPodmanArgs("/proj", true, testPodmanImage, "echo", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if containsPair(args, "--network", "none") {
		t.Fatalf("allowNet must omit --network none, got %q", args)
	}
	// Net opt-in relaxes only egress: every other boundary stays up.
	for _, want := range []string{"--rm", "--read-only", "no-new-privileges", "all"} {
		if !contains(args, want) {
			t.Fatalf("allowNet must keep hardened flag %q, got %q", want, args)
		}
	}
}

func TestPodmanVolumeMountShape(t *testing.T) {
	args, err := BuildPodmanArgs("/proj", false, testPodmanImage, "echo", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if !containsPair(args, "-v", "/proj:/proj:rw,Z") {
		t.Fatalf("expected -v /proj:/proj:rw,Z mount, got %q", args)
	}
	if !containsPair(args, "-w", "/proj") {
		t.Fatalf("expected -w /proj workdir, got %q", args)
	}
	// Ephemeral + hardened + trailing command shape.
	for _, want := range []string{"--rm", "-i", "--read-only", "--cap-drop", "all", "--security-opt", "no-new-privileges"} {
		if !contains(args, want) {
			t.Fatalf("expected hardened flag %q, got %q", want, args)
		}
	}
	if got := args[len(args)-3:]; got[0] != testPodmanImage || got[1] != "echo" || got[2] != "hi" {
		// Image must precede the appended command: [..., image, echo, hi].
		t.Fatalf("cmd must be appended after image, tail got %q (full %q)", got, args)
	}
}
