package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustAppend(t *testing.T, l *AuditLog, e AuditEvent) {
	t.Helper()
	if err := l.Append(e); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func TestExportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	want := []AuditEvent{
		{Tool: "bash", Decision: "allow", RedactedArgsHash: "h1", Detail: "ls redacted"},
		{Tool: "write", Decision: "deny", RedactedArgsHash: "h2", Detail: "blocked path"},
		{Tool: "webfetch", Decision: "ask", RedactedArgsHash: "h3", Detail: "needs approval"},
	}
	for _, e := range want {
		mustAppend(t, l, e)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	got, err := ReadAll(l.Path)
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Tool != want[i].Tool || got[i].Decision != want[i].Decision ||
			got[i].RedactedArgsHash != want[i].RedactedArgsHash || got[i].Detail != want[i].Detail {
			t.Fatalf("event %d mismatch: got %+v want %+v", i, got[i], want[i])
		}
		if got[i].TS.IsZero() {
			t.Fatalf("event %d missing TS", i)
		}
	}
}

func TestExportCorruptLineTolerance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	e1, _ := json.Marshal(AuditEvent{TS: time.Now().UTC(), Tool: "bash", Decision: "allow", RedactedArgsHash: "h1", Detail: "ok"})
	e2, _ := json.Marshal(AuditEvent{TS: time.Now().UTC(), Tool: "write", Decision: "deny", RedactedArgsHash: "h2", Detail: "no"})
	content := string(e1) + "\n{this is not json\n\n" + string(e2) + "\n[1,2,3\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, skipped, err := ReadAllWithSkipped(path)
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d valid events, want 2", len(got))
	}
	if skipped != 2 {
		t.Fatalf("skipped = %d, want 2", skipped)
	}
	// Required-signature entry point tolerates the same file.
	got2, err := ReadAll(path)
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if len(got2) != 2 {
		t.Fatalf("ReadAll got %d events, want 2", len(got2))
	}
}

func TestExportReadMissingFile(t *testing.T) {
	if _, err := ReadAll(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("want error for missing file, got nil")
	}
}

func TestExportFilter(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	events := []AuditEvent{
		{TS: base, Tool: "bash", Decision: "allow", RedactedArgsHash: "h1"},
		{TS: base.Add(time.Hour), Tool: "write", Decision: "deny", RedactedArgsHash: "h2"},
		{TS: base.Add(2 * time.Hour), Tool: "bash", Decision: "ask", RedactedArgsHash: "h3"},
	}
	if got := Filter(events, FilterOpts{}); len(got) != 3 {
		t.Fatalf("empty opts kept %d, want 3", len(got))
	}
	if got := Filter(events, FilterOpts{Since: base.Add(90 * time.Minute)}); len(got) != 1 {
		t.Fatalf("since kept %d, want 1", len(got))
	}
	// Since is inclusive: exact stamp of event 2 keeps events 2-3.
	if got := Filter(events, FilterOpts{Since: base.Add(time.Hour)}); len(got) != 2 {
		t.Fatalf("inclusive since kept %d, want 2", len(got))
	}
	got := Filter(events, FilterOpts{Tool: "bash"})
	if len(got) != 2 || got[0].RedactedArgsHash != "h1" || got[1].RedactedArgsHash != "h3" {
		t.Fatalf("tool filter wrong: %+v", got)
	}
	got = Filter(events, FilterOpts{Decision: "deny"})
	if len(got) != 1 || got[0].Tool != "write" {
		t.Fatalf("decision filter wrong: %+v", got)
	}
	got = Filter(events, FilterOpts{Tool: "bash", Decision: "ask"})
	if len(got) != 1 || got[0].RedactedArgsHash != "h3" {
		t.Fatalf("combined filter wrong: %+v", got)
	}
	got = Filter(events, FilterOpts{Tool: "bash", Since: base.Add(3 * time.Hour)})
	if len(got) != 0 {
		t.Fatalf("over-narrow filter kept %d, want 0", len(got))
	}
	if got := Filter(nil, FilterOpts{Tool: "bash"}); len(got) != 0 {
		t.Fatalf("nil input kept %d, want 0", len(got))
	}
}

func TestExportRenderJSON(t *testing.T) {
	events := []AuditEvent{
		{TS: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), Tool: "bash", Decision: "allow", RedactedArgsHash: "h1", Detail: "d1"},
		{TS: time.Date(2026, 9, 1, 13, 0, 0, 0, time.UTC), Tool: "write", Decision: "deny", RedactedArgsHash: "h2", Detail: "d2"},
	}
	out := RenderJSON(events)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != len(events) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(events), out)
	}
	for i, ln := range lines {
		var got AuditEvent
		if err := json.Unmarshal([]byte(ln), &got); err != nil {
			t.Fatalf("line %d not valid JSON: %v (%q)", i, err, ln)
		}
		if got.Tool != events[i].Tool || got.Decision != events[i].Decision ||
			got.RedactedArgsHash != events[i].RedactedArgsHash || got.Detail != events[i].Detail {
			t.Fatalf("line %d mismatch: got %+v want %+v", i, got, events[i])
		}
	}
	if RenderJSON(nil) != "" {
		t.Fatal("empty input must render as empty string")
	}
}

func TestExportRenderText(t *testing.T) {
	ts := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	events := []AuditEvent{
		{TS: ts, Tool: "bash", Decision: "allow", RedactedArgsHash: "h1", Detail: "ran ls"},
	}
	out := RenderText(events)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %q", len(lines), out)
	}
	want := ts.UTC().Format(time.RFC3339) + " bash allow ran ls"
	if lines[0] != want {
		t.Fatalf("got %q, want %q", lines[0], want)
	}
	if RenderText(nil) != "" {
		t.Fatal("empty input must render as empty string")
	}
}

func TestExportNeverCarriesRawArgs(t *testing.T) {
	// Shape: marshalled events must expose only the hash slot, never raw args.
	raw, err := json.Marshal(AuditEvent{Tool: "bash", Decision: "allow", RedactedArgsHash: "abc", Detail: "d"})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for k := range keys {
		if k == "args" || k == "raw_args" || k == "redacted_args" {
			t.Fatalf("event JSON exposes raw-args slot %q: %s", k, raw)
		}
	}
	if _, ok := keys["redacted_args_hash"]; !ok {
		t.Fatalf("event JSON missing redacted_args_hash: %s", raw)
	}

	// Scrubbed-in (ReadAll) and scrubbed-out (renders): a secret smuggled
	// into Detail must not survive either path.
	fake := "sk-abcdefghij1234567890XY"
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	evil, _ := json.Marshal(AuditEvent{TS: time.Now().UTC(), Tool: "webfetch", Decision: "allow", RedactedArgsHash: "h", Detail: "note key=" + fake})
	if err := os.WriteFile(path, append(evil, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if strings.Contains(got[0].Detail, fake) {
		t.Fatalf("ReadAll leaks secret: %q", got[0].Detail)
	}
	for _, rendered := range []string{RenderJSON(got), RenderText(got)} {
		if strings.Contains(rendered, fake) {
			t.Fatalf("renderer leaks secret: %q", rendered)
		}
	}
	// NOTE: json.Marshal escapes < > &, so RenderJSON carries the marker as
	// \u003c\u003cREDACTED... — assert on "REDACTED" for the JSON path.
	if !strings.Contains(RenderJSON(got), "REDACTED") || !strings.Contains(RenderText(got), "<<REDACTED") {
		t.Fatal("renderers must carry the redaction marker")
	}
}
