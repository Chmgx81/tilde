// Package export distills a session JSONL transcript
// (~/.tilde/sessions/<id>.jsonl, see internal/session Entry) into a short
// markdown brief for /export. It never emits raw transcript or raw file
// contents — only distilled snippets (goal, recent assistant direction,
// trailing user messages, touched paths, command prefixes).
//
// Distillation heuristics (deliberately simple, documented here):
//   - goal: first non-empty user message text (data.text, else data.content).
//   - files touched: unique paths in encounter order from tool_call entries
//     whose data.tool is read_file, write_file, or edit_file (data.args.path),
//     followed by shell command prefixes from data.tool shell_command
//     (aliases shell/bash/exec tolerated): data.args.command truncated to
//     80 chars each, at most 10 commands.
//   - recent direction: last 3 non-empty assistant message texts, each
//     trimmed to 300 chars.
//   - open steps: non-empty user message texts after the last assistant
//     message (tail capped at 3); "none recorded" when there are none.
//   - counts: user turns, tool calls, corrupt lines skipped.
package export

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"tilde/internal/tools"
)

const (
	// defaultMaxChars applies when BriefFromFile gets maxChars <= 0.
	defaultMaxChars = 6000
	// maxInputLines caps how many JSONL lines are read from the log.
	maxInputLines = 5000
	// maxShellCommands caps how many shell command prefixes are listed.
	maxShellCommands = 10
	// maxCommandLen truncates each listed shell command.
	maxCommandLen = 80
	// maxAssistantLen truncates each recent-direction snippet.
	maxAssistantLen = 300
	// maxRecentAssistant caps how many assistant snippets are listed.
	maxRecentAssistant = 3
	// maxOpenSteps caps how many trailing user messages are listed.
	maxOpenSteps = 3
)

// fileTools are the tool_call names whose args.path counts as a touched file.
var fileTools = map[string]bool{
	"read_file":  true,
	"write_file": true,
	"edit_file":  true,
}

// shellTools are the tool_call names whose args.command counts as a command
// prefix. shell_command is the real tool name (see internal/tools/shell.go);
// the rest are tolerated aliases.
var shellTools = map[string]bool{
	"shell_command": true,
	"shell":         true,
	"bash":          true,
	"exec":          true,
}

