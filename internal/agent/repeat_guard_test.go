package agent

import (
	"strings"
	"testing"
)

// Identical nested args must fingerprint identically regardless of Go map
// iteration order, and different values must differ.
func TestFingerprintCanonicalizesNestedArgs(t *testing.T) {
	a := map[string]any{
		"x":    map[string]any{"b": 1, "a": 2},
		"list": []any{map[string]any{"k": "v"}, "z"},
	}
	b := map[string]any{
		"list": []any{map[string]any{"k": "v"}, "z"},
		"x":    map[string]any{"a": 2, "b": 1},
	}
	if fingerprint("t", a) != fingerprint("t", b) {
		t.Fatal("identical nested args must fingerprint identically")
	}
	c := map[string]any{"x": map[string]any{"a": 2, "b": 9}, "list": []any{map[string]any{"k": "v"}, "z"}}
	if fingerprint("t", a) == fingerprint("t", c) {
		t.Fatal("different nested values must produce different fingerprints")
	}
	if fingerprint("t", a) == fingerprint("u", a) {
		t.Fatal("different tool names must produce different fingerprints")
	}
}

func TestPreviewArgCapsAndFormats(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := previewArg(map[string]any{"path": long})
	if !strings.HasPrefix(got, " (") || !strings.HasSuffix(got, ")") {
		t.Fatalf("preview format = %q", got)
	}
	if len(got) > 70 {
		t.Fatalf("preview must be capped, got %d chars", len(got))
	}
	if previewArg(map[string]any{"unrelated": 1}) != "" {
		t.Fatal("no previewable key must yield an empty preview")
	}
}
