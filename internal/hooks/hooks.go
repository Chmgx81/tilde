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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config maps tool names (or "*" for all) to shell commands.
type Config struct {
	Before map[string][]string `yaml:"before"`
	After  map[string][]string `yaml:"after"`
	// SessionStart runs once at session startup; SessionEnd runs at exit.
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
		out, err := runHook(ctx, tool, argsJSON, "", cmd)
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
		out, err := runHook(ctx, tool, argsJSON, result, cmd)
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
	cmd := exec.CommandContext(cctx, "bash", "-c", cmdStr)
	cmd.Env = safeEnv(tool, argsJSON)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	s := capOutput(out.String())
	s = scrubLocal(s)
	s = strings.TrimSpace(s)
	return s, runErr
}

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

// TrustHash returns the sha256 hex of a file's bytes, for
// workspace+path trust pinning (P0-3; wiring into main is P1).
func TrustHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("hooks: cannot hash %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// scrubLocal redacts key-like tokens from hook output for audit lines.
// NOTE: tools.Scrub cannot be used here — internal/tools imports
// internal/hooks (registry.go), so that would be an import cycle.
// This is a minimal local subset; keep in sync conceptually.
var (
	reHookPEM       = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	reHookAnthropic = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)
	reHookOpenAI    = regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`)
	reHookXAI       = regexp.MustCompile(`xai-[A-Za-z0-9_-]{20,}`)
	reHookAWS       = regexp.MustCompile(`(?:AKIA|ASIA)[0-9A-Z]{16}`)
	reHookGitHub    = regexp.MustCompile(`(?:gh[pousr]_|github_pat_)[A-Za-z0-9_]{20,}`)
	reHookGitLab    = regexp.MustCompile(`glpat-[A-Za-z0-9_-]{20,}`)
	reHookSlack     = regexp.MustCompile(`(?:xox[abps]|xapp)-[A-Za-z0-9-]+`)
	reHookBearer    = regexp.MustCompile(`Bearer\s+[A-Za-z0-9_.~+/=-]+`)
	reHookJWT       = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
)

func scrubLocal(s string) string {
	s = reHookPEM.ReplaceAllString(s, "<<REDACTED:pem>>")
	s = reHookAnthropic.ReplaceAllString(s, "<<REDACTED:anthropic>>")
	s = reHookOpenAI.ReplaceAllString(s, "<<REDACTED:openai>>")
	s = reHookXAI.ReplaceAllString(s, "<<REDACTED:xai>>")
	s = reHookAWS.ReplaceAllString(s, "<<REDACTED:aws>>")
	s = reHookGitHub.ReplaceAllString(s, "<<REDACTED:github>>")
	s = reHookGitLab.ReplaceAllString(s, "<<REDACTED:gitlab>>")
	s = reHookSlack.ReplaceAllString(s, "<<REDACTED:slack>>")
	s = reHookBearer.ReplaceAllString(s, "Bearer <<REDACTED:bearer>>")
	s = reHookJWT.ReplaceAllString(s, "<<REDACTED:jwt>>")
	return s
}

func trunc(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("… [truncated at %d chars — shorten the hook command output to see the rest]", max)
}
