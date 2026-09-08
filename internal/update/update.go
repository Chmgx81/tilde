// Package update keeps a source-installed tilde current: a daily,
// silent, cached freshness check behind a startup notice, plus an
// explicit `tilde update` that pulls, rebuilds, and reinstalls.
//
// Privacy: the only network use is one unauthenticated GitHub API read
// per day (commit SHA of main — no identity, version, or telemetry
// leaves the machine), and only in interactive TUI sessions. Anything
// headless (--prompt, --eval) never phones home; TILDE_NO_UPDATE_CHECK=1
// disables both the check and the notice entirely.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

const (
	// repoURL is the single source of truth for both the freshness
	// check and the update pull.
	repoURL = "https://github.com/Chmgx81/tilde"
	apiURL  = "https://api.github.com/repos/Chmgx81/tilde/commits/main"

	// checkTTL bounds phone-home frequency: at most one API read per
	// day no matter how often tilde starts.
	checkTTL = 24 * time.Hour

	apiTimeout = 10 * time.Second
	gitTimeout = 60 * time.Second

	// buildTimeout covers `go build` on cold cache (first update).
	buildTimeout = 5 * time.Minute

	envOptOut = "TILDE_NO_UPDATE_CHECK"
	// envTarget overrides the reinstall destination. Test hook only:
	// without it the running executable is replaced, which a test
	// must never do to itself.
	envTarget = "TILDE_UPDATE_TARGET"
)

// LocalSHA reports the commit this binary was built from (stamped by
// the Go toolchain for builds inside a git checkout), or "" when
// unknown — tarball builds, `go run`, anything without VCS metadata.
// Empty means uncomparable: checks and notices stay silent rather
// than nagging on zero information.
func LocalSHA() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 7 {
			return s.Value
		}
	}
	return ""
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// stateDir is ~/.tilde, created on demand by writers (never by
// readers — a missing dir means "never checked", not an error).
func stateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".tilde"), nil
}

func checkPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "update-check.json"), nil
}

func installPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "install.json"), nil
}

type checkFile struct {
	CheckedAt int64  `json:"checked_at"`
	Available bool   `json:"available"`
	Remote    string `json:"remote"`
}

type installFile struct {
	Source      string `json:"source"`
	InstalledAt string `json:"installed_at"`
}

// RecordInstall remembers where this binary came from so `tilde
// update` knows what to pull. Called by install.sh after a successful
// install; failures are warnings, never fatal.
func RecordInstall(sourceDir string) error {
	p, err := installPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, _ := json.Marshal(installFile{Source: sourceDir, InstalledAt: time.Now().UTC().Format(time.RFC3339)})
	return os.WriteFile(p, append(data, '\n'), 0o600)
}

func readInstall() (installFile, error) {
	var f installFile
	p, err := installPath()
	if err != nil {
		return f, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil || f.Source == "" {
		return f, fmt.Errorf("unreadable install record")
	}
	return f, nil
}

// Notice returns the cached update notice for the splash screen, or ""
// when there is nothing to say (opted out, never checked, up to date,
// or uncomparable build). Pure file read — never touches the network,
// so startup pays nothing beyond one tiny JSON parse.
func Notice() string {
	if os.Getenv(envOptOut) == "1" {
		return ""
	}
	local := LocalSHA()
	if local == "" {
		return ""
	}
	p, err := checkPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var c checkFile
	if err := json.Unmarshal(data, &c); err != nil || !c.Available || c.Remote == "" {
		return ""
	}
	return noticeFor(local, c)
}

// noticeFor renders the notice from a local SHA and a parsed cache —
// pure, so tests pin every shape without VCS metadata or HOME games.
func noticeFor(local string, c checkFile) string {
	if !c.Available || c.Remote == "" || c.Remote == local {
		return ""
	}
	return fmt.Sprintf("update available (%s → %s) — run tilde update", short(local), short(c.Remote))
}

// RefreshAsync re-checks freshness in the background when the cache is
// stale. Fire-and-forget by design: every failure mode (offline,
// rate-limited, unknown build, opted out) is silent — the next startup
// simply shows whatever the cache last knew.
func RefreshAsync() {
	if os.Getenv(envOptOut) == "1" {
		return
	}
	if LocalSHA() == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
		defer cancel()
		p, err := checkPath()
		if err != nil {
			return
		}
		if data, err := os.ReadFile(p); err == nil {
			var c checkFile
			if json.Unmarshal(data, &c) == nil && time.Since(time.Unix(c.CheckedAt, 0)) < checkTTL {
				return
			}
		}
		remote, err := remoteSHA(ctx)
		if err != nil {
			return
		}
		local := LocalSHA()
		c := checkFile{CheckedAt: time.Now().Unix(), Available: remote != "" && remote != local, Remote: remote}
		if dir, derr := stateDir(); derr == nil {
			_ = os.MkdirAll(dir, 0o700)
			data, _ := json.Marshal(c)
			_ = os.WriteFile(p, append(data, '\n'), 0o600)
		}
	}()
}

