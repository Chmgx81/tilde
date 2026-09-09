package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSessionFixture stores lines as ~/.tilde/sessions/<id>.jsonl under a
// temp HOME and returns the session id.
func writeSessionFixture(t *testing.T, home, id string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, ".tilde", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWriteBriefFileMissingID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, _, err := WriteBriefFile("nosuchsession", ""); err == nil {
		t.Fatal("want error for missing session, got nil")
	} else if !strings.Contains(err.Error(), "no session") {
		t.Fatalf("error should name the missing session, got: %v", err)
	}
}

func TestWriteBriefFileBadID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, id := range []string{"", "../evil", "a/b", "a.jsonl", strings.Repeat("x", 65)} {
		if _, _, err := WriteBriefFile(id, ""); err == nil {
			t.Errorf("want error for bad session id %q, got nil", id)
		} else if !strings.Contains(err.Error(), "bad session id") {
			t.Errorf("error should name the bad id, got: %v", err)
		}
	}
}

func TestWriteBriefFileOutContainment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	id := writeSessionFixture(t, home, "sess1",
		line("user", map[string]any{"text": "contained goal"}),
		line("assistant", map[string]any{"text": "working"}),
	)
	cwd := t.TempDir()
	t.Chdir(cwd)
	for _, out := range []string{"../outside.md", filepath.Join(t.TempDir(), "abs-outside.md")} {
		if _, _, err := WriteBriefFile(id, out); err == nil {
			t.Errorf("want containment refusal for --out %q, got nil", out)
		} else if !strings.Contains(err.Error(), "outside the working dir") {
			t.Errorf("--out %q: error should name containment, got: %v", out, err)
		}
	}
}

func TestWriteBriefFileRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	id := writeSessionFixture(t, home, "sess9",
		line("user", map[string]any{"text": "roundtrip goal marker"}),
		line("assistant", map[string]any{"text": "on it"}),
	)
	cwd := t.TempDir()
	t.Chdir(cwd)

	path, n, err := WriteBriefFile(id, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cwd, id+"-brief.md"); path != want {
		t.Errorf("default out = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(data) {
		t.Errorf("reported %d bytes, file holds %d", n, len(data))
	}
	if st, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if st.Mode().Perm() != 0o600 {
		t.Errorf("out mode = %o, want 600", st.Mode().Perm())
	}
	for _, want := range []string{"# Session brief", "roundtrip goal marker"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("out file missing %q\n--- file ---\n%s", want, data)
		}
	}
}

func TestWriteBriefFileExplicitOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	id := writeSessionFixture(t, home, "sess7",
		line("user", map[string]any{"text": "explicit out goal"}),
		line("assistant", map[string]any{"text": "noted"}),
	)
	cwd := t.TempDir()
	t.Chdir(cwd)

	path, _, err := WriteBriefFile(id, "custom.md")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cwd, "custom.md"); path != want {
		t.Errorf("explicit out = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "explicit out goal") {
		t.Errorf("out file missing goal\n--- file ---\n%s", data)
	}
}
