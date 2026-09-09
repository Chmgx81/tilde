// Package update keeps a source-installed tilde current: a daily,
// silent, cached freshness check behind a startup notice, plus an
// explicit `tilde update` that pulls, rebuilds, and reinstalls.
//
// Versions are release tags (vMAJOR.MINOR.PATCH): the freshness check
// compares Version below against the newest remote tag and the notice
// names both ends (v0.8.0 → v0.9.0). Commit SHAs survive only as a
// fallback for a tagless remote and for builds that predate tags.
//
// Privacy: the only network use is one unauthenticated GitHub API read
// per day (newest tag — no identity, version, or telemetry leaves the
// machine beyond the request itself), and only in interactive TUI
// sessions. Anything headless (--prompt, --eval) never phones home;
// TILDE_NO_UPDATE_CHECK=1 disables both the check and the notice
// entirely.
package update

import (
	"context"
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

const (
	// repoURL is the single source of truth for both the freshness
	// check and the update pull.
	repoURL = "https://github.com/Chmgx81/tilde"
	apiURL  = "https://api.github.com/repos/Chmgx81/tilde/commits/main"
	tagsURL = "https://api.github.com/repos/Chmgx81/tilde/tags?per_page=100"

	canonicalRemoteName = "origin"
	expectedBranch      = "main"
	expectedFetchRef    = "refs/heads/main"
	expectedRemoteRef   = "refs/remotes/origin/main"

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

// Version is this binary's release version. It is the comparison value for
// remote release tags and must only change when a release is tagged.
const Version = "v0.9.0"

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

// BuildVersion identifies the release line and exact source revision for VCS
// builds. Version remains separate because update checks compare release tags,
// while users need --version to distinguish post-release rebuilds.
func BuildVersion() string {
	if sha := LocalSHA(); sha != "" {
		return Version + "+g" + short(sha)
	}
	return Version
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// parseVersion splits vMAJOR.MINOR.PATCH (leading v optional) or
// reports false — anything else is not a release version.
func parseVersion(s string) (int, int, int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, 0, 0, false
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], true
}

// compareVersion orders two release versions (-1/0/+1). Non-version
// input sorts below any version and compares unequal to each other
// only by string — callers must gate on parseVersion first.
func compareVersion(a, b string) int {
	amaj, amin, apat, aok := parseVersion(a)
	bmaj, bmin, bpat, bok := parseVersion(b)
	if !aok || !bok {
		switch {
		case a == b:
			return 0
		case aok:
			return 1
		case bok:
			return -1
		default:
			return strings.Compare(a, b)
		}
	}
	for _, p := range [][2]int{{amaj, bmaj}, {amin, bmin}, {apat, bpat}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// ghTag is one entry of the GitHub tags API.
type ghTag struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

// newestTag returns the highest release version among tags plus its
// commit SHA, or "" when no tag parses as a version.
func newestTag(tags []ghTag) (tag, sha string) {
	for _, t := range tags {
		if _, _, _, ok := parseVersion(t.Name); !ok {
			continue
		}
		if tag == "" || compareVersion(t.Name, tag) > 0 {
			tag, sha = t.Name, t.Commit.SHA
		}
	}
	return tag, sha
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
	CheckedAt int64 `json:"checked_at"`
	Available bool  `json:"available"`
	// Remote is the newest remote release tag (vX.Y.Z), or a commit
	// SHA when the remote has no parseable tags (tagless fallback).
	Remote string `json:"remote"`
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
	// Version is always stamped (a const, not VCS metadata), so even
	// tarball builds without a build SHA get version notices; `tilde
	// update` on those still fails closed with the reinstall guidance.
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
	localSHA := LocalSHA()
	if !isReleaseVersion(c.Remote) && cachedRemoteIsAncestor(c.Remote, localSHA) {
		return ""
	}
	return noticeFor(Version, localSHA, c)
}

func isReleaseVersion(s string) bool {
	_, _, _, ok := parseVersion(s)
	return ok
}

// cachedRemoteIsAncestor prevents a stale SHA fallback cache from reporting
// an update when the cached remote commit is already contained in this build.
// This is intentionally best-effort: if the source checkout or commit object
// is unavailable, noticeFor keeps the conservative existing behavior.
func cachedRemoteIsAncestor(remote, local string) bool {
	if len(remote) != 40 || len(local) != 40 {
		return false
	}
	if remote == local {
		return true
	}
	install, err := readInstall()
	if err != nil || install.Source == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", install.Source, "merge-base", "--is-ancestor", remote, local)
	return cmd.Run() == nil
}

// noticeFor renders the notice from this binary's version and a parsed
// cache — pure, so tests pin every shape without network or HOME games.
// Version remotes render as versions; a SHA remote (tagless fallback,
// or a cache written before tags existed) renders the old short-SHA
// shape against the local build SHA.
func noticeFor(localVersion, localSHA string, c checkFile) string {
	if !c.Available || c.Remote == "" {
		return ""
	}
	if _, _, _, ok := parseVersion(c.Remote); ok {
		if c.Remote == localVersion {
			return ""
		}
		return fmt.Sprintf("update available (%s → %s) — run tilde update", localVersion, c.Remote)
	}
	if c.Remote == localSHA {
		return ""
	}
	return fmt.Sprintf("update available (%s → %s) — run tilde update", short(localSHA), short(c.Remote))
}

// RefreshAsync re-checks freshness in the background when the cache is
// stale. Fire-and-forget by design: every failure mode (offline,
// rate-limited, opted out) is silent — the next startup simply shows
// whatever the cache last knew. Versions first (tags API), commit SHA
// as fallback when the remote has no parseable tags.
func RefreshAsync() {
	if os.Getenv(envOptOut) == "1" {
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
		if tag, _, err := remoteVersion(ctx); err == nil && tag != "" {
			writeCheck(p, checkFile{CheckedAt: time.Now().Unix(), Available: compareVersion(tag, Version) > 0, Remote: tag})
			return
		}
		remote, err := remoteSHA(ctx)
		if err != nil {
			return
		}
		local := LocalSHA()
		if local == "" {
			return
		}
		writeCheck(p, checkFile{CheckedAt: time.Now().Unix(), Available: remote != "" && remote != local, Remote: remote})
	}()
}

// writeCheck persists one freshness result. Best-effort: a failed
// write only means the next startup re-checks sooner.
func writeCheck(p string, c checkFile) {
	if dir, derr := stateDir(); derr == nil {
		_ = os.MkdirAll(dir, 0o700)
		data, _ := json.Marshal(c)
		_ = os.WriteFile(p, append(data, '\n'), 0o600)
	}
}

// remoteVersion reads the newest release tag over the public API —
// no auth, no identity. Returns ("","", nil) when the remote has no
// parseable version tags (callers fall back to the commit-SHA check).
func remoteVersion(ctx context.Context) (tag, sha string, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", tagsURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("github api: status %d", resp.StatusCode)
	}
	var tags []ghTag
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return "", "", err
	}
	tag, sha = newestTag(tags)
	return tag, sha, nil
}

// remoteSHA reads main's HEAD commit over the public API. No auth, no
// identity: the request carries nothing but the URL. Tagless fallback
// only — versions are the primary channel.
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

// latestLocalTag returns the highest release version among local tags,
// or "" when none parses as a version.
func latestLocalTag(git func(args ...string) (string, error)) string {
	out, err := git("tag", "--list")
	if err != nil {
		return ""
	}
	var best string
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if _, _, _, ok := parseVersion(t); !ok {
			continue
		}
		if best == "" || compareVersion(t, best) > 0 {
			best = t
		}
	}
	return best
}

// verifyTag refuses unsigned/unverifiable release tags before anything
// is pulled or built: a failed `git verify-tag --raw` (missing gnupg,
// unsigned tag) fails closed with the fix named. Empty tag means no
// version tags exist — nothing to verify.
func verifyTag(git func(args ...string) (string, error), tag string) error {
	if tag == "" {
		return nil
	}
	out, err := git("verify-tag", "--raw", tag)
	if err != nil {
		return fmt.Errorf("refusing update: tag %s failed signature verification (%s: %v) — fix: sign release tags (`git tag -s`) and install gnupg so `git verify-tag --raw %s` passes; nothing was pulled", tag, strings.TrimSpace(out), err, tag)
	}
	return nil
}

// needsReleaseVerification reports whether tag represents a release newer
// than this binary. An old unsigned tag must not brick source refreshes or
// rebuilding a stale binary when there is no newer release to install; a
// newer release tag is always verified before pull/build.
func needsReleaseVerification(tag string) bool {
	return tag != "" && compareVersion(tag, Version) > 0
}

// isCanonicalRemote accepts the normal HTTPS clone URL plus the equivalent
// GitHub SSH forms. The repository identity must remain exact; in particular,
// local paths, other hosts, and lookalike repository names are not updater
// sources.
func isCanonicalRemote(raw string) bool {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	raw = strings.TrimSuffix(raw, ".git")
	return raw == repoURL ||
		raw == "git@github.com:Chmgx81/tilde" ||
		raw == "ssh://git@github.com/Chmgx81/tilde"
}

// validateCanonicalRemote makes the update source explicit instead of
// trusting whichever remote or URL the checkout happens to have configured.
func validateCanonicalRemote(git func(args ...string) (string, error)) error {
	// Read the configured URL rather than `remote get-url`: the latter applies
	// url.*.insteadOf rewrites and can make a canonical-looking checkout appear
	// to have a different origin (or hide the configured provenance).
	out, err := git("config", "--get-all", "remote."+canonicalRemoteName+".url")
	if err != nil {
		return fmt.Errorf("refusing update: checkout has no usable %s remote — configure it as %s", canonicalRemoteName, repoURL)
	}
	var urls []string
	for _, line := range strings.Split(out, "\n") {
		if url := strings.TrimSpace(line); url != "" {
			urls = append(urls, url)
		}
	}
	if len(urls) != 1 || !isCanonicalRemote(urls[0]) {
		return fmt.Errorf("refusing update: %s remote %q is not the canonical repository %s", canonicalRemoteName, strings.Join(urls, ", "), repoURL)
	}
	return nil
}

// validateFetchedCommit proves that the commit reported by FETCH_HEAD is the
// commit installed at the exact origin/main remote-tracking ref. Keeping both
// checks prevents a configured upstream or a fetch result from silently
// changing the source selected by the updater.
func validateFetchedCommit(fetchHead, remoteRef string) error {
	if fetchHead == "" || remoteRef == "" {
		return fmt.Errorf("refusing update: fetch did not produce %s and %s", expectedFetchRef, expectedRemoteRef)
	}
	if fetchHead != remoteRef {
		return fmt.Errorf("refusing update: fetched commit %s does not match %s at %s", short(fetchHead), short(remoteRef), expectedRemoteRef)
	}
	return nil
}

func fetchedCommit(git func(args ...string) (string, error)) (string, error) {
	fetchHead, err := git("rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("refusing update: cannot resolve FETCH_HEAD: %s", fetchHead)
	}
	remoteRef, err := git("rev-parse", "--verify", expectedRemoteRef+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("refusing update: cannot resolve %s: %s", expectedRemoteRef, remoteRef)
	}
	if err := validateFetchedCommit(fetchHead, remoteRef); err != nil {
		return "", err
	}
	return fetchHead, nil
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
	if err := validateCanonicalRemote(git); err != nil {
		return err
	}
	branch, err := git("branch", "--show-current")
	if err != nil || branch != expectedBranch {
		return fmt.Errorf("refusing update: source checkout is on branch %q; expected %s", branch, expectedBranch)
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
	if out, err := git("fetch", "--tags", "--prune", canonicalRemoteName, expectedFetchRef+":"+expectedRemoteRef); err != nil {
		return fmt.Errorf("fetch failed: %s — resolve it with git in %s, then retry", out, dir)
	}
	fetched, err := fetchedCommit(git)
	if err != nil {
		return err
	}
	if tag := latestLocalTag(git); needsReleaseVerification(tag) {
		if err := verifyTag(git, tag); err != nil {
			return err
		}
	}
	if out, err := git("merge", "--ff-only", "--no-edit", expectedRemoteRef); err != nil {
		return fmt.Errorf("merge of fetched %s failed: %s — resolve it with git in %s, then retry", expectedRemoteRef, out, dir)
	}
	after, err := git("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || after != fetched {
		return fmt.Errorf("refusing update: checkout HEAD %s does not equal fetched %s", short(after), short(fetched))
	}
	target, err := updateTarget()
	if err != nil {
		return err
	}
	if after == before && binaryRevision(target) == after {
		fmt.Printf("tilde: already up to date (%s)\n", describeVersion(dir, before))
		return nil
	}
	if after == before {
		fmt.Println("tilde: source is current, but the installed binary is stale or missing; rebuilding…")
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
	if err := installBinary(filepath.Join(dir, "tilde"), target); err != nil {
		return fmt.Errorf("reinstall failed: %v — fresh binary is at %s", err, filepath.Join(dir, "tilde"))
	}
	_ = RecordInstall(dir)
	// A successful update has resolved any previous cached SHA notice. Mark
	// the cache current so the next startup cannot repeat stale advice.
	if p, err := checkPath(); err == nil {
		writeCheck(p, checkFile{CheckedAt: time.Now().Unix(), Available: false, Remote: Version})
	}
	fmt.Printf("tilde: updated %s → %s — restart tilde to use it\n", describeVersion(dir, before), describeVersion(dir, after))
	return nil
}

// updateTarget resolves the executable that an update should replace. Tests
// use TILDE_UPDATE_TARGET so they never overwrite the test process itself.
func updateTarget() (string, error) {
	if target := os.Getenv(envTarget); target != "" {
		return target, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot locate running binary: %v", err)
	}
	target, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("cannot resolve binary path: %v", err)
	}
	return target, nil
}

// binaryRevision reads the VCS revision embedded by go build. An empty
// result is deliberately treated as stale: binaries built from tarballs or
// without VCS metadata cannot prove that they match the checkout.
func binaryRevision(path string) string {
	if path == "" {
		return ""
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return strings.TrimSpace(setting.Value)
		}
	}
	return ""
}

// describeVersion names one checkout state for humans, including its commit
// distance from the nearest release tag when available. Never fails loud —
// "unknown" beats a broken update receipt.
func describeVersion(dir, sha string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "git", "-C", dir, "describe", "--tags", "--always").CombinedOutput(); err == nil {
		if tag := strings.TrimSpace(string(out)); tag != "" {
			return tag
		}
	}
	if sha != "" {
		return short(sha)
	}
	return "unknown"
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
