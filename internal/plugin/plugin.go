// Package plugin implements the P3-C plugin manifest v1 (local-only install).
//
// A plugin is a local directory the user has already vetted. It carries a
// manifest file (tilde-plugin.yaml) declaring which files make up the
// plugin; Install copies exactly those files into pluginHome/<name>/ and
// pins a lockfile with the sha256 of every installed file. Verify re-hashes
// the installed tree against the lockfile. Reinstall (--upgrade semantics)
// wipes the installed dir and re-pins from source.
//
// Trust contract (mirrors internal/trust explicit opt-in, no auto-trust):
//   - Local-only: no network, no git clone, no subprocess execution. This
//     package never execs anything it installs; skills/hooks/mcp files are
//     data until some other component (owned by the caller) decides to read
//     or run them.
//   - No auto-install: there is deliberately no API that installs as a side
//     effect of loading. LoadManifest and Verify are read-only. The caller
//     (owner wiring, e.g. a CLI command) must obtain explicit user opt-in
//     before calling Install or Reinstall.
//   - The source dir is assumed already vetted by the user, but manifest
//     paths are still contained: every listed file must be a relative path
//     inside the source dir (no absolute paths, no `..` escape, symlinks
//     must resolve inside), must exist as a regular file, and skill (.md)
//     files are capped at 32 files / 64KB each.
package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest file and lockfile names.
const (
	// ManifestFileName is the manifest file read from the source dir.
	ManifestFileName = "tilde-plugin.yaml"
	// ManifestFileNameLegacy is the pre-v0.11 typo name, still accepted
	// read-only so existing installs keep verifying.
	ManifestFileNameLegacy = "tilder-plugin.yaml"
	// LockFileName is the integrity lockfile written into the installed dir.
	LockFileName = "tilde-plugin.lock.json"
	// LockFileNameLegacy is the old lockfile name, accepted on verify.
	LockFileNameLegacy = "tilder-plugin.lock.json"
)

// Skill caps, carried over from the deferred marketplace notes.
const (
	// MaxSkillFiles caps how many .md skills one plugin may list.
	MaxSkillFiles = 32
	// MaxSkillBytes caps each skill file at 64KB.
	MaxSkillBytes = 64 * 1024
)

var (
	// nameRe constrains plugin names to lowercase letters, digits, hyphens.
	nameRe = regexp.MustCompile(`^[a-z0-9-]+$`)
	// versionRe requires a strict X.Y.Z numeric version.
	versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

// Manifest is the parsed tilde-plugin.yaml.
type Manifest struct {
	Name        string   `yaml:"name"`
	Version     string   `yaml:"version"`
	Description string   `yaml:"description"`
	Skills      []string `yaml:"skills"`
	Hooks       string   `yaml:"hooks,omitempty"`
	MCP         string   `yaml:"mcp,omitempty"`
}

// Lockfile pins the exact installed content: relpath -> sha256 hex.
type Lockfile struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Files   map[string]string `json:"files"`
}

