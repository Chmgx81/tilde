// Package tools — memory tool (P2-E local-first memory).
//
// Project-scoped append-only log at <Root>/.tilde/memory.md, independent of
// ~/.tilde (user-level memory never touched). The path is fixed: callers
// pass no path, so there is no caller-controlled path to escape Root —
// containment is structural, with contain() still applied as defense in
// depth (symlinked .tilde dir or TOCTOU swap fails closed).
//
// Policy tiers (owner wires policy; this file only documents):
//   - recall: read-only, Plan-safe.
//   - save, forget: mutating (append / rewrite of the memory file;
//     suggested tier: ask, Plan-blocked — owner confirms tier).
//
// Trust model / poisoning resistance (§14 — notes only, no new machinery):
//   - memory.md is USER-owned: system rules, user-stated preferences, and
//     agent-inferred content are segregated by role — agent writes here are
//     approved actions (ask-tier + Plan-blocked), never silent background
//     writes.
//   - The save path is already privileged: ask-tier approval + session and
//     audit trails record each write, so a seeded false "fact" (via a
//     summarized conversation or a poisoned retrieved document) is
//     attributable, not ambient.
//   - Recall output stays fenced as untrusted (prior session/model output),
//     and the Description warns that recalled facts are approximate —
//     verify before high-stakes use.
//   - Temporal validity (§9): saves are date-prefixed (UTC YYYY-MM-DD),
//     so a time-bounded fact reads as time-bounded instead of silently
//     overriding a durable preference forever.
//
// Undo: NOT snapshotted (snapshotTools untouched on purpose). The file is
// append-only log semantics: save only appends, and forget is an explicit,
// model-confirmed destructive op with an exact-count receipt — snapshotting
// every recall-adjacent append would spam the undo stack and a restore
// could resurrect lines the operator deliberately forgot.
package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// memoryRel is the only path this tool ever touches, relative to Root.
	memoryRel = ".tilde/memory.md"
	// memoryMaxBytes caps the file: save refuses past this (cleanup fix
	// named in the refusal — forget with a match — instead of silently
	// truncating history the operator never asked to lose).
	memoryMaxBytes = 64 * 1024
	// memoryMaxText caps one save to a single short line.
	memoryMaxText = 500
	// memoryRecallLines caps recall output; the cap names itself plus the
	// refine fix (recall with a narrower match).
	memoryRecallLines = 40
)

// Memory is project-local persistent memory: save/recall/forget lines in
// <Root>/.tilde/memory.md. File-backed (survives restarts); hermetic tests
// point Root at t.TempDir().
type Memory struct {
	Root string

	mu sync.Mutex
}

func (t *Memory) Name() string { return "memory" }
func (t *Memory) Description() string {
	return "Project-local memory: save a fact, recall saved facts (optionally filtered), forget lines matching text. Stored at .tilde/memory.md inside the project. Saved facts are date-prefixed (UTC YYYY-MM-DD); recalled facts are approximate — verify before high-stakes use."
}
func (t *Memory) Schema() map[string]any {
	op := map[string]any{"type": "string", "enum": []string{"save", "recall", "forget"}}
	return map[string]any{"type": "object", "properties": map[string]any{"op": op,
		"text":  map[string]any{"type": "string", "description": "Fact to save (save only)"},
		"match": map[string]any{"type": "string", "description": "Substring filter (recall) or lines to delete (forget)"},
	}, "required": []string{"op"}}
}

func (t *Memory) Exec(_ context.Context, args map[string]any) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch op := optStr(args, "op", "recall"); op {
	case "save":
		return t.save(optStr(args, "text", ""))
	case "recall":
		return t.recall(optStr(args, "match", ""))
	case "forget":
		return t.forget(optStr(args, "match", ""))
	default:
		return "", fmt.Errorf("unknown op %q: pass one of save|recall|forget", op)
	}
}

// full resolves the fixed path under Root. No caller input enters the path.
func (t *Memory) full() (string, error) {
	return contain(t.Root, memoryRel)
}

// load reads current lines; missing file (auto-created on first save) is
// empty memory, not an error.
func (t *Memory) load() ([]string, error) {
	if _, err := t.full(); err != nil {
		return nil, err
	}
	data, err := readFileNoFollow(t.Root, memoryRel)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read %q: %v — check permissions and retry", memoryRel, err)
	}
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

