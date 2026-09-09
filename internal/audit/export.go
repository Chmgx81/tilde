// Package audit — P3-D read/filter/render additions (export.go).
//
// Read-only export surface over the P1-D append-only trail; the owner wires
// `tilde audit` on top of these helpers. Nothing here writes the log or
// persists raw tool arguments: events carry only a redacted-args hash plus
// an already-redacted, already-capped detail string.
package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"tilde/internal/tools"
)

// FilterOpts narrows Filter output. Zero values disable that dimension:
// zero Since disables the time floor, empty Tool/Decision match everything.
type FilterOpts struct {
	Since    time.Time
	Tool     string
	Decision string
}

// ReadAllWithSkipped reads path line by line, skipping blank lines silently
// and counting corrupt (non-JSON) lines like the TUI resume loader does: one
// bad line never kills the read. Each raw line is scrubbed before decode so
// a hand-edited file cannot smuggle a secret past the reader.
func ReadAllWithSkipped(path string) ([]AuditEvent, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("audit: cannot open %s: %w", path, err)
	}
	defer f.Close()

	var (
		events  []AuditEvent
		skipped int
	)
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	for s.Scan() {
		trimmed := strings.TrimSpace(s.Text())
		if trimmed == "" {
			continue
		}
		clean, _ := tools.Scrub(trimmed)
		var e AuditEvent
		if err := json.Unmarshal([]byte(clean), &e); err != nil {
			skipped++
			continue
		}
		events = append(events, e)
	}
	if err := s.Err(); err != nil {
		return nil, 0, fmt.Errorf("audit: read %s: %w", path, err)
	}
	return events, skipped, nil
}

// ReadAll returns the valid events in path, skipping corrupt lines.
// A missing/unreadable file fails loud; an empty or fully-corrupt file
// yields zero events and a nil error.
func ReadAll(path string) ([]AuditEvent, error) {
	events, _, err := ReadAllWithSkipped(path)
	return events, err
}

// Filter keeps events matching every set dimension: TS at/after Since when
// Since is non-zero, exact Tool match when Tool is non-empty, exact Decision
// match when Decision is non-empty. Nil/empty input yields nil.
func Filter(events []AuditEvent, opts FilterOpts) []AuditEvent {
	var out []AuditEvent
	for _, e := range events {
		if !opts.Since.IsZero() && e.TS.Before(opts.Since) {
			continue
		}
		if opts.Tool != "" && e.Tool != opts.Tool {
			continue
		}
		if opts.Decision != "" && e.Decision != opts.Decision {
			continue
		}
		out = append(out, e)
	}
	return out
}

// RenderJSON renders one JSON object per line (JSONL). Output is scrubbed;
// empty input renders as "".
func RenderJSON(events []AuditEvent) string {
	if len(events) == 0 {
		return ""
	}
	var b strings.Builder
	for _, e := range events {
		raw, err := json.Marshal(e)
		if err != nil {
			continue
		}
		clean, _ := tools.Scrub(string(raw))
		b.WriteString(clean)
		b.WriteByte('\n')
	}
	return b.String()
}

// RenderText renders `TS tool decision detail` lines (RFC3339 UTC stamp).
// Detail is already capped upstream, so no truncation happens here. Output
// is scrubbed; empty input renders as "".
func RenderText(events []AuditEvent) string {
	if len(events) == 0 {
		return ""
	}
	var b strings.Builder
	for _, e := range events {
		line := e.TS.UTC().Format(time.RFC3339) + " " + e.Tool + " " + e.Decision + " " + e.Detail
		clean, _ := tools.Scrub(line)
		b.WriteString(clean)
		b.WriteByte('\n')
	}
	return b.String()
}
