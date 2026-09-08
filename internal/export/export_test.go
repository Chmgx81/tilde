package export

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// line builds one JSONL log line like session.Log.Append does.
func line(typ string, data map[string]any) string {
	b, err := json.Marshal(map[string]any{
		"ts":   time.Now().UTC().Format(time.RFC3339Nano),
		"type": typ,
		"data": data,
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// writeFixture writes lines (raw strings, so corrupt lines can be embedded)
// to a temp JSONL file and returns its path.
func writeFixture(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustContain(t *testing.T, brief string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(brief, w) {
			t.Errorf("brief missing %q\n--- brief ---\n%s", w, brief)
		}
	}
}

func TestGoalExtraction(t *testing.T) {
	path := writeFixture(t,
		line("user", map[string]any{"text": "Add export of session briefs"}),
		line("assistant", map[string]any{"text": "On it."}),
		line("user", map[string]any{"content": "Also handle corrupt lines"}),
	)
	brief, err := BriefFromFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, brief,
		"# Session brief",
		"## Goal",
		"Add export of session briefs",
		"## Files touched",
		"## Recent direction",
		"## Open steps",
		"## Counts",
	)
}

func TestGoalPrefersTextOverContent(t *testing.T) {
	path := writeFixture(t,
		line("user", map[string]any{"content": "content fallback goal"}),
		line("assistant", map[string]any{"text": "ok"}),
	)
	brief, err := BriefFromFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, brief, "content fallback goal")
}

func TestFilesDedupAndOrder(t *testing.T) {
	call := func(tool string, args map[string]any) string {
		return line("tool_call", map[string]any{"tool": tool, "args": args})
	}
	path := writeFixture(t,
		line("user", map[string]any{"text": "touch files"}),
		call("read_file", map[string]any{"path": "a.txt"}),
		call("write_file", map[string]any{"path": "b.txt"}),
		call("read_file", map[string]any{"path": "a.txt"}), // dup: listed once
		call("edit_file", map[string]any{"path": "c.txt"}),
		call("shell_command", map[string]any{"command": "go test ./..."}),
		line("assistant", map[string]any{"text": "done"}),
	)
	brief, err := BriefFromFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, brief, "- a.txt", "- b.txt", "- c.txt", "go test")
	if got := strings.Count(brief, "a.txt"); got != 1 {
		t.Errorf("a.txt listed %d times, want 1 (dedup)\n--- brief ---\n%s", got, brief)
	}
	ia, ib, ic := strings.Index(brief, "a.txt"), strings.Index(brief, "b.txt"), strings.Index(brief, "c.txt")
	if !(ia < ib && ib < ic) {
		t.Errorf("files out of encounter order: a=%d b=%d c=%d\n--- brief ---\n%s", ia, ib, ic, brief)
	}
}

func TestTruncationNote(t *testing.T) {
	path := writeFixture(t,
		line("user", map[string]any{"text": "a fairly long goal " + strings.Repeat("x", 500)}),
		line("assistant", map[string]any{"text": "reply " + strings.Repeat("y", 500)}),
	)
	brief, err := BriefFromFile(path, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(brief) > 200 {
		t.Errorf("brief is %d chars, want <= 200", len(brief))
	}
	mustContain(t, brief, "truncated")
}

func TestCorruptLineTolerance(t *testing.T) {
	path := writeFixture(t,
		line("user", map[string]any{"text": "goal survives corruption"}),
		`{this is not json`,
		line("assistant", map[string]any{"text": "still here"}),
	)
	brief, err := BriefFromFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, brief, "goal survives corruption", "corrupt lines skipped: 1")
}

func TestEmptyFileError(t *testing.T) {
	path := writeFixture(t)
	if _, err := BriefFromFile(path, 0); err == nil {
		t.Error("want error for empty log, got nil")
	}
}

func TestMissingFileError(t *testing.T) {
	if _, err := BriefFromFile(filepath.Join(t.TempDir(), "nope.jsonl"), 0); err == nil {
		t.Error("want error for missing file, got nil")
	}
}

func TestScrubRedactsSecrets(t *testing.T) {
	// Fake OpenAI-style key (matches tools.Scrub pattern sk- + 20 chars).
	key := "sk-testkey123456789012345"
	path := writeFixture(t,
		line("user", map[string]any{"text": "my key is " + key + " please proceed"}),
		line("assistant", map[string]any{"text": "noted"}),
	)
	brief, err := BriefFromFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(brief, key) {
		t.Errorf("brief leaks raw secret\n--- brief ---\n%s", brief)
	}
	mustContain(t, brief, "<<REDACTED")
}

func TestOpenStepsTrailingUser(t *testing.T) {
	path := writeFixture(t,
		line("user", map[string]any{"text": "first goal"}),
		line("assistant", map[string]any{"text": "working on it"}),
		line("user", map[string]any{"text": "also update the docs"}),
	)
	brief, err := BriefFromFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, brief, "also update the docs")
}

func TestOpenStepsNoneRecorded(t *testing.T) {
	path := writeFixture(t,
		line("user", map[string]any{"text": "goal"}),
		line("assistant", map[string]any{"text": "last word"}),
	)
	brief, err := BriefFromFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, brief, "none recorded")
}
