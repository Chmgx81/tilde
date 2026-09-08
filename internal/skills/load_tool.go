package skills

import (
	"context"
	"fmt"
	"strings"

	"tilde/internal/tools"
)

// LoadTool is the model-facing half of progressive disclosure: bodies
// load per-use via a tool call instead of bloating every prompt.
type LoadTool struct {
	Index *Index // re-scanned by the owner; nil = no skills installed
}

func (t *LoadTool) Name() string { return "load_skill" }
func (t *LoadTool) Description() string {
	return "Load a skill's full instructions by name. Skills are listed in your system prompt with one-line descriptions — call this only for skills relevant to the task."
}
func (t *LoadTool) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "Skill name from the system prompt list"},
		}, "required": []string{"name"}}
}

func (t *LoadTool) Exec(_ context.Context, args map[string]any) (string, error) {
	v, ok := args["name"]
	if !ok || v == nil {
		return "", fmt.Errorf("missing required argument \"name\": pass a skill name from the system prompt list")
	}
	name, ok := v.(string)
	if !ok || name == "" {
		return "", fmt.Errorf("argument \"name\" must be a non-empty string")
	}
	if t.Index == nil {
		return "", fmt.Errorf("no skills installed: there is no skill %q to load", name)
	}
	sk, ok := t.Index.Get(name)
	if !ok {
		names := t.Index.Names()
		if len(names) == 0 {
			return "", fmt.Errorf("no skills installed: nothing to load")
		}
		return "", fmt.Errorf("unknown skill %q. Available: %s. Send one of these names instead", name, strings.Join(names, ", "))
	}
	return fmt.Sprintf("[skill loaded: %s (%s)]\n%s", sk.Name, sk.Scope, sk.Body), nil
}

var _ tools.Tool = (*LoadTool)(nil)
