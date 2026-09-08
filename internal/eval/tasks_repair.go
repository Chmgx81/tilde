package eval

import (
	"os"
	"os/exec"
	"path/filepath"

	"tilde/internal/agent"
)

// RepairTasks is the repair suite: one failing-go-test scenario.
func RepairTasks() []Task {
	return []Task{
		{
			Name:        "repair-go-test",
			Goal:        "Run shell_command `go test ./...`, read the failure, fix the bug with edit_file so the tests pass, then report done. Do not change the test file.",
			MaxIters:    16,
			ExpectTools: []string{"shell_command", "edit_file"},
			Setup: func(root string) error {
				if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module evalrepair\n\ngo 1.21\n"), 0o644); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(root, "add.go"), []byte("package main\n\nfunc Add(a, b int) int { return a - b }\n"), 0o644); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(root, "main_test.go"), []byte("package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 1) != 2 {\n\t\tt.Fatalf(\"Add(1,1)=%d want 2\", Add(1, 1))\n\t}\n}\n"), 0o644)
			},
			Verify: func(root, _ string, _ []agent.Event) (bool, string) {
				cmd := exec.Command("go", "test", "./...")
				cmd.Dir = root
				if out, err := cmd.CombinedOutput(); err != nil {
					return false, "go test failed: " + trunc(string(out), 200)
				}
				return true, "ok"
			},
		},
	}
}
