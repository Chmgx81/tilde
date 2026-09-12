// File-writing entry point for `tilde --export <session-id> [--out path.md]`
// (spec §2.22 + §4 row). The brief shape itself is untouched: BriefFromFile
// in export.go owns distillation, scrubbing, and truncation; this file only
// resolves the session source, contains the output path to the cwd, and
// writes the file with mode 0600.
package export

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"tilde/internal/session"
)

// WriteBriefFile builds the distilled brief for sessionID and writes it to
// outPath (default <sessionID>-brief.md in the cwd) with mode 0600.
// It returns the resolved output path and the bytes written.
// A missing session and an out path outside the cwd both fail loud with
// the fix named; the brief content itself is whatever BriefFromFile
// builds today.
func WriteBriefFile(sessionID, outPath string) (string, int, error) {
	// One session-id guard for the whole codebase (session.ValidID): no
	// separators, no dots, so the lookup can never leave the sessions dir.
	if !session.ValidID(sessionID) {
		return "", 0, fmt.Errorf("export: bad session id %q: letters, digits, _ and - only (max 64) — list sessions with `tilde --resume`", sessionID)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", 0, fmt.Errorf("export: cannot locate home: %w", err)
	}
	src := filepath.Join(home, ".tilde", "sessions", sessionID+".jsonl")
	if _, err := os.Stat(src); err != nil {
		return "", 0, fmt.Errorf("export: no session %q (missing %s) — list sessions with `tilde --resume`", sessionID, src)
	}
	brief, err := BriefFromFile(src, 0)
	if err != nil {
		return "", 0, err
	}
	out, err := resolveOut(sessionID, outPath)
	if err != nil {
		return "", 0, err
	}
	if err := os.WriteFile(out, []byte(brief), 0o600); err != nil {
		return "", 0, fmt.Errorf("export: cannot write %q: %w", out, err)
	}
	return out, len(brief), nil
}

// resolveOut maps the --out value to an absolute path contained in the
// cwd. Empty means the spec default (<sessionID>-brief.md in the cwd).
// Anything resolving outside the cwd refuses — an export is a
// user-pasted artifact, not a file tool, so it stays where it is seen.
func resolveOut(sessionID, outPath string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("export: cannot determine working dir: %w", err)
	}
	if strings.TrimSpace(outPath) == "" {
		return filepath.Join(cwd, sessionID+"-brief.md"), nil
	}
	abs, err := filepath.Abs(outPath)
	if err != nil {
		return "", fmt.Errorf("export: cannot resolve --out %q: %w", outPath, err)
	}
	rel, err := filepath.Rel(cwd, abs)
	if err != nil {
		return "", fmt.Errorf("export: refusing --out %q: outside the working dir — write inside %s", outPath, cwd)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("export: refusing --out %q: outside the working dir — write inside %s", outPath, cwd)
	}
	return abs, nil
}
