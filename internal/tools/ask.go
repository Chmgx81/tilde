package tools

import (
	"context"
	"fmt"
	"strings"
)

// Ask routes an unanswerable-or-risky decision to the host callback.
type Ask struct {
	AskUser func(tool string, args map[string]any) bool
}

func (t *Ask) Name() string { return "ask_user" }
func (t *Ask) Description() string {
	return "Ask the user a question; routes to the host. Deny-by-default when unwired."
}
func (t *Ask) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"question": map[string]any{"type": "string"},
			"options":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}, "required": []string{"question"}}
}

func (t *Ask) Exec(_ context.Context, args map[string]any) (string, error) {
	q, err := strArg(args, "question")
	if err != nil {
		return "", err
	}
	if t.AskUser == nil {
		return fmt.Sprintf("ask_user denied by policy: no host callback wired, so the question %q is unanswerable — treat as denied and propose a safe default instead.", q), nil
	}
	opts := ""
	if v, ok := args["options"]; ok {
		if list, ok := v.([]any); ok && len(list) > 0 {
			var parts []string
			for _, o := range list {
				parts = append(parts, fmt.Sprint(o))
			}
			opts = " Options: " + strings.Join(parts, " | ")
		}
	}
	if ok := t.AskUser(t.Name(), args); !ok {
		return fmt.Sprintf("user denied: %q.%s Do not retry; proceed with a safe default.", q, opts), nil
	}
	return fmt.Sprintf("user approved: %q.%s Proceed accordingly.", q, opts), nil
}
