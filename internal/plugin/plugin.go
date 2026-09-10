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
	"sort"
	"strings"
	"time"

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
	// StateDirName stores lifecycle state outside the content-hashed plugin
	// directory, so enabling or disabling a plugin never invalidates Verify.
	StateDirName = ".state"
	// RollbackDirName stores validated prior plugin trees. Entries are
	// directories named with a sortable timestamp and are never loaded as
	// active plugins.
	RollbackDirName = ".rollback"
)

// Skill caps, carried over from the deferred marketplace notes.
const (
	// MaxSkillFiles caps how many .md skills one plugin may list.
	MaxSkillFiles = 32
	// MaxSkillBytes caps each skill file at 64KB.
	MaxSkillBytes    = 64 * 1024
	MaxResourceBytes = 256 * 1024
	MaxResourceFiles = 128
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
	Agents      []string `yaml:"agents,omitempty"`
	Scripts     []string `yaml:"scripts,omitempty"`
	References  []string `yaml:"references,omitempty"`
	Assets      []string `yaml:"assets,omitempty"`
	Hooks       string   `yaml:"hooks,omitempty"`
	MCP         string   `yaml:"mcp,omitempty"`
}

// Lockfile pins the exact installed content: relpath -> sha256 hex.
type Lockfile struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Files   map[string]string `json:"files"`
}

// State is lifecycle metadata for an installed plugin. It is deliberately
// separate from the plugin tree and is not executable plugin content.
type State struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Enabled bool   `json:"enabled"`
}

