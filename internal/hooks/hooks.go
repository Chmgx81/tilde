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
	"syscall"
	"tilde/internal/sandbox"
	"tilde/internal/scrub"
	"time"

	"gopkg.in/yaml.v3"
)

// Config maps tool names (or "*" for all) to shell commands.
type Config struct {
	// Root is the project boundary used to sandbox configured hooks. Empty is
	// retained for isolated library tests and means the legacy process runner.
	Root   string              `yaml:"-"`
	Before map[string][]string `yaml:"before"`
	After  map[string][]string `yaml:"after"`
	// SessionStart/SessionEnd are RESERVED for a future session
	// lifecycle: parsed, merged, and shown in the marketplace view, but
	// deliberately NOT executed — auto-running configured commands at
	// startup/exit would expand the auto-exec surface, so only the
	// before/after phases run today.
	SessionStart []string `yaml:"session_start"`
	SessionEnd   []string `yaml:"session_end"`
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
		Before:       mergeMap(project.Before, user.Before),
		After:        mergeMap(project.After, user.After),
		SessionStart: append(append([]string{}, project.SessionStart...), user.SessionStart...),
		SessionEnd:   append(append([]string{}, project.SessionEnd...), user.SessionEnd...),
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
	return c == nil || (len(c.Before) == 0 && len(c.After) == 0 && len(c.SessionStart) == 0 && len(c.SessionEnd) == 0)
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
		out, err := runHook(WithRoot(ctx, c.Root), tool, argsJSON, "", cmd)
		if err != nil {
			msg := scrubLocal(fmt.Sprintf("blocked by before-hook %q: %s", cmd, trunc(out, 500)))
			return fmt.Errorf("%s", msg)
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
		out, err := runHook(WithRoot(ctx, c.Root), tool, argsJSON, result, cmd)
		if err != nil {
			notes = append(notes, scrubLocal(fmt.Sprintf("[after-hook %q failed: %s]", cmd, trunc(out, 500))))
		} else if strings.TrimSpace(out) != "" {
			notes = append(notes, scrubLocal(fmt.Sprintf("[after-hook %q says: %s]", cmd, trunc(strings.TrimSpace(out), 500))))
		}
	}
	return strings.Join(notes, "\n")
}

func runHook(ctx context.Context, tool, argsJSON, stdin, cmdStr string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if hookRoot := hookRootFromContext(ctx); hookRoot != "" {
		// The command itself is still configured by the user, but its process
		// runs under the same filesystem/network boundary as shell tools.
		// Build the command and bail out before touching it: a failed
		// wrapper (missing bwrap, `/` as root, bad backend) returns a nil
		// *exec.Cmd, and assigning to its fields would panic.
		wrapped := "export TILDE_TOOL=" + shellQuote(tool) + " TILDE_ARGS_JSON=" + shellQuote(scrubLocal(argsJSON)) + "; " + cmdStr
		built, err := (&sandbox.Config{Root: hookRoot}).Command(cctx, wrapped)
		if err != nil {
			return "", err
		}
		cmd = built
	} else {
		cmd = exec.Command("bash", "-c", cmdStr)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Env = safeEnv(tool, argsJSON)
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return "", err
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	var runErr error
	select {
	case runErr = <-wait:
	case <-cctx.Done():
		if cmd.Process != nil {
			if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			} else {
				_ = cmd.Process.Kill()
			}
		}
		runErr = <-wait
		if runErr != nil {
			// The timer fired first: whatever the wait status is, the
			// hook overran its budget — say so explicitly instead of
			// surfacing a bare "signal: killed".
			runErr = fmt.Errorf("hook timed out after 30s: %v", runErr)
		}
		// A nil wait result here means the process exited cleanly in
		// the same instant the timer fired (the kill found nothing to
		// kill): report success, not a timeout.
	}
	s := capOutput(out.String())
	s = scrubLocal(s)
	s = strings.TrimSpace(s)
	return s, runErr
}

type hookRootKey struct{}

// WithRoot attaches the trusted project boundary for sandboxed hooks.
func WithRoot(ctx context.Context, root string) context.Context {
	return context.WithValue(ctx, hookRootKey{}, root)
}

func hookRootFromContext(ctx context.Context) string {
	root, _ := ctx.Value(hookRootKey{}).(string)
	return root
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// maxHookOutput caps combined stdout+stderr from a hook (P0-3 sandbox).
const maxHookOutput = 32 * 1024

// capOutput truncates combined hook output to maxHookOutput bytes,
// naming the fix so the model knows why output was cut.
func capOutput(s string) string {
	if len(s) <= maxHookOutput {
		return s
	}
	return s[:maxHookOutput] + fmt.Sprintf("… [truncated at %d bytes by hooks sandbox output cap — shorten the hook command output to see the rest]", maxHookOutput)
}

// safeEnv builds a minimal baseline env for hooks (P0-3 sandbox):
// only PATH, HOME, USER, SHELL, LANG, PWD plus TILDE_TOOL and
// TILDE_ARGS_JSON. Anything named *_KEY/*_TOKEN/*_SECRET/*_PASSWORD
// is never passed through.
func safeEnv(tool, argsJSON string) []string {
	keep := []string{"PATH", "HOME", "USER", "SHELL", "LANG", "PWD"}
	var env []string
	for _, k := range keep {
		if isSecretName(k) {
			continue
		}
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	argsJSON = scrubLocal(argsJSON)
	env = append(env,
		"TILDE_TOOL="+tool,
		"TILDE_ARGS_JSON="+argsJSON,
	)
	return env
}

// isSecretName reports whether an env name looks like a credential.
// Kept as a guard so future passthrough additions stay safe.
func isSecretName(k string) bool {
	up := strings.ToUpper(k)
	for _, suf := range []string{"_KEY", "_TOKEN", "_SECRET", "_PASSWORD"} {
		if strings.HasSuffix(up, suf) {
			return true
		}
	}
	return false
}

// scrubLocal redacts via the shared leaf internal/scrub (full pattern set).
// tools.Scrub still cannot be used here (import cycle via registry.go),
// but the leaf has no such cycle.
func scrubLocal(s string) string {
	clean, _ := scrub.Scrub(s)
	return clean
}

func trunc(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("… [truncated at %d chars — shorten the hook command output to see the rest]", max)
}
