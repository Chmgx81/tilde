package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePlugin(t *testing.T, dir string, m Manifest) {
	t.Helper()
	data, err := yaml.Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ManifestFileName, string(data))
}

func validManifest() Manifest {
	return Manifest{
		Name:        "demo-plugin",
		Version:     "1.2.3",
		Description: "Demo plugin for hermetic tests.",
		Skills:      []string{"skills/a.md", "skills/b.md"},
		Hooks:       "hooks.yaml",
		MCP:         "mcp.json",
	}
}

// writeValidSource builds a complete valid source plugin, plus one extra
// file that is NOT listed and must never be installed.
func writeValidSource(t *testing.T, dir string) {
	t.Helper()
	writePlugin(t, dir, validManifest())
	writeFile(t, dir, "skills/a.md", "---\nname: a\ndescription: skill A does things here\n---\n\nBody A.\n")
	writeFile(t, dir, "skills/b.md", "---\nname: b\ndescription: skill B does other things\n---\n\nBody B.\n")
	writeFile(t, dir, "hooks.yaml", "hooks: []\n")
	writeFile(t, dir, "mcp.json", "{}\n")
	writeFile(t, dir, "notes.txt", "not part of the plugin\n")
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRoundTrip(t *testing.T) {
	src := t.TempDir()
	writeValidSource(t, src)

	m, err := LoadManifest(src)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Name != "demo-plugin" || m.Version != "1.2.3" {
		t.Fatalf("unexpected manifest: %+v", m)
	}

	home := t.TempDir()
	dest, err := Install(src, home)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if dest != filepath.Join(home, "demo-plugin") {
		t.Fatalf("unexpected dest %q", dest)
	}
	if !Verify(dest) {
		t.Fatal("Verify=false right after Install")
	}

	lf, err := LoadLockfile(dest)
	if err != nil {
		t.Fatalf("LoadLockfile: %v", err)
	}
	if lf.Name != "demo-plugin" || lf.Version != "1.2.3" {
		t.Fatalf("unexpected lockfile identity: %+v", lf)
	}
	if len(lf.Files) != 5 { // manifest + 2 skills + hooks + mcp
		t.Fatalf("lockfile pins %d files, want 5: %v", len(lf.Files), lf.Files)
	}

	// Installed content matches source; unlisted extra is not copied.
	for _, rel := range m.Files() {
		if got, want := readFile(t, dest, rel), readFile(t, src, rel); got != want {
			t.Errorf("installed %q differs from source", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "notes.txt")); !os.IsNotExist(err) {
		t.Error("unlisted notes.txt was installed")
	}

	// Second Install of the same clean version is an idempotent no-op.
	dest2, err := Install(src, home)
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if dest2 != dest {
		t.Fatalf("second Install returned %q, want %q", dest2, dest)
	}
	if !Verify(dest) {
		t.Fatal("Verify=false after idempotent Install")
	}

	// A dir with no lockfile never verifies.
	if Verify(t.TempDir()) {
		t.Error("Verify=true on empty dir")
	}
}

func TestBadName(t *testing.T) {
	for _, name := range []string{"", "Bad_Name!", "UPPER", "has space", "dot.name", "sla/sh", "../x"} {
		src := t.TempDir()
		m := validManifest()
		m.Name = name
		writePlugin(t, src, m)
		writeFile(t, src, "skills/a.md", "Body A.\n")
		if _, err := LoadManifest(src); err == nil {
			t.Errorf("name %q accepted, want refusal", name)
		}
	}
}

func TestVersion(t *testing.T) {
	for _, v := range []string{"0.0.1", "1.2.3", "10.20.30"} {
		src := t.TempDir()
		writeValidSource(t, src)
		m := validManifest()
		m.Version = v
		writePlugin(t, src, m)
		if _, err := LoadManifest(src); err != nil {
			t.Errorf("version %q refused: %v", v, err)
		}
	}
	for _, v := range []string{"", "1", "1.2", "v1.2.3", "1.2.3.4", "1.2.x", "latest", "1.2.3-rc1"} {
		src := t.TempDir()
		m := validManifest()
		m.Version = v
		writePlugin(t, src, m)
		writeFile(t, src, "skills/a.md", "Body A.\n")
		if _, err := LoadManifest(src); err == nil {
			t.Errorf("version %q accepted, want refusal", v)
		}
	}
}

func TestEscapeRefused(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, base, "evil.md", "Body evil.\n") // exists outside: must still be refused
	m := validManifest()
	m.Skills = []string{"../evil.md"}
	m.Hooks, m.MCP = "", ""
	writePlugin(t, src, m)
	if _, err := LoadManifest(src); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Errorf(".. escape accepted or wrong error: %v", err)
	}

	// Absolute paths are refused too.
	m.Skills = []string{filepath.Join(base, "evil.md")}
	writePlugin(t, src, m)
	if _, err := LoadManifest(src); err == nil || !strings.Contains(err.Error(), "relative") {
		t.Errorf("absolute path accepted or wrong error: %v", err)
	}

	// Escape via hooks as well.
	m.Skills = []string{"here.md"}
	writeFile(t, src, "here.md", "Body here.\n")
	writeFile(t, base, "evil.yaml", "hooks: []\n")
	m.Hooks = "../evil.yaml"
	writePlugin(t, src, m)
	if _, err := LoadManifest(src); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Errorf("hooks escape accepted or wrong error: %v", err)
	}
}

