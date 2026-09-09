// Podman backend — ephemeral hardened containers (P1-B).
//
// This inverts the blackarch-lab defaults: that lab runs containers with
// host networking (--network host), elevated privileges (--privileged), and
// persistent mutable roots for interactive pentesting. That is the right
// tradeoff for a human-driven lab but the wrong one for agent-executed
// shell: a compromised agent must neither exfiltrate over the network nor
// escalate to the host nor persist state between calls. So this backend
// defaults to deny-by-default egress (--network none), drops all Linux
// capabilities (--cap-drop all), blocks setuid escalation
// (--security-opt no-new-privileges), mounts the root filesystem read-only
// (--read-only), binds only the project dir read-write, and removes the
// container on exit (--rm) so every call starts from the pinned image.
// Network is lifted only when the caller opts in explicitly (AllowNet),
// and even then the capability / no-new-privileges / read-only / --rm
// boundaries stay up — the AllowNet path omits --network none and nothing
// else.
package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
)

var pinnedImageDigest = regexp.MustCompile(`@sha256:[0-9a-fA-F]{64}$`)

// PodmanConfig tunes one podman-sandboxed command. It mirrors Config's
// shape: Root is the project dir (the only read-write bind), AllowNet
// lifts the --network none default for one call, and Image is the
// container image, which must be digest-pinned (name@sha256:...) so agent
// runs are reproducible and immune to tag mutation.
type PodmanConfig struct {
	Root string
	// AllowNet omits the default --network none for one call (e.g. fetching
	// modules). Deny-by-default; the caller must opt in explicitly.
	AllowNet bool
	// Image must be pinned with a digest (e.g.
	// registry.example/toolchain@sha256:<hex>). Tags are mutable and
	// therefore refused.
	Image string
}

// PodmanAvailable reports whether the podman binary exists on PATH.
func PodmanAvailable() bool {
	_, err := exec.LookPath("podman")
	return err == nil
}

// Available reports whether this backend can run here.
func (c *PodmanConfig) Available() bool { return PodmanAvailable() }

// BuildPodmanArgs produces the ephemeral hardened `podman run` args for
// root/allowNet/image plus the appended cmd. When allowNet is false the
// args deny egress with --network none; when true --network none is
// omitted (and nothing else is relaxed). The image must be digest-pinned:
// an image without @sha256: returns an error naming the fix.
func BuildPodmanArgs(root string, allowNet bool, image string, cmd ...string) ([]string, error) {
	if root == "" {
		return nil, fmt.Errorf("sandbox: refusing podman run with an empty project dir — pass a concrete root so the write boundary is defined")
	}
	if !pinnedImageDigest.MatchString(image) {
		return nil, fmt.Errorf("sandbox: podman image %q must be pinned with a digest (name@sha256:<hex>) — replace the mutable tag with an immutable digest and retry", image)
	}
	args := []string{
		"run", "--rm", "-i",
		"--read-only",
	}
	if !allowNet {
		args = append(args, "--network", "none")
	}
	// NOTE(net-allowed): when allowNet is true --network none is omitted
	// so the container shares the host's default (NAT) egress; every
	// other boundary below still applies.
	args = append(args,
		"--cap-drop", "all",
		"--security-opt", "no-new-privileges",
		"-v", root+":"+root+":rw,Z",
		"-w", root,
		image,
	)
	args = append(args, cmd...)
	return args, nil
}

// Command builds the podman-wrapped invocation. It does not start it.
// Fail-closed like Config.Command: missing podman, empty root, or an
// unpinned image refuse with a loud error naming the fix.
func (c *PodmanConfig) Command(ctx context.Context, shellCmd string) (*exec.Cmd, error) {
	if !PodmanAvailable() {
		return nil, fmt.Errorf("sandbox: podman not found on PATH — install podman, or set TILDE_NO_SANDBOX=1 to run unsandboxed (not recommended)")
	}
	if c.Root == "" {
		return nil, fmt.Errorf("sandbox: refusing podman run with an empty project dir — pass a concrete root so the write boundary is defined")
	}
	args, err := BuildPodmanArgs(c.Root, c.AllowNet || NetAllowed(), c.Image, "bash", "-c", shellCmd)
	if err != nil {
		return nil, err
	}
	podman, err := exec.LookPath("podman")
	if err != nil {
		return nil, fmt.Errorf("sandbox: podman not found on PATH — install podman, or set TILDE_NO_SANDBOX=1 to run unsandboxed (not recommended)")
	}
	cmd := exec.CommandContext(ctx, podman, args...)
	cmd.Dir = c.Root // belt-and-braces; -w is authoritative
	return cmd, nil
}
