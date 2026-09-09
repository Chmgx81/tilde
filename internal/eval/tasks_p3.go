package eval

import (
	"os"
	"path/filepath"
	"strings"

	"tilde/internal/agent"
)

// P3Tasks covers the shipped P3/P4 tool surface: symbol_search,
// diagnose, memory save/recall, and the web_search default-off net
// gate. All hermetic (fresh trial dir, no network, deterministic
// fixtures); goals name exact tools like the rest of the suite, and
// every Verify scores the trajectory (ExpectTools-style tool-call
// evidence plus filesystem state) — never exact prose.
func P3Tasks() []Task {
	return []Task{
		{
			// Fixture carries one package-level func plus one method on
			// a named type, so a bare substring grep is not enough to
			// distinguish definitions — the run must go through the
			// symbol index, then open the defining file.
			Name:        "symbol-search-finds-def",
			Goal:        "Use symbol_search with query \"Greet\" to find where Greet is defined, then read the defining file with read_file and report the file path and line. Then report done.",
			MaxIters:    10,
			ExpectTools: []string{"symbol_search", "read_file"},
			Setup: func(root string) error {
				body := "package main\n\ntype Greeter struct{}\n\nfunc Greet(name string) string { return \"hi \" + name }\n\nfunc (g Greeter) Hello() string { return \"hello\" }\n"
				return os.WriteFile(filepath.Join(root, "greet.go"), []byte(body), 0o644)
			},
			Verify: func(root, _ string, events []agent.Event) (bool, string) {
				if !sawTool(events, "symbol_search") {
					return false, "no symbol_search call in trajectory"
				}
				if !toolCallHas(events, "read_file", "greet.go") {
					return false, "defining file greet.go never read"
				}
				if !toolResultHas(events, "greet.go") && !toolResultHas(events, "Greet") {
					return false, "symbol_search result never named Greet/greet.go"
				}
				data, err := os.ReadFile(filepath.Join(root, "greet.go"))
				if err != nil {
					return false, "greet.go missing"
				}
				if !strings.Contains(string(data), "func Greet") || !strings.Contains(string(data), "func (g Greeter)") {
					return false, "fixture damaged: func+method definitions gone"
				}
				return true, "ok"
			},
		},
		{
			// One gofmt-clean file next to one gofmt-dirty-but-parseable
			// file: the run must surface the dirty one by kind, not by
			// rewording the goal.
			Name:        "diagnose-clean-and-dirty",
			Goal:        "Run the diagnose tool over the repo root (path \".\") and report which files need gofmt and what the summary counts say. Then report done.",
			MaxIters:    10,
			ExpectTools: []string{"diagnose"},
			Setup: func(root string) error {
				if err := os.WriteFile(filepath.Join(root, "clean.go"), []byte("package main\n\nfunc Clean() int {\n\treturn 2\n}\n"), 0o644); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(root, "dirty.go"), []byte("package main\n\nfunc Dirty() int {\nreturn 1\n}\n"), 0o644)
			},
			Verify: func(root, _ string, events []agent.Event) (bool, string) {
				if !sawTool(events, "diagnose") {
					return false, "no diagnose call in trajectory"
				}
				if !toolResultHas(events, "gofmt") {
					return false, "diagnose findings never mentioned gofmt"
				}
				for _, n := range []string{"clean.go", "dirty.go"} {
					if _, err := os.Stat(filepath.Join(root, n)); err != nil {
						return false, n + " missing"
					}
				}
				return true, "ok"
			},
		},
		{
			// Save-then-recall round trip: the fact must land in the
			// project memory file AND be observed coming back through
			// a recall result — a save-only run fails the second half.
			Name:        "memory-save-recall",
			Goal:        "Use memory with op \"save\" to save the fact \"operator prefers tabs over spaces\", then use memory with op \"recall\" to read it back and report the recalled fact. Then report done.",
			MaxIters:    10,
			ExpectTools: []string{"memory"},
			Verify: func(root, _ string, events []agent.Event) (bool, string) {
				if countToolCalls(events, "memory") < 2 {
					return false, "need save+recall (fewer than 2 memory calls)"
				}
				data, err := os.ReadFile(filepath.Join(root, ".tilde", "memory.md"))
				if err != nil {
					return false, ".tilde/memory.md missing"
				}
				if !strings.Contains(strings.ToLower(string(data)), "prefers tabs") {
					return false, "memory.md missing the saved fact"
				}
				if !toolResultHas(events, "prefers tabs") {
					return false, "no recall result surfaced the fact"
				}
				return true, "ok"
			},
		},
		{
			// Default-off net gate: trials inherit the process env and
			// the repo ships no allow_net hosts, so web_search answers
			// with the disabled-gate message (or tool_unavailable on a
			// backend failure). The pass is one attempt plus a local
			// fallback read — a retry-spin fails.
			Name:        "web-search-unavailable",
			Goal:        "Call web_search once with query \"tilde release notes\". If the result says the network is disabled or the tool is unavailable, do NOT retry — read notes.txt with read_file instead and report the local fallback fact. Then report done.",
			MaxIters:    10,
			ExpectTools: []string{"web_search", "read_file"},
			Setup: func(root string) error {
				return os.WriteFile(filepath.Join(root, "notes.txt"), []byte("Local fallback fact: the vault code is 0000.\n"), 0o644)
			},
			Verify: func(root, _ string, events []agent.Event) (bool, string) {
				n := countToolCalls(events, "web_search")
				if n == 0 {
					return false, "no web_search attempt in trajectory"
				}
				if n > 2 {
					return false, "retry-spin: web_search called too many times"
				}
				if !toolResultHas(events, "network is disabled") && !toolResultHas(events, "tool_unavailable") {
					return false, "no disabled-gate/tool_unavailable marker (is TILDE_ALLOW_NET=1 set?)"
				}
				if !toolCallHas(events, "read_file", "notes.txt") {
					return false, "no local fallback read after denial"
				}
				if data, err := os.ReadFile(filepath.Join(root, "notes.txt")); err != nil || !strings.Contains(string(data), "0000") {
					return false, "notes.txt missing or damaged"
				}
				return true, "ok"
			},
		},
	}
}

// countToolCalls counts tool_call events for one tool.
func countToolCalls(events []agent.Event, name string) int {
	n := 0
	for _, e := range events {
		if e.Kind == "tool_call" && (e.Text == name || strings.HasPrefix(e.Text, name+" ")) {
			n++
		}
	}
	return n
}

// toolCallHas reports whether a tool_call for name mentions substr.
func toolCallHas(events []agent.Event, name, substr string) bool {
	for _, e := range events {
		if e.Kind != "tool_call" {
			continue
		}
		if e.Text == name || strings.HasPrefix(e.Text, name+" ") {
			if strings.Contains(e.Text, substr) {
				return true
			}
		}
	}
	return false
}

// toolResultHas reports whether any tool_result mentions substr
// (case-insensitive) — for asserting the model saw a finding, gate
// message, or recalled fact without matching exact prose.
func toolResultHas(events []agent.Event, substr string) bool {
	for _, e := range events {
		if e.Kind == "tool_result" && strings.Contains(strings.ToLower(e.Text), strings.ToLower(substr)) {
			return true
		}
	}
	return false
}
