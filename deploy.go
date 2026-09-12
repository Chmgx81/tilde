// deploy.go — `tilde deploy <target>`: run a deployment for the current
// project. User-invoked only; this is deliberately NOT an agent tool, so a
// model can never ship to production on its own.
//
// Vercel is the first target: it shells out to the Vercel CLI with the
// token resolved through the same credential ladder as model providers
// (env var, then the sealed store). The token is passed in the child's
// environment, never argv, so it stays out of `ps` and CI logs.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"tilde/internal/creds"
)

// defaultVercelCLIVersion pins the npx-fetched CLI major. It mirrors the
// version the website deploy workflow uses, so a local deploy and a CI
// deploy behave the same. Override with $TILDE_VERCEL_CLI_VERSION.
const defaultVercelCLIVersion = "59"

// runDeployCmd implements `tilde deploy [target] [--prod|--preview]`.
// With no target it prints usage; `vercel` is the only target today.
func runDeployCmd(args []string) error {
	target, prod, err := parseDeployArgs(args[1:])
	if err != nil {
		return err
	}
	switch target {
	case "", "vercel":
		return deployVercel(prod)
	default:
		return fmt.Errorf("unknown deploy target %q — the only target is vercel", target)
	}
}

// parseDeployArgs is the pure argv parser: it pulls out the target and the
// prod flag, rejecting flag-like targets (which could be smuggled into a
// subcommand) and extra positionals.
func parseDeployArgs(rest []string) (target string, prod bool, err error) {
	for _, a := range rest {
		switch a {
		case "":
			continue
		case "--prod", "-p":
			prod = true
		case "--preview":
			prod = false
		default:
			if strings.HasPrefix(a, "-") {
				return "", false, fmt.Errorf("unknown deploy flag %q (use --prod or --preview)", a)
			}
			if target != "" {
				return "", false, fmt.Errorf("usage: tilde deploy [vercel] [--prod|--preview] — got extra argument %q", a)
			}
			target = strings.ToLower(a)
		}
	}
	return target, prod, nil
}

// vercelDeployArgs is the pure CLI argument list for a deploy.
func vercelDeployArgs(prod bool) []string {
	out := []string{"deploy"}
	if prod {
		out = append(out, "--prod")
	}
	return append(out, "--yes")
}

// resolveVercelToken runs the credential ladder for the Vercel deploy
// token: $VERCEL_TOKEN first, then the sealed store. Returns the token and
// a human source label ("" token means nothing was found).
func resolveVercelToken(store *creds.Store) (token, source string) {
	if k := strings.TrimSpace(os.Getenv("VERCEL_TOKEN")); k != "" {
		return k, "env"
	}
	if store != nil {
		if k, err := store.Get("vercel"); err == nil && strings.TrimSpace(k) != "" {
			return strings.TrimSpace(k), "stored"
		}
	}
	return "", ""
}

// vercelInvocation picks how to run the CLI: a `vercel` already on PATH
// wins (fast, offline), else `npx --yes vercel@<major>` (matches CI).
func vercelInvocation() []string {
	if p, err := exec.LookPath("vercel"); err == nil {
		return []string{p}
	}
	version := strings.TrimSpace(os.Getenv("TILDE_VERCEL_CLI_VERSION"))
	if version == "" {
		version = defaultVercelCLIVersion
	}
	return []string{"npx", "--yes", "vercel@" + version}
}

// deployVercel deploys the current directory to Vercel. A missing token or
// a missing npx/node fails loud with the fix; nothing is invented.
func deployVercel(prod bool) error {
	store, err := openCredStore()
	if err != nil {
		return err
	}
	token, source := resolveVercelToken(store)
	if token == "" {
		return fmt.Errorf("no Vercel token — run `tilde login vercel` (or set $VERCEL_TOKEN) first")
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot determine working dir: %w", err)
	}
	inv := vercelInvocation()
	if _, lerr := exec.LookPath(inv[0]); lerr != nil {
		return fmt.Errorf("cannot run %q: %w — install Node.js (which provides npx) or the Vercel CLI, then retry", inv[0], lerr)
	}
	full := append(append([]string{}, inv[1:]...), vercelDeployArgs(prod)...)
	mode := "preview"
	if prod {
		mode = "production"
	}
	fmt.Fprintf(os.Stderr, "tilde: deploying %s to Vercel (%s) using the %s token…\n", filepath.Base(root), mode, source)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, inv[0], full...)
	cmd.Dir = root
	// The token rides the environment, never argv: argv is visible in
	// `ps` and lands in shell history; the env is not.
	cmd.Env = append(os.Environ(), "VERCEL_TOKEN="+token)
	// Tee stdout so the run still streams while we capture the deployment
	// URL the CLI prints last. Best-effort: an unparseable transcript
	// simply yields no URL line, never a failure.
	var captured strings.Builder
	cmd.Stdout = io.MultiWriter(os.Stdout, &captured)
	cmd.Stderr, cmd.Stdin = os.Stderr, os.Stdin
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("vercel deploy failed (exit %d) — see the output above", ee.ExitCode())
		}
		return fmt.Errorf("vercel deploy failed: %w", err)
	}
	if url := deploymentURL(captured.String()); url != "" {
		fmt.Fprintf(os.Stderr, "tilde: Vercel deploy finished (%s): %s\n", mode, url)
	} else {
		fmt.Fprintf(os.Stderr, "tilde: Vercel deploy finished (%s).\n", mode)
	}
	return nil
}

// deploymentURL picks the deployment URL out of the Vercel CLI transcript:
// the last line that is a bare http(s) URL. Best-effort by design — CLI
// output is not a stable contract, so a miss returns "" and the caller
// just omits the extra line.
func deploymentURL(out string) string {
	url := ""
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "http://") || strings.HasPrefix(ln, "https://") {
			if noSpace := strings.Fields(ln); len(noSpace) > 0 {
				url = noSpace[0]
			}
		}
	}
	return url
}