func TestMissingFile(t *testing.T) {
	src := t.TempDir()
	m := validManifest()
	m.Skills = []string{"ghost.md"}
	m.Hooks, m.MCP = "", ""
	writePlugin(t, src, m)
	if _, err := LoadManifest(src); err == nil {
		t.Error("missing skill accepted, want refusal")
	}
}

func TestSkillCaps(t *testing.T) {
	// Non-.md skill.
	src := t.TempDir()
	m := validManifest()
	m.Skills = []string{"skills/a.txt"}
	m.Hooks, m.MCP = "", ""
	writePlugin(t, src, m)
	writeFile(t, src, "skills/a.txt", "Body.\n")
	if _, err := LoadManifest(src); err == nil || !strings.Contains(err.Error(), ".md") {
		t.Errorf("non-.md skill accepted or wrong error: %v", err)
	}

	// Oversize skill.
	src = t.TempDir()
	m = validManifest()
	m.Skills = []string{"big.md"}
	m.Hooks, m.MCP = "", ""
	writePlugin(t, src, m)
	writeFile(t, src, "big.md", strings.Repeat("x", MaxSkillBytes+1))
	if _, err := LoadManifest(src); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Errorf("oversize skill accepted or wrong error: %v", err)
	}

	// Too many skills.
	src = t.TempDir()
	m = validManifest()
	m.Hooks, m.MCP = "", ""
	m.Skills = nil
	for i := 0; i < MaxSkillFiles+1; i++ {
		rel := fmt.Sprintf("skills/s%02d.md", i)
		m.Skills = append(m.Skills, rel)
		writeFile(t, src, rel, "Body.\n")
	}
	writePlugin(t, src, m)
	if _, err := LoadManifest(src); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Errorf("33 skills accepted or wrong error: %v", err)
	}
}

func TestDriftDetected(t *testing.T) {
	src := t.TempDir()
	writeValidSource(t, src)
	home := t.TempDir()
	dest, err := Install(src, home)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Tamper with an installed file: drift.
	writeFile(t, dest, "skills/a.md", "TAMPERED\n")
	if Verify(dest) {
		t.Fatal("Verify=true after tampering")
	}
	// Deleted file is drift too.
	if err := os.Remove(filepath.Join(dest, "skills", "b.md")); err != nil {
		t.Fatal(err)
	}
	if Verify(dest) {
		t.Fatal("Verify=true after deletion")
	}

	// Install refuses on drift and points at Reinstall.
	if _, err := Install(src, home); err == nil || !strings.Contains(err.Error(), "Reinstall") {
		t.Errorf("Install on drift succeeded or wrong error: %v", err)
	}
}

func TestReinstallRepins(t *testing.T) {
	src := t.TempDir()
	writeValidSource(t, src)
	home := t.TempDir()
	dest, err := Install(src, home)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Drift, then evolve the source.
	writeFile(t, dest, "skills/a.md", "TAMPERED\n")
	if Verify(dest) {
		t.Fatal("Verify=true after tampering")
	}
	writeFile(t, src, "skills/a.md", "Body A v2.\n")

	dest2, err := Reinstall(src, home)
	if err != nil {
		t.Fatalf("Reinstall: %v", err)
	}
	if dest2 != dest {
		t.Fatalf("Reinstall returned %q, want %q", dest2, dest)
	}
	if !Verify(dest) {
		t.Fatal("Verify=false after Reinstall")
	}
	if got := readFile(t, dest, "skills/a.md"); got != "Body A v2.\n" {
		t.Errorf("Reinstall did not re-pin source content: %q", got)
	}
}
