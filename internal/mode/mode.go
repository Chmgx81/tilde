// Package mode — Plan / Build / Auto gate, enforced at the registry layer.
package mode

import "strings"

// Mode is the autonomy tier.
type Mode int

const (
	Plan Mode = iota
	Build
	Auto
)

func (m Mode) String() string {
	switch m {
	case Plan:
		return "Plan"
	case Build:
		return "Build"
	case Auto:
		return "Auto"
	}
	return "?"
}

// Cycle advances Plan -> Build -> Auto -> Plan (Tab behavior).
func (m Mode) Cycle() Mode {
	switch m {
	case Plan:
		return Build
	case Build:
		return Auto
	default:
		return Plan
	}
}

// Mutating tools — blocked entirely in Plan mode.
// mcp_call counts: a server tool can do anything its server allows.
var mutating = map[string]bool{
	"write_file":          true,
	"edit_file":           true,
	"shell_command":       true,
	"git_worktree_add":    true,
	"git_worktree_remove": true,
	"mcp_call":            true,
}

// IsMutating reports whether a tool can change state.
func IsMutating(name string) bool { return mutating[name] }

// IsMutatingCall is the arg-aware form: shell_poll status/log are
// read-only polls, but shell_poll kill stops a live process — mutating,
// so Plan-blocked. A name-only map entry would wrongly block the polls.
func IsMutatingCall(name string, args map[string]any) bool {
	if name == "shell_poll" {
		if a, _ := args["action"].(string); strings.EqualFold(strings.TrimSpace(a), "kill") {
			return true
		}
		return false
	}
	return mutating[name]
}

// GateError is returned when Plan mode blocks a call.
type GateError struct{ Tool string }

func (e *GateError) Error() string {
	return "Plan mode is read-only: tool " + e.Tool + " is blocked. Switch to Build (Tab) with an approved plan, or gather more context with read-only tools instead."
}

// Allowed reports whether a tool may run in this mode.
func (m Mode) Allowed(tool string) error {
	if m == Plan && IsMutating(tool) {
		return &GateError{Tool: tool}
	}
	return nil
}

// AllowedCall is the arg-aware form (shell_poll kill is Plan-blocked;
// status/log pass). Prefer it at dispatch sites that have args.
func (m Mode) AllowedCall(tool string, args map[string]any) error {
	if m == Plan && IsMutatingCall(tool, args) {
		return &GateError{Tool: tool}
	}
	return nil
}
