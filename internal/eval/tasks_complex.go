package eval

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"tilde/internal/agent"
)

// ComplexTasks extends the suite past toy demos: retrieval under
// distractors, multi-hop debug with a forbidden path, and a longer
// fan-out + shell-verified chain. All run natively under bwrap via
// the standard harness — no Docker, no network.
func ComplexTasks() []Task {
	return []Task{
		{
			Name:        "navigate-distractors",
			Goal:        "Use grep to find which .txt file under notes/ contains the word TARGET, read that file with read_file, then use edit_file to replace TARGET with FOUND. Then report done.",
			MaxIters:    14,
			ExpectTools: []string{"grep", "read_file", "edit_file"},
			Setup: func(root string) error {
				dir := filepath.Join(root, "notes")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
				for i := 0; i < 19; i++ {
					body := fmt.Sprintf("note %d: nothing here, see TARGE_T adjacent\n", i)
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("n%02d.txt", i)), []byte(body), 0o644); err != nil {
						return err
					}
				}
				return os.WriteFile(filepath.Join(dir, "real.txt"), []byte("holds TARGET token\n"), 0o644)
			},
			Verify: func(root, _ string, _ []agent.Event) (bool, string) {
				data, err := os.ReadFile(filepath.Join(root, "notes", "real.txt"))
				if err != nil {
					return false, "notes/real.txt missing"
				}
				if strings.TrimSpace(string(data)) != "holds FOUND token" {
					return false, "real.txt=" + trunc(strings.TrimSpace(string(data)), 60)
				}
				return true, "ok"
			},
		},
		{
			Name:        "multi-hop-debug",
			Goal:        "Run shell_command `go test ./...`, trace the failure across files, fix the bug with edit_file so the tests pass, then report done. Do not change the test file.",
			MaxIters:    20,
			ExpectTools: []string{"shell_command", "read_file", "edit_file"},
			Setup: func(root string) error {
				files := map[string]string{
					"go.mod":       "module evalmultihop\n\ngo 1.21\n",
					"main.go":      "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(Greet(\"hi\")) }\n",
					"calc.go":      "package main\n\nfunc Shout(s string) string { return s }\n",
					"greet.go":     "package main\n\nfunc Greet(s string) string { return Shout(s) + \"!\" }\n",
					"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestGreet(t *testing.T) {\n\tif Greet(\"hi\") != \"HI!\" {\n\t\tt.Fatalf(\"Greet(hi)=%q want HI!\", Greet(\"hi\"))\n\t}\n}\n",
				}
				// Bug is one hop away from the failing symbol: Shout must upper-case.
				files["calc.go"] = "package main\n\nfunc Shout(s string) string { return s }\n"
				for n, c := range files {
					if err := os.WriteFile(filepath.Join(root, n), []byte(c), 0o644); err != nil {
						return err
					}
				}
				return nil
			},
			Verify: func(root, _ string, _ []agent.Event) (bool, string) {
				want := "package main\n\nimport \"testing\"\n\nfunc TestGreet(t *testing.T) {\n\tif Greet(\"hi\") != \"HI!\" {\n\t\tt.Fatalf(\"Greet(hi)=%q want HI!\", Greet(\"hi\"))\n\t}\n}\n"
				if data, err := os.ReadFile(filepath.Join(root, "main_test.go")); err != nil || string(data) != want {
					return false, "test file must not change"
				}
				cmd := exec.Command("go", "test", "./...")
				cmd.Dir = root
				if out, err := cmd.CombinedOutput(); err != nil {
					return false, "go test failed: " + trunc(string(out), 200)
				}
				return true, "ok"
			},
		},
		{
			Name:        "fix-and-extend",
			Goal:        "Use grep to find all .txt files containing TODO, read each one, use edit_file on each to replace TODO with DONE, then run shell_command `grep -r TODO . || true` to verify none remain. Then report done.",
			MaxIters:    20,
			ExpectTools: []string{"grep", "read_file", "edit_file", "shell_command"},
			Setup: func(root string) error {
				for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"} {
					if err := os.WriteFile(filepath.Join(root, n), []byte("task TODO here\n"), 0o644); err != nil {
						return err
					}
				}
				return nil
			},
			Verify: func(root, _ string, _ []agent.Event) (bool, string) {
				for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"} {
					data, err := os.ReadFile(filepath.Join(root, n))
					if err != nil {
						return false, n + " missing"
					}
					if strings.Contains(string(data), "TODO") {
						return false, n + " still contains TODO"
					}
					if !strings.Contains(string(data), "DONE") {
						return false, n + " missing DONE"
					}
				}
				return true, "ok"
			},
		},
		{
			// Failing-shape target: repair-go-test retries broke adjacent
			// files (observed: `undefined: fmt` after the fix). The fix
			// must be surgical — every non-target file byte-identical.
			Name:        "repair-no-collateral",
			Goal:        "Run shell_command `go test ./...`, read the failure, fix the bug with edit_file so the tests pass, then report done. Do not change the test file or any file other than the buggy one.",
			MaxIters:    20,
			ExpectTools: []string{"shell_command", "read_file", "edit_file"},
			Setup: func(root string) error {
				files := map[string]string{
					"go.mod":       "module evalnocollat\n\ngo 1.21\n",
					"main.go":      "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(Double(21)) }\n",
					"double.go":    "package main\n\nfunc Double(n int) int { return n * 3 }\n",
					"helper.go":    "package main\n\nimport \"strings\"\n\nfunc Shout(s string) string { return strings.ToUpper(s) }\n",
					"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestDouble(t *testing.T) {\n\tif Double(21) != 42 {\n\t\tt.Fatalf(\"Double(21)=%d want 42\", Double(21))\n\t}\n}\n",
				}
				for n, c := range files {
					if err := os.WriteFile(filepath.Join(root, n), []byte(c), 0o644); err != nil {
						return err
					}
				}
				return nil
			},
			Verify: func(root, _ string, _ []agent.Event) (bool, string) {
				untouched := map[string]string{
					"main.go":      "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(Double(21)) }\n",
					"helper.go":    "package main\n\nimport \"strings\"\n\nfunc Shout(s string) string { return strings.ToUpper(s) }\n",
					"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestDouble(t *testing.T) {\n\tif Double(21) != 42 {\n\t\tt.Fatalf(\"Double(21)=%d want 42\", Double(21))\n\t}\n}\n",
				}
				for n, want := range untouched {
					if data, err := os.ReadFile(filepath.Join(root, n)); err != nil || string(data) != want {
						return false, n + " must not change"
					}
				}
				cmd := exec.Command("go", "test", "./...")
				cmd.Dir = root
				if out, err := cmd.CombinedOutput(); err != nil {
					return false, "go test failed: " + trunc(string(out), 200)
				}
				return true, "ok"
			},
		},
		{
			// Long-horizon proof: a small context budget forces mid-task
			// auto-compaction (fallback summary, no model call). The goal
			// must survive it — end state correct AND a compacted marker
			// in the trajectory.
			Name:        "compact-survives",
			Goal:        "Use grep to find all .txt files containing SIGIL, read each one with read_file, then use edit_file on each to replace SIGIL with RELIC. Then report done.",
			MaxIters:    22,
			Budget:      2500,
			ExpectTools: []string{"grep", "read_file", "edit_file"},
			Setup: func(root string) error {
				for _, n := range []string{"g1.txt", "g2.txt", "g3.txt", "g4.txt"} {
					var b strings.Builder
					b.WriteString("holds SIGIL token\n")
					for i := 0; i < 40; i++ {
						fmt.Fprintf(&b, "filler line %02d for context pressure lorem ipsum dolor sit amet\n", i)
					}
					if err := os.WriteFile(filepath.Join(root, n), []byte(b.String()), 0o644); err != nil {
						return err
					}
				}
				return nil
			},
			Verify: func(root, _ string, events []agent.Event) (bool, string) {
				for _, n := range []string{"g1.txt", "g2.txt", "g3.txt", "g4.txt"} {
					data, err := os.ReadFile(filepath.Join(root, n))
					if err != nil {
						return false, n + " missing"
					}
					if strings.Contains(string(data), "SIGIL") {
						return false, n + " still contains SIGIL"
					}
					if !strings.Contains(string(data), "RELIC") {
						return false, n + " missing RELIC"
					}
				}
				if !sawKind(events, "compacted") {
					return false, "no compaction fired — budget too generous to prove survival"
				}
				return true, "ok"
			},
		},
		{
			// Failing-shape target: fix-and-extend trials skipped read_file
			// (right end state, wrong trajectory — rejected) or died at the
			// cap. Six files plus an explicit read-each-first goal.
			Name:        "chain-read-first",
			Goal:        "Use grep to find all .txt files containing MARK, read each one with read_file, then use edit_file on each to replace MARK with SEALED, then run shell_command `grep -r MARK . || true` to verify none remain. Then report done.",
			MaxIters:    22,
			ExpectTools: []string{"grep", "read_file", "edit_file", "shell_command"},
			Setup: func(root string) error {
				for _, n := range []string{"f1.txt", "f2.txt", "f3.txt", "f4.txt", "f5.txt", "f6.txt"} {
					if err := os.WriteFile(filepath.Join(root, n), []byte("holds MARK token\n"), 0o644); err != nil {
						return err
					}
				}
				return nil
			},
			Verify: func(root, _ string, _ []agent.Event) (bool, string) {
				for _, n := range []string{"f1.txt", "f2.txt", "f3.txt", "f4.txt", "f5.txt", "f6.txt"} {
					data, err := os.ReadFile(filepath.Join(root, n))
					if err != nil {
						return false, n + " missing"
					}
					if strings.Contains(string(data), "MARK") {
						return false, n + " still contains MARK"
					}
					if !strings.Contains(string(data), "SEALED") {
						return false, n + " missing SEALED"
					}
				}
				return true, "ok"
			},
		},
	}
}