// entry mirrors session.Entry (internal/session is append-only by design,
// so the shape is re-declared here for reading).
type entry struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// BriefFromFile reads a session JSONL log line by line, distills it into a
// markdown brief, scrubs secrets via tools.Scrub, and truncates the result
// to maxChars (default 6000 when <= 0) with a truncation note.
//
// Corrupt lines are skipped and counted. At most 5000 input lines are read.
// A missing/unreadable file fails loud; a log with no valid entries errors.
func BriefFromFile(path string, maxChars int) (string, error) {
	if maxChars <= 0 {
		maxChars = defaultMaxChars
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("export: cannot open %s: %w", path, err)
	}
	defer f.Close()

	var (
		entries  []entry
		corrupt  int
		lines    int
		capped   bool
		userMsgs []string // all non-empty user texts, in order
		asstMsgs []string // all non-empty assistant texts, in order
		lastAsst = -1     // index into entries of last non-empty assistant msg
	)
	toolCalls := 0

	r := bufio.NewReader(f)
	for {
		if lines >= maxInputLines {
			capped = true
			break
		}
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			lines++
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				// Blank line: ignore silently, not corruption.
			} else {
				var e entry
				if jerr := json.Unmarshal([]byte(trimmed), &e); jerr != nil {
					corrupt++
				} else {
					entries = append(entries, e)
					idx := len(entries) - 1
					switch e.Type {
					case "user":
						if t := messageText(e.Data); t != "" {
							userMsgs = append(userMsgs, t)
						}
					case "assistant":
						if t := messageText(e.Data); t != "" {
							asstMsgs = append(asstMsgs, t)
							lastAsst = idx
						}
					case "tool_call":
						toolCalls++
					}
				}
			}
		}
		if err != nil {
			break // io.EOF (possibly after a final unterminated line, handled above)
		}
	}

	if len(entries) == 0 {
		return "", fmt.Errorf("export: empty log: no valid entries in %s (%d corrupt lines skipped)", path, corrupt)
	}

	goal := "none recorded"
	if len(userMsgs) > 0 {
		goal = userMsgs[0]
	}

	files, commands := touchedPaths(entries)

	var recent []string
	if len(asstMsgs) > maxRecentAssistant {
		recent = asstMsgs[len(asstMsgs)-maxRecentAssistant:]
	} else {
		recent = asstMsgs
	}
	for i, t := range recent {
		recent[i] = truncateRunes(t, maxAssistantLen)
	}

	var open []string
	if lastAsst >= 0 {
		for _, e := range entries[lastAsst+1:] {
			if e.Type == "user" {
				if t := messageText(e.Data); t != "" {
					open = append(open, t)
				}
			}
		}
		if len(open) > maxOpenSteps {
			open = open[len(open)-maxOpenSteps:]
		}
	}

	var b strings.Builder
	b.WriteString("# Session brief\n\n")
	b.WriteString("## Goal\n\n")
	b.WriteString(goal + "\n\n")
	b.WriteString("## Files touched\n\n")
	if len(files) == 0 && len(commands) == 0 {
		b.WriteString("none recorded\n\n")
	} else {
		for _, p := range files {
			b.WriteString("- " + p + "\n")
		}
		for _, c := range commands {
			b.WriteString("- `$ " + c + "`\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Recent direction\n\n")
	if len(recent) == 0 {
		b.WriteString("none recorded\n\n")
	} else {
		for _, t := range recent {
			b.WriteString("- " + singleLine(t) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Open steps\n\n")
	if len(open) == 0 {
		b.WriteString("none recorded\n\n")
	} else {
		for _, t := range open {
			b.WriteString("- " + singleLine(t) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Counts\n\n")
	fmt.Fprintf(&b, "- user turns: %d\n- tool calls: %d\n- corrupt lines skipped: %d\n", len(userMsgs), toolCalls, corrupt)
	if capped {
		fmt.Fprintf(&b, "- input capped at %d lines\n", maxInputLines)
	}

	scrubbed, _ := tools.Scrub(b.String())
	if len(scrubbed) > maxChars {
		note := fmt.Sprintf("\n\n[truncated: brief capped at %d chars]", maxChars)
		cut := maxChars - len(note)
		if cut < 0 {
			cut = 0
		}
		head := scrubbed
		if len(head) > cut {
			head = head[:cut]
		}
		for len(head) > 0 && !utf8.ValidString(head) {
			head = head[:len(head)-1]
		}
		scrubbed = head + note
	}
	return scrubbed, nil
}

// touchedPaths returns unique touched file paths in first-encounter order
// plus shell command prefixes (each capped at 80 chars, at most 10).
func touchedPaths(entries []entry) (files []string, commands []string) {
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Type != "tool_call" {
			continue
		}
		tool, _ := e.Data["tool"].(string)
		args, _ := e.Data["args"].(map[string]any)
		if args == nil {
			continue
		}
		switch {
		case fileTools[tool]:
			if p, _ := args["path"].(string); p != "" && !seen[p] {
				seen[p] = true
				files = append(files, p)
			}
		case shellTools[tool]:
			if len(commands) >= maxShellCommands {
				continue
			}
			if c, _ := args["command"].(string); strings.TrimSpace(c) != "" {
				commands = append(commands, truncateRunes(strings.TrimSpace(c), maxCommandLen))
			}
		}
	}
	return files, commands
}

// messageText extracts a user/assistant message body: data.text, else
// data.content (string form only — never tool output or file contents).
func messageText(data map[string]any) string {
	if data == nil {
		return ""
	}
	if s, _ := data["text"].(string); strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	if s, _ := data["content"].(string); strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	return ""
}

// singleLine flattens a snippet so brief bullets stay one line each.
func singleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateRunes cuts s to at most n runes.
func truncateRunes(s string, n int) string {
	if n < 0 {
		n = 0
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