// save appends one "- YYYY-MM-DD: <text>" line (UTC date prefix = the
// temporal-validity signal: a time-bounded fact reads as time-bounded).
// Matching in recall/forget is plain substring, so existing dateless
// lines ("- <text>") keep working with no migration. Refuses empty text,
// text over memoryMaxText chars, and writes that would push the file past
// memoryMaxBytes (names the forget-based cleanup fix).
func (t *Memory) save(text string) (string, error) {
	one := strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(text, "\r", " "), "\n", " ")), " ")
	if one == "" {
		return "", fmt.Errorf("op \"save\" needs \"text\" as a non-empty string")
	}
	if len([]rune(one)) > memoryMaxText {
		return "", fmt.Errorf("refusing save: text is %d chars (over the %d-char per-line cap) — shorten it to one short fact and retry", len([]rune(one)), memoryMaxText)
	}
	lines, err := t.load()
	if err != nil {
		return "", err
	}
	dated := "- " + time.Now().UTC().Format("2006-01-02") + ": " + one
	next := strings.Join(append(lines, dated), "\n") + "\n"
	if len(next) > memoryMaxBytes {
		return "", fmt.Errorf("refusing save: memory file would pass the %d-byte cap — forget stale lines with op \"forget\" + \"match\", then retry", memoryMaxBytes)
	}
	if err := writeFileNoFollow(t.Root, memoryRel, []byte(next), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("saved 1 line (%d of %d total).", 1, len(lines)+1), nil
}

// recall returns the whole file, or only lines containing match. Caps at
// memoryRecallLines with a self-naming note + refine fix. Content is prior
// session/model output — fenced as untrusted.
func (t *Memory) recall(match string) (string, error) {
	lines, err := t.load()
	if err != nil {
		return "", err
	}
	if match != "" {
		var kept []string
		for _, l := range lines {
			if strings.Contains(l, match) {
				kept = append(kept, l)
			}
		}
		lines = kept
	}
	if len(lines) == 0 {
		if match != "" {
			return "", fmt.Errorf("no memory lines match %q — not an error. Broaden the match with op \"recall\" and a shorter substring, or save it first with op \"save\"", match)
		}
		return "memory is empty.", nil
	}
	out := lines
	note := ""
	if len(out) > memoryRecallLines {
		out = out[:memoryRecallLines]
		note = fmt.Sprintf("\n(showing first %d of %d lines — recall with a narrower \"match\" to see the rest)", memoryRecallLines, len(lines))
	}
	return Fence(strings.Join(out, "\n")) + note, nil
}

// forget deletes every line containing match and reports the exact count.
// Empty match is refused (it would wipe everything); to clear the file,
// forget each line's distinctive text instead.
func (t *Memory) forget(match string) (string, error) {
	if match == "" {
		return "", fmt.Errorf("op \"forget\" needs \"match\" as a non-empty string: pass the substring of the lines to delete")
	}
	lines, err := t.load()
	if err != nil {
		return "", err
	}
	var kept []string
	removed := 0
	for _, l := range lines {
		if strings.Contains(l, match) {
			removed++
			continue
		}
		kept = append(kept, l)
	}
	if removed == 0 {
		return "", fmt.Errorf("no memory lines contain %q — nothing forgotten. Recall first to see the exact wording, then retry", match)
	}
	// Write via the no-follow atomic helper; an emptied file truncates in
	// place (never deletes: the path identity stays stable for the next
	// save). The empty branch pre-checks the final-component symlink so
	// the refusal matches the read/open path wording.
	if len(kept) == 0 {
		full, err := t.full()
		if err != nil {
			return "", err
		}
		if st, err := os.Lstat(full); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing %q: symlink at the final path — point at a real file inside the project and retry", memoryRel)
		}
		if err := writeFileNoFollow(t.Root, memoryRel, []byte{}, 0o644); err != nil {
			return "", err
		}
	} else if err := writeFileNoFollow(t.Root, memoryRel, []byte(strings.Join(kept, "\n")+"\n"), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("forgot %d line(s) containing %q (%d remaining). Lines are stored date-prefixed (\"- YYYY-MM-DD: ...\"); match on the fact text, not the date.", removed, match, len(kept)), nil
}
