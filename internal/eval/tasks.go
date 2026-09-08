package eval

import (
	"os"
	"path/filepath"
	"strings"

	"tilde/internal/agent"
)

// DefaultTasks is the v1 suite: small, fast, strictly verified. Goals are
// deliberately explicit (exact tool names) — this measures harness
// consistency, not the model's ability to guess intent.
func DefaultTasks() []Task {
	return []Task{
		{
			Name:     "write-two-files",
			Goal:     "Create two files with write_file: a.txt containing exactly ALPHA and b.txt containing exactly BETA. Then report done.",
			MaxIters: 10,
			Verify: func(root, _ string, _ []agent.Event) (bool, string) {
				return checkFiles(root, map[string]string{"a.txt": "ALPHA", "b.txt": "BETA"})
			},
		},
		{
			Name:     "edit-after-read",
			Goal:     "Read seed.txt with read_file, then use edit_file exactly once to replace SEED with SPROUT. Then report done.",
			MaxIters: 10,
			Setup: func(root string) error {
				return os.WriteFile(filepath.Join(root, "seed.txt"), []byte("SEED\n"), 0o644)
			},
			Verify: func(root, _ string, _ []agent.Event) (bool, string) {
				return checkFiles(root, map[string]string{"seed.txt": "SPROUT"})
			},
		},
		{
			Name:     "grep-then-edit",
			Goal:     "Use grep to find which .txt file contains the word NEEDLE, read that file, then use edit_file to replace NEEDLE with THREAD. Then report done.",
			MaxIters: 12,
			Setup: func(root string) error {
				if err := os.WriteFile(filepath.Join(root, "hay1.txt"), []byte("plain hay\n"), 0o644); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(root, "hay2.txt"), []byte("has NEEDLE here\n"), 0o644)
			},
			ExpectTools: []string{"grep", "read_file", "edit_file"},
			Verify: func(root, _ string, _ []agent.Event) (bool, string) {
				data, err := os.ReadFile(filepath.Join(root, "hay2.txt"))
				if err != nil {
					return false, "hay2.txt missing"
				}
				if strings.TrimSpace(string(data)) != "has THREAD here" {
					return false, "hay2.txt=" + trunc(strings.TrimSpace(string(data)), 60)
				}
				return true, "ok"
			},
		},
		{
			Name:        "shell-verify",
			Goal:        "Create out.txt containing exactly DATA123 with write_file, then run shell_command with exactly the command `cat out.txt` to verify it. Then report done.",
			MaxIters:    10,
			ExpectTools: []string{"write_file", "shell_command"},
			Verify: func(root, _ string, _ []agent.Event) (bool, string) {
				data, err := os.ReadFile(filepath.Join(root, "out.txt"))
				if err != nil || strings.TrimSpace(string(data)) != "DATA123" {
					return false, "out.txt wrong or missing"
				}
				return true, "ok"
			},
		},
		{
			Name:     "plan-answers-no-touch",
			Mode:     "plan",
			Goal:     "Read info.txt with read_file and reply with the word after 'color:' (just the word, e.g. blue). Do not create or change any file.",
			MaxIters: 8,
			Setup: func(root string) error {
				return os.WriteFile(filepath.Join(root, "info.txt"), []byte("color: blue\nsize: big\n"), 0o644)
			},
			Verify: func(root, final string, events []agent.Event) (bool, string) {
				if sawTool(events, "write_file") || sawTool(events, "edit_file") || sawTool(events, "shell_command") {
					return false, "mutating tool fired in Plan mode"
				}
				if !strings.Contains(strings.ToLower(final), "blue") {
					return false, "answer missing 'blue': " + trunc(final, 80)
				}
				entries, _ := os.ReadDir(root)
				if len(entries) != 1 {
					return false, "workdir changed during read-only task"
				}
				return true, "ok"
			},
		},
	}
}

func checkFiles(root string, want map[string]string) (bool, string) {
	for name, content := range want {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return false, name + " missing"
		}
		got := strings.TrimSpace(string(data))
		if got != content {
			return false, name + "=" + trunc(got, 60)
		}
	}
	return true, "ok"
}

func sawTool(events []agent.Event, name string) bool {
	for _, e := range events {
		if e.Kind == "tool_call" && strings.HasPrefix(e.Text, name+" ") || e.Kind == "tool_call" && e.Text == name {
			return true
		}
	}
	return false
}