// LoadManifest reads dir/tilde-plugin.yaml, parses it, and validates it.
// Read-only: loading never installs anything.
func LoadManifest(dir string) (*Manifest, error) {
	path := filepath.Join(dir, ManifestFileName)
	data, err := os.ReadFile(path)
	if err != nil && os.IsNotExist(err) {
		// Legacy typo name from before the rename — read-only compat so
		// existing checkouts keep loading; new installs pin the new name.
		path = filepath.Join(dir, ManifestFileNameLegacy)
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("plugin: cannot read manifest %q: %v — create %s with name/version/description/skills", path, err, ManifestFileName)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("plugin: bad manifest YAML in %q: %v — fix the name:/version:/skills: lines", path, err)
	}
	if err := m.Validate(dir); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks the manifest structurally and confirms every listed file
// exists inside dir as a regular file with no `..` escape.
func (m *Manifest) Validate(dir string) error {
	if m == nil {
		return fmt.Errorf("plugin: nil manifest")
	}
	if !nameRe.MatchString(m.Name) {
		return fmt.Errorf("plugin: bad name %q — use lowercase letters, digits, and hyphens only (e.g. my-plugin)", m.Name)
	}
	if !versionRe.MatchString(m.Version) {
		return fmt.Errorf("plugin: bad version %q — use a strict X.Y.Z number (e.g. 1.2.3)", m.Version)
	}
	if strings.TrimSpace(m.Description) == "" {
		return fmt.Errorf("plugin %q: missing description — add one line saying what the plugin is for", m.Name)
	}
	if len(m.Skills) == 0 {
		return fmt.Errorf("plugin %q: no skills listed — add at least one .md file under skills", m.Name)
	}
	if len(m.Skills) > MaxSkillFiles {
		return fmt.Errorf("plugin %q: %d skills listed (cap %d) — split the plugin or drop extras", m.Name, len(m.Skills), MaxSkillFiles)
	}
	for _, rel := range m.Skills {
		if !strings.HasSuffix(rel, ".md") {
			return fmt.Errorf("plugin %q: skill %q must end in .md", m.Name, rel)
		}
		abs, err := resolveInside(dir, rel)
		if err != nil {
			return fmt.Errorf("plugin %q: skill %q: %v", m.Name, rel, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("plugin %q: skill %q: cannot read: %v — list only files that exist inside the plugin dir", m.Name, rel, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin %q: skill %q is not a regular file", m.Name, rel)
		}
		if info.Size() > MaxSkillBytes {
			return fmt.Errorf("plugin %q: skill %q is %d bytes (cap %d) — split it into smaller skills", m.Name, rel, info.Size(), MaxSkillBytes)
		}
		if err := checkLinkInside(dir, abs); err != nil {
			return fmt.Errorf("plugin %q: skill %q: %v", m.Name, rel, err)
		}
	}
	if m.Hooks != "" {
		if !strings.HasSuffix(m.Hooks, ".yaml") && !strings.HasSuffix(m.Hooks, ".yml") {
			return fmt.Errorf("plugin %q: hooks %q must end in .yaml or .yml", m.Name, m.Hooks)
		}
		if err := checkListedFile(dir, m.Name, "hooks", m.Hooks); err != nil {
			return err
		}
	}
	if m.MCP != "" {
		if !strings.HasSuffix(m.MCP, ".json") {
			return fmt.Errorf("plugin %q: mcp %q must end in .json", m.Name, m.MCP)
		}
		if err := checkListedFile(dir, m.Name, "mcp", m.MCP); err != nil {
			return err
		}
	}
	return nil
}

// Files returns every relpath Install copies, manifest first.
func (m *Manifest) Files() []string {
	out := []string{ManifestFileName}
	out = append(out, m.Skills...)
	if m.Hooks != "" {
		out = append(out, m.Hooks)
	}
	if m.MCP != "" {
		out = append(out, m.MCP)
	}
	return out
}

// checkListedFile verifies one non-skill listed file (hooks/mcp): contained,
// present, regular, and any symlink resolves inside dir.
func checkListedFile(dir, pluginName, kind, rel string) error {
	abs, err := resolveInside(dir, rel)
	if err != nil {
		return fmt.Errorf("plugin %q: %s %q: %v", pluginName, kind, rel, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("plugin %q: %s %q: cannot read: %v — list only files that exist inside the plugin dir", pluginName, kind, rel, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("plugin %q: %s %q is not a regular file", pluginName, kind, rel)
	}
	if err := checkLinkInside(dir, abs); err != nil {
		return fmt.Errorf("plugin %q: %s %q: %v", pluginName, kind, rel, err)
	}
	return nil
}

// resolveInside resolves rel against dir and refuses absolute paths and any
// `..` escape, lexically and after cleaning.
func resolveInside(dir, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("empty path — list a file relative to the plugin dir")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute path %q not allowed — use a path relative to the plugin dir", rel)
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == ".." {
			return "", fmt.Errorf("path escapes the plugin dir (.. not allowed)")
		}
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	abs := filepath.Join(absDir, filepath.FromSlash(rel))
	r, err := filepath.Rel(absDir, abs)
	if err != nil {
		return "", err
	}
	if r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the plugin dir (.. not allowed)")
	}
	return abs, nil
}

// checkLinkInside ensures a symlink at abs resolves to a target still inside
// dir. Non-symlinks pass. The source dir is already user-vetted; this keeps
// a manifest from smuggling an outside file past the lexical check.
func checkLinkInside(dir, abs string) error {
	fi, err := os.Lstat(abs)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	target, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("cannot resolve symlink: %v", err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	r, err := filepath.Rel(absDir, target)
	if err != nil {
		return err
	}
	if r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return fmt.Errorf("symlink target escapes the plugin dir (.. not allowed)")
	}
	return nil
}

// hashFile returns the sha256 hex of the file at path.
func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// LoadLockfile reads and sanity-checks the lockfile in an installed dir.
func LoadLockfile(installedDir string) (*Lockfile, error) {
	path := filepath.Join(installedDir, LockFileName)
	data, err := os.ReadFile(path)
	if err != nil && os.IsNotExist(err) {
		path = filepath.Join(installedDir, LockFileNameLegacy)
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("plugin: cannot read lockfile in %q: %v", installedDir, err)
	}
	var lf Lockfile
	if err := json.Unmarshal(data, &lf); err != nil {
		return nil, fmt.Errorf("plugin: bad lockfile in %q: %v — reinstall the plugin", installedDir, err)
	}
	if !nameRe.MatchString(lf.Name) {
		return nil, fmt.Errorf("plugin: bad lockfile in %q: invalid name %q — reinstall the plugin", installedDir, lf.Name)
	}
	if !versionRe.MatchString(lf.Version) {
		return nil, fmt.Errorf("plugin: bad lockfile in %q: invalid version %q — reinstall the plugin", installedDir, lf.Version)
	}
	if len(lf.Files) == 0 {
		return nil, fmt.Errorf("plugin: bad lockfile in %q: no pinned files — reinstall the plugin", installedDir)
	}
	return &lf, nil
}

// Install copies the validated source plugin into pluginHome/<name>/ and
// pins the lockfile. It never executes anything it installs.
//
// Explicit opt-in: call Install only after the user has explicitly approved
// installing this plugin — nothing auto-installs on load.
//
// If the destination already exists, Install refuses to touch it when the
// on-disk files drifted from their lockfile (tamper or lockfile loss); use
// Reinstall (--upgrade) to re-pin. An existing clean install of the same
// version is a no-op success; a clean install of a different version is
// refused so upgrades stay explicit via Reinstall.
func Install(srcDir, pluginHome string) (string, error) {
	m, err := LoadManifest(srcDir)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(pluginHome, m.Name)
	if _, err := os.Lstat(dest); err == nil {
		if !Verify(dest) {
			return "", fmt.Errorf("plugin %q: existing install at %q drifted from its lockfile (or has none) — pass --upgrade (Reinstall) to re-pin, or remove it first", m.Name, dest)
		}
		lf, err := LoadLockfile(dest)
		if err != nil {
			return "", fmt.Errorf("plugin %q: existing install at %q is unverifiable — pass --upgrade (Reinstall) to re-pin, or remove it first", m.Name, dest)
		}
		if lf.Version != m.Version {
			return "", fmt.Errorf("plugin %q: version %s already installed — use Reinstall (--upgrade) to move to %s", m.Name, lf.Version, m.Version)
		}
		return dest, nil
	}
	if err := installFresh(m, srcDir, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// Reinstall (--upgrade semantics) wipes pluginHome/<name>/ and re-installs
// from source, re-pinning the lockfile. Like Install it never executes
// anything and requires explicit user opt-in from the caller.
func Reinstall(srcDir, pluginHome string) (string, error) {
	m, err := LoadManifest(srcDir)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(pluginHome, m.Name)
	if err := os.RemoveAll(dest); err != nil {
		return "", fmt.Errorf("plugin %q: cannot clear %q: %v", m.Name, dest, err)
	}
	if err := installFresh(m, srcDir, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// installFresh copies exactly Manifest.Files() from srcDir to dest and pins
// the lockfile from the installed bytes. A failed install removes dest so a
// retry starts clean.
func installFresh(m *Manifest, srcDir, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("plugin %q: cannot create %q: %v", m.Name, dest, err)
	}
	failed := true
	defer func() {
		if failed {
			os.RemoveAll(dest)
		}
	}()
	for _, rel := range m.Files() {
		src, err := resolveInside(srcDir, rel)
		if err != nil {
			return fmt.Errorf("plugin %q: %q: %v", m.Name, rel, err)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("plugin %q: cannot read %q: %v", m.Name, rel, err)
		}
		dst := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("plugin %q: cannot create dir for %q: %v", m.Name, rel, err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("plugin %q: cannot write %q: %v", m.Name, rel, err)
		}
	}
	files := make(map[string]string, len(m.Files()))
	for _, rel := range m.Files() {
		h, err := hashFile(filepath.Join(dest, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("plugin %q: cannot hash installed %q: %v", m.Name, rel, err)
		}
		files[filepath.ToSlash(rel)] = h
	}
	lf := &Lockfile{Name: m.Name, Version: m.Version, Files: files}
	data, err := json.MarshalIndent(lf, "", "  ")
	if err != nil {
		return fmt.Errorf("plugin %q: cannot encode lockfile: %v", m.Name, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dest, LockFileName), data, 0o644); err != nil {
		return fmt.Errorf("plugin %q: cannot write lockfile: %v", m.Name, err)
	}
	failed = false
	return nil
}

// Verify re-hashes every lockfile-pinned file in installedDir and reports
// whether the tree matches. Missing, modified, or extra files (beyond the
// lockfile itself) all fail. Read-only; any error reads as false.
func Verify(installedDir string) bool {
	lf, err := LoadLockfile(installedDir)
	if err != nil {
		return false
	}
	for rel, want := range lf.Files {
		abs, err := resolveInside(installedDir, rel)
		if err != nil {
			return false
		}
		info, err := os.Stat(abs)
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
		got, err := hashFile(abs)
		if err != nil || got != want {
			return false
		}
	}
	allowed := make(map[string]bool, len(lf.Files)+2)
	allowed[LockFileName] = true
	allowed[LockFileNameLegacy] = true
	for rel := range lf.Files {
		allowed[filepath.FromSlash(rel)] = true
	}
	ok := true
	err = filepath.WalkDir(installedDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			ok = false
			return err
		}
		if d.IsDir() {
			return nil
		}
		r, err := filepath.Rel(installedDir, p)
		if err != nil || !allowed[r] {
			ok = false
		}
		return nil
	})
	if err != nil {
		return false
	}
	return ok
}