// Status describes the installed plugin and its current lifecycle state.
type Status struct {
	Name     string
	Version  string
	Enabled  bool
	Verified bool
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
	for label, files := range map[string][]string{"scripts": m.Scripts, "references": m.References, "assets": m.Assets} {
		if len(files) > MaxResourceFiles {
			return fmt.Errorf("plugin %q: %s has %d files (cap %d)", m.Name, label, len(files), MaxResourceFiles)
		}
		for _, rel := range files {
			if err := checkResourceFile(dir, m.Name, label, rel); err != nil {
				return err
			}
		}
	}
	if len(m.Agents) > MaxResourceFiles {
		return fmt.Errorf("plugin %q: agents has %d files (cap %d)", m.Name, len(m.Agents), MaxResourceFiles)
	}
	for _, rel := range m.Agents {
		if !strings.HasSuffix(rel, ".md") && !strings.HasSuffix(rel, ".yaml") && !strings.HasSuffix(rel, ".yml") {
			return fmt.Errorf("plugin %q: agent %q must be markdown or YAML", m.Name, rel)
		}
		if err := checkResourceFile(dir, m.Name, "agents", rel); err != nil {
			return err
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
	out = append(out, m.Agents...)
	out = append(out, m.Scripts...)
	out = append(out, m.References...)
	out = append(out, m.Assets...)
	if m.Hooks != "" {
		out = append(out, m.Hooks)
	}
	if m.MCP != "" {
		out = append(out, m.MCP)
	}
	return out
}

func checkResourceFile(dir, pluginName, kind, rel string) error {
	abs, err := resolveInside(dir, rel)
	if err != nil {
		return fmt.Errorf("plugin %q: %s %q: %v", pluginName, kind, rel, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("plugin %q: %s %q: cannot read: %v", pluginName, kind, rel, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("plugin %q: %s %q is not a regular file", pluginName, kind, rel)
	}
	if info.Size() > MaxResourceBytes {
		return fmt.Errorf("plugin %q: %s %q is %d bytes (cap %d)", pluginName, kind, rel, info.Size(), MaxResourceBytes)
	}
	if err := checkLinkInside(dir, abs); err != nil {
		return fmt.Errorf("plugin %q: %s %q: %v", pluginName, kind, rel, err)
	}
	return nil
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
// DryRun validates a source plugin and reports what install would copy,
// without writing anything. Preview for the destructive install path.
func DryRun(srcDir string) (name, version string, files []string, err error) {
	if dir, staged, err := resolveSrc(srcDir); err != nil {
		return "", "", nil, err
	} else if staged {
		defer os.RemoveAll(dir)
		srcDir = dir
	}
	m, err := LoadManifest(srcDir)
	if err != nil {
		return "", "", nil, err
	}
	if err := m.Validate(srcDir); err != nil {
		return "", "", nil, err
	}
	return m.Name, m.Version, m.Files(), nil
}

func Install(srcDir, pluginHome string) (string, error) {
	// Embedded starter plugins (embedded://plugin/<name>) materialize to
	// a temp dir first; everything below runs the directory path.
	if dir, staged, err := resolveSrc(srcDir); err != nil {
		return "", err
	} else if staged {
		defer os.RemoveAll(dir)
		srcDir = dir
	}
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
	if err := os.MkdirAll(pluginHome, 0o755); err != nil {
		return "", fmt.Errorf("plugin %q: cannot create plugin home: %v", m.Name, err)
	}
	stage, err := os.MkdirTemp(pluginHome, "."+m.Name+".stage-")
	if err != nil {
		return "", fmt.Errorf("plugin %q: cannot create staging directory: %v", m.Name, err)
	}
	defer os.RemoveAll(stage)
	if err := installFresh(m, srcDir, stage); err != nil {
		return "", err
	}
	if !Verify(stage) {
		return "", fmt.Errorf("plugin %q: staged install failed verification", m.Name)
	}
	if err := writeState(pluginHome, &State{Name: m.Name, Version: m.Version, Enabled: true}); err != nil {
		return "", err
	}
	if err := os.Rename(stage, dest); err != nil {
		_ = removeState(pluginHome, m.Name)
		return "", fmt.Errorf("plugin %q: cannot activate staged install: %v", m.Name, err)
	}
	return dest, nil
}

// Upgrade atomically stages a validated source plugin and swaps it into the
// active location. The previous verified tree is retained for Rollback. A
// failed staging, activation, or state write leaves the old active tree in
// place.
func Upgrade(srcDir, pluginHome string) (string, error) {
	if dir, staged, err := resolveSrc(srcDir); err != nil {
		return "", err
	} else if staged {
		defer os.RemoveAll(dir)
		srcDir = dir
	}
	m, err := LoadManifest(srcDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(pluginHome, 0o755); err != nil {
		return "", fmt.Errorf("plugin %q: cannot create plugin home: %v", m.Name, err)
	}
	dest := filepath.Join(pluginHome, m.Name)
	oldExists, err := pathExists(dest)
	if err != nil {
		return "", err
	}
	enabled := true
	var oldVersion string
	if oldExists {
		if !Verify(dest) {
			return "", fmt.Errorf("plugin %q: existing install at %q drifted from its lockfile — repair or remove it before upgrading", m.Name, dest)
		}
		lf, err := LoadLockfile(dest)
		if err != nil {
			return "", err
		}
		oldVersion = lf.Version
		enabled, err = readEnabled(pluginHome, m.Name)
		if err != nil {
			return "", err
		}
	}

	stage, err := os.MkdirTemp(pluginHome, "."+m.Name+".stage-")
	if err != nil {
		return "", fmt.Errorf("plugin %q: cannot create staging directory: %v", m.Name, err)
	}
	stageMoved := false
	defer func() {
		if !stageMoved {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := installFresh(m, srcDir, stage); err != nil {
		return "", err
	}
	if !Verify(stage) {
		return "", fmt.Errorf("plugin %q: staged upgrade failed verification", m.Name)
	}

	if !oldExists {
		if err := writeState(pluginHome, &State{Name: m.Name, Version: m.Version, Enabled: enabled}); err != nil {
			return "", err
		}
		if err := os.Rename(stage, dest); err != nil {
			_ = removeState(pluginHome, m.Name)
			return "", fmt.Errorf("plugin %q: cannot activate staged upgrade: %v", m.Name, err)
		}
		stageMoved = true
		return dest, nil
	}

	rollbackDir := filepath.Join(pluginHome, RollbackDirName, m.Name)
	if err := os.MkdirAll(rollbackDir, 0o755); err != nil {
		return "", fmt.Errorf("plugin %q: cannot create rollback directory: %v", m.Name, err)
	}
	backup, err := uniqueSnapshotPath(rollbackDir, oldVersion)
	if err != nil {
		return "", err
	}
	if err := os.Rename(dest, backup); err != nil {
		return "", fmt.Errorf("plugin %q: cannot stage current install for rollback: %v", m.Name, err)
	}
	if err := os.Rename(stage, dest); err != nil {
		if restoreErr := os.Rename(backup, dest); restoreErr != nil {
			return "", fmt.Errorf("plugin %q: activation failed: %v; restoring old install failed: %v", m.Name, err, restoreErr)
		}
		return "", fmt.Errorf("plugin %q: activation failed: %v", m.Name, err)
	}
	stageMoved = true
	if err := writeState(pluginHome, &State{Name: m.Name, Version: m.Version, Enabled: enabled}); err != nil {
		_ = os.RemoveAll(dest)
		if restoreErr := os.Rename(backup, dest); restoreErr != nil {
			return "", fmt.Errorf("plugin %q: state update failed: %v; restoring old install failed: %v", m.Name, err, restoreErr)
		}
		return "", fmt.Errorf("plugin %q: state update failed: %v", m.Name, err)
	}
	return dest, nil
}

// Reinstall is the legacy force-reinstall API. It stages the replacement and
// swaps it atomically, but unlike Upgrade it does not retain the old tree for
// rollback and can repair a drifted install.
func Reinstall(srcDir, pluginHome string) (string, error) {
	m, err := LoadManifest(srcDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(pluginHome, 0o755); err != nil {
		return "", fmt.Errorf("plugin %q: cannot create plugin home: %v", m.Name, err)
	}
	dest := filepath.Join(pluginHome, m.Name)
	oldExists, err := pathExists(dest)
	if err != nil {
		return "", err
	}
	enabled := true
	if oldExists {
		enabled, err = readEnabled(pluginHome, m.Name)
		if err != nil {
			return "", err
		}
	}
	stage, err := os.MkdirTemp(pluginHome, "."+m.Name+".reinstall-")
	if err != nil {
		return "", fmt.Errorf("plugin %q: cannot create staging directory: %v", m.Name, err)
	}
	stageMoved := false
	defer func() {
		if !stageMoved {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := installFresh(m, srcDir, stage); err != nil {
		return "", err
	}
	if !Verify(stage) {
		return "", fmt.Errorf("plugin %q: staged reinstall failed verification", m.Name)
	}
	var backup string
	if oldExists {
		backup, err = temporaryPath(pluginHome, "."+m.Name+".reinstall-")
		if err != nil {
			return "", err
		}
		if err := os.Rename(dest, backup); err != nil {
			return "", fmt.Errorf("plugin %q: cannot stage current install: %v", m.Name, err)
		}
	}
	if err := os.Rename(stage, dest); err != nil {
		if oldExists {
			_ = os.Rename(backup, dest)
		}
		return "", fmt.Errorf("plugin %q: cannot activate reinstall: %v", m.Name, err)
	}
	stageMoved = true
	if err := writeState(pluginHome, &State{Name: m.Name, Version: m.Version, Enabled: enabled}); err != nil {
		_ = os.RemoveAll(dest)
		if oldExists {
			_ = os.Rename(backup, dest)
		}
		return "", fmt.Errorf("plugin %q: state update failed: %v", m.Name, err)
	}
	if oldExists {
		_ = os.RemoveAll(backup)
	}
	return dest, nil
}

// Enable marks an installed, verified plugin as enabled.
func Enable(pluginHome, name string) error {
	return setEnabled(pluginHome, name, true)
}

// Disable marks an installed plugin as disabled. Disabling does not delete
// content, so a later Enable can restore it without a source directory.
func Disable(pluginHome, name string) error {
	return setEnabled(pluginHome, name, false)
}

// PluginEnabled reports the persisted lifecycle state. Missing state is
// treated as enabled for compatibility with installs created before the
// lifecycle metadata existed; malformed state fails closed.
func PluginEnabled(pluginHome, name string) bool {
	enabled, err := readEnabled(pluginHome, name)
	return err == nil && enabled
}

func setEnabled(pluginHome, name string, enabled bool) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	dest := filepath.Join(pluginHome, name)
	if !Verify(dest) {
		return fmt.Errorf("plugin %q failed verification (drifted or unlocked)", name)
	}
	lf, err := LoadLockfile(dest)
	if err != nil {
		return err
	}
	return writeState(pluginHome, &State{Name: name, Version: lf.Version, Enabled: enabled})
}

// Remove deletes an installed plugin, its lifecycle metadata, and its
// rollback history. It is intentionally explicit and is not used by upgrade.
func Remove(pluginHome, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	dest := filepath.Join(pluginHome, name)
	if ok, err := pathExists(dest); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("plugin %q is not installed", name)
	}
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("plugin %q: cannot remove install: %v", name, err)
	}
	if err := removeState(pluginHome, name); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(pluginHome, RollbackDirName, name)); err != nil {
		return fmt.Errorf("plugin %q: cannot remove rollback history: %v", name, err)
	}
	return nil
}

// Rollback swaps the active plugin with the newest validated prior tree. The
// active tree is discarded after a successful swap, so repeated rollbacks
// walk backward through upgrade history instead of ping-ponging.
func Rollback(pluginHome, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	dest := filepath.Join(pluginHome, name)
	rollbackDir := filepath.Join(pluginHome, RollbackDirName, name)
	target, err := latestValidSnapshot(rollbackDir)
	if err != nil {
		return fmt.Errorf("plugin %q: %v", name, err)
	}
	enabled, err := readEnabled(pluginHome, name)
	if err != nil {
		return err
	}
	currentExists, err := pathExists(dest)
	if err != nil {
		return err
	}
	var backup string
	if currentExists {
		currentVersion := "current"
		if lf, loadErr := LoadLockfile(dest); loadErr == nil {
			currentVersion = lf.Version
		}
		backup, err = uniqueSnapshotPath(rollbackDir, currentVersion)
		if err != nil {
			return err
		}
		if err := os.Rename(dest, backup); err != nil {
			return fmt.Errorf("plugin %q: cannot stage current install for rollback: %v", name, err)
		}
	}
	if err := os.Rename(target, dest); err != nil {
		if currentExists {
			_ = os.Rename(backup, dest)
		}
		return fmt.Errorf("plugin %q: cannot activate rollback: %v", name, err)
	}
	if err := writeState(pluginHome, &State{Name: name, Version: mustVersion(dest), Enabled: enabled}); err != nil {
		// Put the selected snapshot back before restoring the old active tree;
		// a state-file failure must not consume the rollback target.
		_ = os.Rename(dest, target)
		if currentExists {
			_ = os.Rename(backup, dest)
		}
		return fmt.Errorf("plugin %q: state update failed during rollback: %v", name, err)
	}
	// The old active tree is not a rollback target: keeping it would make
	// the next rollback immediately return to the version we just left.
	if currentExists {
		_ = os.RemoveAll(backup)
	}
	return nil
}

// Inspect returns lifecycle and integrity information for one installed
// plugin. Missing or invalid installs are errors, while Verify is reported
// separately so list commands can still show drift.
func Inspect(pluginHome, name string) (Status, error) {
	if err := ValidateName(name); err != nil {
		return Status{}, err
	}
	dest := filepath.Join(pluginHome, name)
	lf, err := LoadLockfile(dest)
	if err != nil {
		return Status{}, err
	}
	enabled, err := readEnabled(pluginHome, name)
	if err != nil {
		return Status{}, err
	}
	return Status{Name: lf.Name, Version: lf.Version, Enabled: enabled, Verified: Verify(dest)}, nil
}

// ValidateName validates a name used to address an installed plugin.
func ValidateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("plugin: bad name %q — use lowercase letters, digits, and hyphens only", name)
	}
	return nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func statePath(pluginHome, name string) string {
	return filepath.Join(pluginHome, StateDirName, name+".json")
}

func readEnabled(pluginHome, name string) (bool, error) {
	data, err := os.ReadFile(statePath(pluginHome, name))
	if os.IsNotExist(err) {
		return true, nil // pre-lifecycle installs were enabled by definition
	}
	if err != nil {
		return false, fmt.Errorf("plugin %q: cannot read lifecycle state: %v", name, err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil || state.Name != name {
		return false, fmt.Errorf("plugin %q: invalid lifecycle state", name)
	}
	return state.Enabled, nil
}

func writeState(pluginHome string, state *State) error {
	if err := ValidateName(state.Name); err != nil {
		return err
	}
	dir := filepath.Join(pluginHome, StateDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("plugin %q: cannot create lifecycle state dir: %v", state.Name, err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("plugin %q: cannot encode lifecycle state: %v", state.Name, err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return fmt.Errorf("plugin %q: cannot create lifecycle state temp file: %v", state.Name, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("plugin %q: cannot protect lifecycle state: %v", state.Name, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("plugin %q: cannot write lifecycle state: %v", state.Name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("plugin %q: cannot close lifecycle state: %v", state.Name, err)
	}
	if err := os.Rename(tmpName, statePath(pluginHome, state.Name)); err != nil {
		return fmt.Errorf("plugin %q: cannot activate lifecycle state: %v", state.Name, err)
	}
	return nil
}

func removeState(pluginHome, name string) error {
	err := os.Remove(statePath(pluginHome, name))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("plugin %q: cannot remove lifecycle state: %v", name, err)
	}
	return nil
}

func uniqueSnapshotPath(dir, version string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cannot create rollback directory: %v", err)
	}
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("%020d-%s", time.Now().UnixNano(), version)
		if i > 0 {
			name += fmt.Sprintf("-%d", i)
		}
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return path, nil
		} else if err != nil {
			return "", err
		}
		time.Sleep(time.Nanosecond)
	}
	return "", fmt.Errorf("cannot allocate unique rollback snapshot")
}

func temporaryPath(dir, pattern string) (string, error) {
	tmp, err := os.MkdirTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("cannot allocate temporary plugin path: %v", err)
	}
	path := tmp
	if err := os.RemoveAll(tmp); err != nil {
		return "", fmt.Errorf("cannot prepare temporary plugin path: %v", err)
	}
	return path, nil
}

func latestValidSnapshot(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("no rollback available")
	}
	if err != nil {
		return "", fmt.Errorf("cannot read rollback history: %v", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if Verify(path) {
			return path, nil
		}
	}
	return "", fmt.Errorf("no verified rollback available")
}

func mustVersion(dir string) string {
	lf, err := LoadLockfile(dir)
	if err != nil {
		return "0.0.0"
	}
	return lf.Version
}

// installFresh copies exactly Manifest.Files() from srcDir to dest and pins
// the lockfile from the installed bytes. A failed install removes dest so a
// retry starts clean.
func installFresh(m *Manifest, srcDir, dest string) error {
	// Full re-validation immediately before copying: the source tree is
	// untrusted and may have changed since an earlier Validate call.
	// Kind-specific caps (skill vs resource bytes) are enforced here;
	// the per-file Stat/link re-check in the loop below covers swaps
	// between this validation and each individual copy.
	if err := m.Validate(srcDir); err != nil {
		return err
	}
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
		// Re-verify at copy time, not just at Validate time: the source
		// tree may have changed between check and copy (TOCTOU). Same
		// gates as validation — regular file, size cap, symlink inside.
		info, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("plugin %q: cannot read %q: %v", m.Name, rel, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin %q: %q is not a regular file", m.Name, rel)
		}
		if info.Size() > MaxResourceBytes {
			return fmt.Errorf("plugin %q: %q is %d bytes (cap %d)", m.Name, rel, info.Size(), MaxResourceBytes)
		}
		if err := checkLinkInside(srcDir, src); err != nil {
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