// remoteSHA reads main's HEAD commit over the public API. No auth, no
// identity: the request carries nothing but the URL.
func remoteSHA(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("github api: status %d", resp.StatusCode)
	}
	var out struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.SHA) < 7 {
		return "", fmt.Errorf("github api: no sha in response")
	}
	return out.SHA, nil
}

// Run pulls, rebuilds, and reinstalls tilde from its install source.
// Fail-closed throughout: a dirty tree refuses (never stashes or
// resets user work), a failed build or smoke test never touches the
// running binary, and the executable is replaced by atomic rename —
// never a partial overwrite.
func Run() error {
	inst, err := readInstall()
	if err != nil {
		return fmt.Errorf("no install record (~/.tilde/install.json) — reinstall from a fresh clone (%s) with ./install.sh, then retry", repoURL)
	}
	dir := inst.Source
	for _, tool := range []string{"git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s not found — `tilde update` rebuilds from source, so git and Go 1.25+ are required", tool)
		}
	}
	git := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	before, err := git("rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("not a usable git checkout at %s — reinstall from a fresh clone", dir)
	}
	if dirty, err := git("status", "--porcelain"); err != nil || dirty != "" {
		if err != nil {
			return fmt.Errorf("cannot inspect %s: %v", dir, err)
		}
		return fmt.Errorf("source tree at %s has uncommitted changes — commit or stash them, then retry (nothing was pulled)", dir)
	}
	fmt.Printf("tilde: pulling %s in %s\n", repoURL, dir)
	if out, err := git("pull", "--ff-only"); err != nil {
		return fmt.Errorf("pull failed: %s — resolve it with git in %s, then retry", out, dir)
	}
	after, _ := git("rev-parse", "HEAD")
	if after == before {
		fmt.Printf("tilde: already up to date (%s)\n", short(before))
		return nil
	}
	fmt.Println("tilde: rebuilding…")
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", "tilde", ".")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build failed: %s — your installed binary is untouched; fix the tree in %s and retry", strings.TrimSpace(string(out)), dir)
	}
	// Smoke-test the fresh binary before it goes anywhere near PATH.
	smoke := exec.CommandContext(ctx, filepath.Join(dir, "tilde"), "--help")
	if out, err := smoke.CombinedOutput(); err != nil {
		return fmt.Errorf("fresh binary failed its --help smoke test (%v: %s) — installed binary untouched", err, strings.TrimSpace(string(out)))
	}
	target := os.Getenv(envTarget)
	if target == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("cannot locate running binary: %v", err)
		}
		target, err = filepath.EvalSymlinks(exe)
		if err != nil {
			return fmt.Errorf("cannot resolve binary path: %v", err)
		}
	}
	if err := installBinary(filepath.Join(dir, "tilde"), target); err != nil {
		return fmt.Errorf("reinstall failed: %v — fresh binary is at %s", err, filepath.Join(dir, "tilde"))
	}
	_ = RecordInstall(dir)
	fmt.Printf("tilde: updated %s → %s — restart tilde to use it\n", short(before), short(after))
	return nil
}

// installBinary swaps the fresh binary over target atomically: write
// beside it, chmod, rename. A crash mid-copy leaves either the old or
// the new binary — never a half-written executable on PATH.
func installBinary(src, target string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp := target + ".tilde-new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
