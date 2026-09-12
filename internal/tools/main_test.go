package tools

import (
	"os"
	"testing"
)

// TestMain keeps tool-output spill inside a throwaway dir so tests never
// write to the real ~/.tilde/spill.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tilde-spill-test-*")
	if err == nil {
		os.Setenv("TILDE_SPILL_DIR", dir)
	}
	code := m.Run()
	if err == nil {
		os.RemoveAll(dir)
	}
	os.Exit(code)
}
