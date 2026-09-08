package trust

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrustRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "proj")
	if IsTrusted(dir) {
		t.Fatalf("miss must read as denied")
	}
	if err := SetTrusted(dir, true); err != nil {
		t.Fatalf("set: %v", err)
	}
	if !IsTrusted(dir) {
		t.Fatalf("set true must read back trusted")
	}
	if st, err := os.Stat(filepath.Join(home, ".tilde", "trusted.json")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("store must be 0600: %v %v", st, err)
	}
	if err := SetTrusted(dir, false); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if IsTrusted(dir) {
		t.Fatalf("cleared must read as denied")
	}
}
