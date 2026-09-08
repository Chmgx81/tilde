// Package hooks — project/user pre/post tool scripts (v1).
//
// Hooks let a project enforce house rules without forking the harness:
// run a linter after every edit, block writes outside an allow-list,
// log tool calls. Config lives in .tilde/hooks.yaml (project) and
// ~/.tilde/hooks.yaml (user); both run, project first.
//
//	before:
//	  write_file: ["./scripts/guard.sh"]
//	  "*": ["./scripts/audit.sh"]
//	after:
//	  write_file: ["gofmt -l ."]
//
// A before-hook exiting non-zero BLOCKS the call (stderr becomes the
// reason). An after-hook failure never fails the call — its output
// appends as a warning the model can act on.
//
// Trust note: hooks are arbitrary local commands from config files —
// same trust class as MCP servers. Only install hooks you trust.
package hooks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config maps tool names (or "*" for all) to shell commands.
type Config struct {
	Before map[string][]string `yaml:"before"`
	After  map[string][]string `yaml:"after"`
}

// LoadFile reads one hooks file. Missing = empty, not an error.
func LoadFile(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, fmt.Errorf("hooks: cannot read %s: %v", path, err)
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("hooks: bad YAML in %s: %v — expected before:/after: maps of tool → command list", path, err)
	}
	return c, nil
}

// Merge concatenates project then user hooks (both run, in that order).
func Merge(project, user Config) Config {
	return Config{
		Before: mergeMap(project.Before, user.Before),
		After:  mergeMap(project.After, user.After),
	}
}

func mergeMap(a, b map[string][]string) map[string][]string {
	out := map[string][]string{}
	for k, v := range a {
		out[k] = append(out[k], v...)
	}
	for k, v := range b {
		out[k] = append(out[k], v...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Empty reports whether no hooks are configured.
func (c *Config) Empty() bool {
	return c == nil || (len(c.Before) == 0 && len(c.After) == 0)
}

func (c *Config) forTool(m map[string][]string, tool string) []string {
	if c == nil {
		return nil
	}
	var out []string
	out = append(out, m[tool]...)
	out = append(out, m["*"]...)
	return out
}

// Before runs pre-hooks. A non-zero exit blocks the call; the command's
// output becomes the reason shown to the model.
func (c *Config) RunBefore(ctx context.Context, tool, argsJSON string) error {
	if c == nil {
		return nil
	}
	for _, cmd := range c.forTool(c.Before, tool) {
		out, err := runHook(ctx, tool, argsJSON, "", cmd)
		if err != nil {
			return fmt.Errorf("blocked by before-hook %q: %s", cmd, trunc(out, 500))
		}
	}
	return nil
}

// After runs post-hooks, returning warning lines ("" when clean).
// Failures never fail the call — they append for the model to act on.
func (c *Config) RunAfter(ctx context.Context, tool, argsJSON, result string) string {
	if c == nil {
		return ""
	}
	var notes []string
	for _, cmd := range c.forTool(c.After, tool) {
		out, err := runHook(ctx, tool, argsJSON, result, cmd)
		if err != nil {
			notes = append(notes, fmt.Sprintf("[after-hook %q failed: %s]", cmd, trunc(out, 500)))
		} else if strings.TrimSpace(out) != "" {
			notes = append(notes, fmt.Sprintf("[after-hook %q says: %s]", cmd, trunc(strings.TrimSpace(out), 500)))
		}
	}
	return strings.Join(notes, "\n")
}

func runHook(ctx context.Context, tool, argsJSON, stdin, cmdStr string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "bash", "-c", cmdStr)
	cmd.Env = append(os.Environ(),
		"TILDE_TOOL="+tool,
		"TILDE_ARGS_JSON="+argsJSON,
	)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(out.String()), err
	}
	return strings.TrimSpace(out.String()), nil
}

func trunc(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("… [truncated at %d chars — shorten the hook command output to see the rest]", max)
}
