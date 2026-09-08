package update

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalSHAShape(t *testing.T) {
	sha := LocalSHA()
	if sha == "" {
		t.Skip("no VCS metadata stamped (tarball-style build)")
	}
	if len(sha) != 40 {
		t.Fatalf("vcs.revision should be a 40-char SHA, got %q", sha)
	}
}

func TestNoticeOptOut(t *testing.T) {
	t.Setenv("TILDE_NO_UPDATE_CHECK", "1")
	if got := Notice(); got != "" {
		t.Fatalf("opt-out must silence the notice, got %q", got)
	}
}

func TestNoticeForShapes(t *testing.T) {
	local := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	remote := "ffffffffffffffffffffffffffffffffffffffff"
	if got := noticeFor(local, checkFile{Available: false, Remote: remote}); got != "" {
		t.Fatalf("unavailable must stay silent, got %q", got)
	}
	if got := noticeFor(local, checkFile{Available: true}); got != "" {
		t.Fatalf("empty remote must stay silent, got %q", got)
	}
	if got := noticeFor(local, checkFile{Available: true, Remote: local}); got != "" {
		t.Fatalf("same SHA must stay silent, got %q", got)
	}
	got := noticeFor(local, checkFile{Available: true, Remote: remote})
	if !strings.Contains(got, "tilde update") || !strings.Contains(got, "aaaaaaa") || !strings.Contains(got, "fffffff") {
		t.Fatalf("notice must name remedy + both ends, got %q", got)
	}
}

func TestRunWithoutInstallRecord(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := Run(); err == nil || !strings.Contains(err.Error(), "install.sh") {
		t.Fatalf("missing record must point at install.sh, got %v", err)
	}
}

func gitOK() bool {
	_, err1 := exec.LookPath("git")
	_, err2 := exec.LookPath("go")
	return err1 == nil && err2 == nil
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestRunFullCycle exercises pull → rebuild → reinstall → smoke test
// against a local fixture remote (file:// clone — no network), ending
// with an executable target binary.
func TestRunFullCycle(t *testing.T) {
	if !gitOK() {
		t.Skip("git and go required")
	}
	work := t.TempDir()
	origin := filepath.Join(work, "origin")
	src := filepath.Join(work, "src")
	target := filepath.Join(work, "bin", "tilde")
	t.Setenv("HOME", filepath.Join(work, "home"))
	t.Setenv("TILDE_UPDATE_TARGET", target)

	// Fixture remote: a real clone of this repo would rebuild for a
	// minute; a tiny Go module proves the whole pipeline instead.
	os.MkdirAll(origin, 0o755)
	gitRun(t, origin, "init", "-b", "main", "-q", ".")
	os.WriteFile(filepath.Join(origin, "go.mod"), []byte("module fixture\n\ngo 1.25.0\n"), 0o644)
	os.WriteFile(filepath.Join(origin, "main.go"), []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"fixture v1\") }\n"), 0o644)
	gitRun(t, origin, "add", "-A")
	gitRun(t, origin, "commit", "-qm", "v1")
	// A main package needs at least the module to build; --help smoke
	// test requires a --help flag — add the smallest one.
	os.WriteFile(filepath.Join(origin, "main.go"), []byte("package main\n\nimport (\n\"flag\"\n\"fmt\"\n)\n\nfunc main() {\nhelp := flag.Bool(\"help\", false, \"x\")\nflag.Parse()\nif *help {\nfmt.Println(\"usage\")\nreturn\n}\nfmt.Println(\"fixture v1\")\n}\n"), 0o644)
	gitRun(t, origin, "add", "-A")
	gitRun(t, origin, "commit", "-qm", "v1b")

	clone := exec.Command("git", "clone", "-q", origin, src)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	writeInstall := func() {
		dir := filepath.Join(work, "home", ".tilde")
		os.MkdirAll(dir, 0o700)
		data, _ := json.Marshal(installFile{Source: src})
		os.WriteFile(filepath.Join(dir, "install.json"), append(data, '\n'), 0o600)
	}
	writeInstall()

	if err := Run(); err != nil {
		t.Fatalf("first run (already current): %v", err)
	}
	// Second commit upstream → the update must pull, rebuild, install.
	os.WriteFile(filepath.Join(origin, "note.txt"), []byte("v2\n"), 0o644)
	gitRun(t, origin, "add", "-A")
	gitRun(t, origin, "commit", "-qm", "v2")
	if err := Run(); err != nil {
		t.Fatalf("update run: %v", err)
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatalf("target binary missing: %v", err)
	}
	if st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("target must be executable, mode %o", st.Mode().Perm())
	}
	out, err := exec.Command(target, "--help").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "usage") {
		t.Fatalf("installed target must pass smoke test: %v\n%s", err, out)
	}
}

func TestRunRefusesDirtyTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	work := t.TempDir()
	repo := filepath.Join(work, "repo")
	t.Setenv("HOME", filepath.Join(work, "home"))
	t.Setenv("TILDE_UPDATE_TARGET", filepath.Join(work, "bin", "tilde"))
	os.MkdirAll(repo, 0o755)
	gitRun(t, repo, "init", "-b", "main", "-q", ".")
	os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x\n"), 0o644)
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "-qm", "v1")
	os.WriteFile(filepath.Join(repo, "f.txt"), []byte("dirty\n"), 0o644) // uncommitted
	dir := filepath.Join(work, "home", ".tilde")
	os.MkdirAll(dir, 0o700)
	data, _ := json.Marshal(installFile{Source: repo})
	os.WriteFile(filepath.Join(dir, "install.json"), append(data, '\n'), 0o600)
	if err := Run(); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty tree must refuse loudly, got %v", err)
	}
}
