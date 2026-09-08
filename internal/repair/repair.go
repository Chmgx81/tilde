// Package repair — the tool-call repair layer (docs/Plan.md Phase 3, top priority).
//
// Local/open models make a small, finite, predictable set of tool-call
// mistakes: null instead of omitting an optional field, a JSON-stringified
// array instead of a real one, a bare value wrapped in {"value":…} where an
// array was expected, a number sent as a string. Rejecting these burns a
// turn per mistake; repairing them at the harness layer makes the calls
// land. Called once per dispatch, before validation — anything still broken
// afterwards falls through to a model-readable error as before.
package repair

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Normalize applies every applicable repair to args given a JSON-schema-like
// tool schema (the same map the model saw). It returns the fixed args plus a
// short receipt of what changed (empty when nothing did).
func Normalize(args map[string]any, schema map[string]any) (map[string]any, []string) {
	if args == nil {
		return map[string]any{}, nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	props := properties(schema)
	var fixed []string

	for k, v := range out {
		if v == nil {
			delete(out, k)
			fixed = append(fixed, fmt.Sprintf("dropped null %q (omit optional fields instead of sending null)", k))
			continue
		}
		want := propType(props, k)
		if s, ok := v.(string); ok && (want == "array" || want == "object") {
			if parsed, ok := tryJSON(s, want); ok {
				out[k] = parsed
				fixed = append(fixed, fmt.Sprintf("parsed JSON-string %q as %s", k, want))
				continue
			}
		}
		if want == "array" {
			// Typed string slices decode as []string, not []any — lift
			// them instead of wrapping the whole slice as one element
			// ([[a b]]), which launders a good value into a nested one.
			if ss, ok := v.([]string); ok {
				a := make([]any, len(ss))
				for i, s := range ss {
					a[i] = s
				}
				out[k] = a
				fixed = append(fixed, fmt.Sprintf("lifted typed []string %q to array", k))
				continue
			}
			if m, ok := v.(map[string]any); ok && len(m) == 1 {
				for _, wk := range []string{"value", "input", "arg", "text", "items"} {
					if inner, ok := m[wk]; ok {
						if _, isArr := inner.([]any); isArr {
							out[k] = inner
						} else {
							out[k] = []any{inner}
						}
						fixed = append(fixed, fmt.Sprintf("unwrapped %q.%s to array", k, wk))
						break
					}
				}
				continue
			}
			if _, isArr := v.([]any); !isArr {
				if _, isMap := v.(map[string]any); !isMap {
					// A scalar that *looks* like attempted JSON but failed to
					// parse is left for the validator — wrapping it would
					// launder a real syntax error into a wrong-typed value.
					if s, ok := v.(string); ok {
						if ts := strings.TrimSpace(s); strings.HasPrefix(ts, "[") || strings.HasPrefix(ts, "{") {
							continue
						}
					}
					out[k] = []any{v}
					fixed = append(fixed, fmt.Sprintf("wrapped scalar %q as single-element array", k))
				}
			}
			continue
		}
		if want == "integer" {
			if coerced, ok := coerceInt(v); ok {
				out[k] = coerced
				fixed = append(fixed, fmt.Sprintf("coerced %q to integer", k))
			}
			continue
		}
		if want == "boolean" {
			if coerced, ok := coerceBool(v); ok {
				out[k] = coerced
				fixed = append(fixed, fmt.Sprintf("coerced %q to boolean", k))
			}
		}
	}
	sort.Strings(fixed)
	return out, fixed
}

func properties(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	p, _ := schema["properties"].(map[string]any)
	return p
}

func propType(props map[string]any, key string) string {
	if props == nil {
		return ""
	}
	def, _ := props[key].(map[string]any)
	if def == nil {
		return ""
	}
	t, _ := def["type"].(string)
	return strings.ToLower(t)
}

func tryJSON(s, want string) (any, bool) {
	t := strings.TrimSpace(s)
	if want == "array" && !strings.HasPrefix(t, "[") {
		return nil, false
	}
	if want == "object" && !strings.HasPrefix(t, "{") {
		return nil, false
	}
	var v any
	if err := json.Unmarshal([]byte(t), &v); err != nil {
		return nil, false
	}
	switch want {
	case "array":
		if a, ok := v.([]any); ok {
			return a, true
		}
	case "object":
		if m, ok := v.(map[string]any); ok {
			return m, true
		}
	}
	return nil, false
}

func coerceInt(v any) (any, bool) {
	// Strings only. A real bool here is a type error the validator should
	// report, not something to launder into 0/1.
	if s, ok := v.(string); ok {
		if i, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return float64(i), true // JSON numbers decode as float64; match that
		}
	}
	return nil, false
}

func coerceBool(v any) (any, bool) {
	if s, ok := v.(string); ok {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		}
	}
	return nil, false
}
