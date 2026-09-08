package repair

import (
	"testing"
)

func testSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path":    map[string]any{"type": "string"},
		"limit":   map[string]any{"type": "integer"},
		"tags":    map[string]any{"type": "array"},
		"recurse": map[string]any{"type": "boolean"},
		"filter":  map[string]any{"type": "object"},
	}}
}

func TestDropNulls(t *testing.T) {
	out, notes := Normalize(map[string]any{"path": "a.txt", "limit": nil}, testSchema())
	if _, ok := out["limit"]; ok {
		t.Fatalf("null not dropped: %v", out)
	}
	if len(notes) != 1 {
		t.Fatalf("expected 1 note, got %v", notes)
	}
}

func TestParseJSONStringArray(t *testing.T) {
	out, notes := Normalize(map[string]any{"tags": `["a","b"]`}, testSchema())
	a, ok := out["tags"].([]any)
	if !ok || len(a) != 2 {
		t.Fatalf("not parsed to array: %v (%v)", out, notes)
	}
}

func TestUnwrapSingleKeyToArray(t *testing.T) {
	out, _ := Normalize(map[string]any{"tags": map[string]any{"value": []any{"x"}}}, testSchema())
	if a, ok := out["tags"].([]any); !ok || len(a) != 1 {
		t.Fatalf("not unwrapped: %v", out)
	}
}

func TestWrapScalarAsArray(t *testing.T) {
	out, _ := Normalize(map[string]any{"tags": "solo"}, testSchema())
	if a, ok := out["tags"].([]any); !ok || len(a) != 1 || a[0] != "solo" {
		t.Fatalf("not wrapped: %v", out)
	}
}

func TestCoerceIntAndBool(t *testing.T) {
	out, _ := Normalize(map[string]any{"limit": "25", "recurse": "true"}, testSchema())
	if f, ok := out["limit"].(float64); !ok || f != 25 {
		t.Fatalf("int not coerced: %v", out)
	}
	if b, ok := out["recurse"].(bool); !ok || !b {
		t.Fatalf("bool not coerced: %v", out)
	}
}

func TestLeavesGoodInputAlone(t *testing.T) {
	in := map[string]any{"path": "a.txt", "limit": float64(10)}
	out, notes := Normalize(in, testSchema())
	if len(notes) != 0 {
		t.Fatalf("clean input drew repairs: %v", notes)
	}
	if out["path"] != "a.txt" {
		t.Fatalf("clean input altered: %v", out)
	}
}

func TestNilArgsSafe(t *testing.T) {
	out, notes := Normalize(nil, testSchema())
	if len(out) != 0 || len(notes) != 0 {
		t.Fatalf("got %v %v", out, notes)
	}
}

func TestBadJSONStringLeftForValidator(t *testing.T) {
	// Unparseable stays a string: the tool's own validator names the real fix.
	out, notes := Normalize(map[string]any{"tags": "[oops"}, testSchema())
	if s, ok := out["tags"].(string); !ok || s != "[oops" {
		t.Fatalf("should have left it alone: %v (%v)", out, notes)
	}
}

func TestTypedStringSliceLiftedNotNested(t *testing.T) {
	// A typed []string for an array field must lift to []any — not wrap
	// as a single nested element ([[a b]]).
	out, notes := Normalize(map[string]any{"tags": []string{"a", "b"}}, testSchema())
	a, ok := out["tags"].([]any)
	if !ok || len(a) != 2 || a[0] != "a" || a[1] != "b" {
		t.Fatalf("typed slice nested instead of lifted: %v (%v)", out, notes)
	}
	if len(notes) != 1 {
		t.Fatalf("expected 1 repair note, got %v", notes)
	}
}

func TestBoolToIntNotLaundered(t *testing.T) {
	// A real bool where an integer belongs is a type error for the
	// validator to report — never silently 0/1.
	out, _ := Normalize(map[string]any{"limit": true}, testSchema())
	if _, ok := out["limit"].(bool); !ok {
		t.Fatalf("bool input must pass through untouched, got %v", out)
	}
}
