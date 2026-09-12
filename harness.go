// Harness wiring — composition helpers for main (package main).
//
// This file owns the fully-wired core tool registry and the startup gates
// around it (audit sink, provider selection, policies, hooks, MCP). main.go
// owns flag parsing and dispatch; behavior is unchanged, only location moved
// for readability (main.go was 1811 lines).
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"tilde/internal/audit"
	"tilde/internal/creds"
	"tilde/internal/hooks"
	"tilde/internal/mcp"
	"tilde/internal/policy"
	"tilde/internal/provider"
	"tilde/internal/sandbox"
	"tilde/internal/tools"
)

// harness is one fully-wired core tool registry and its shared stores.
// Skills and MCP ride on top (main only); eval uses the same core so the
// measured harness never drifts from the shipped one.
type harness struct {
	reg    *tools.Registry
	seen   *tools.SeenMap
	tasks  *tools.TaskManager
	undo   *tools.UndoManager
	todos  *tools.TodoManager
	ask    *tools.Ask
	fetch  *tools.WebFetch
	search *tools.WebSearch
	shot   *tools.WebShot
}

// auditSink adapts *audit.AuditLog to the registry's AuditSink interface
// (defined in tools to avoid an import cycle: audit imports tools for
// Scrub, so tools cannot import audit back). Plain wrapper holding the
// log pointer — no type conversion.
type auditSink struct{ log *audit.AuditLog }

func (s *auditSink) AppendEvent(tool, decision, argsHash, detail string) {
	if s == nil || s.log == nil {
		return
	}
	_ = s.log.Append(audit.AuditEvent{
		Tool: tool, Decision: decision, RedactedArgsHash: argsHash, Detail: detail,
	})
}

// openAuditLog opens the global append-only audit trail at
// ~/.tilde/audit/audit.jsonl. Nil on any home-dir failure — the caller
// degrades to unaudited with a warning, never a startup refusal.
func openAuditLog() *audit.AuditLog {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tilde: warning: audit trail off (%v)\n", err)
		return nil
	}
	l, err := audit.Open(filepath.Join(home, ".tilde", "audit"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "tilde: warning: audit trail off (%v)\n", err)
		return nil
	}
	return l
}

// loadPolicies reads policies.yaml (missing = built-in defaults).
// selectProvider picks the model backend. Unknown names fail loud with
// the valid set — never a silent fallback to a different vendor.
// selectProvider builds the startup backend, resolving its key through
// the credential ladder (§2.24): --api-key flag, then the store, then
// env. It returns (provider, "") on success, (nil, usageError) on a
// cloud provider with no key anywhere — main exits 2 with the fix,
// never a raw 401 later. No dialing: construction only; a dead daemon
// surfaces as a provider error (exit 3) once the loop calls it.
func selectProvider(which, model, base, key string, store *creds.Store) (provider.Provider, string) {
	id, modelFromRef, ok := provider.ParseModelRef(which)
	if !ok {
		id = strings.ToLower(which)
		if id == "" {
			id = "ollama"
		}
	}
	if model == "" {
		model = modelFromRef
	}
	switch id {
	case "ollama":
		return provider.NewOllama(model), ""
	case "openai", "anthropic", "openrouter", "gemini", "opencode":
		resolved, _ := provider.Resolve(store, id, map[string]string{})
		// The explicit flag outranks everything (ladder step zero) —
		// Resolve saw the store, not the flag; apply it here.
		if key != "" {
			resolved = key
		}
		if resolved == "" {
			envName := "OPENAI_API_KEY"
			if id == "anthropic" {
				envName = "ANTHROPIC_API_KEY"
			}
			if id == "openrouter" {
				envName = "OPENROUTER_API_KEY"
			}
			if id == "gemini" {
				envName = "GEMINI_API_KEY"
			}
			if id == "opencode" {
				envName = "OPENCODE_API_KEY"
			}
			return nil, fmt.Sprintf("--provider %s needs an API key: run /login %s in the TUI, or set $%s, or pass --api-key", id, id, envName)
		}
		if id == "openai" {
			return provider.NewOpenAI(model, base, resolved), ""
		}
		if id == "openrouter" {
			return provider.NewOpenRouter(model, base, resolved), ""
		}
		if id == "gemini" {
			return provider.NewGemini(model, base, resolved), ""
		}
		if id == "opencode" {
			return provider.NewOpenCode(model, base, resolved), ""
		}
		return provider.NewAnthropic(model, base, resolved), ""
	default:
		return nil, fmt.Sprintf("unknown --provider %q (use ollama | openai | anthropic | openrouter | gemini | opencode, or provider/model)", which)
	}
}

func loadPolicies(root string) *policy.File {
	path := filepath.Join(root, "policies.yaml")
	f, err := policy.Load(path)
	if err != nil {
		// Fail closed, never warn-and-continue to defaults: a broken
		// policy file is not evidence its rules don't apply (exit 2,
		// pre-TUI — the yaml error already carries file and line).
		fmt.Fprintf(os.Stderr, "tilde: %s: %v — refusing to start. Fix the policy file and try again.\n", path, err)
		os.Exit(2)
	}
	if f != nil {
		// Strict tier check: an unknown top-level key (e.g. "mabye")
		// would otherwise parse silently into nothing, loosening the
		// file toward defaults without a word.
		data, rerr := os.ReadFile(path)
		if rerr == nil {
			var keys map[string]any
			if yerr := yaml.Unmarshal(data, &keys); yerr == nil {
				for k := range keys {
					if k != "deny" && k != "ask" && k != "allow" && k != "allow_net" && k != "deny_paths" {
						fmt.Fprintf(os.Stderr, "tilde: %s: invalid tier %q (expected deny/ask/allow/allow_net/deny_paths) — refusing to start. Fix the policy file and try again.\n", path, k)
						os.Exit(2)
					}
				}
			}
		}
	}
	return f
}

// checkPolicyTools refuses a policies.yaml naming tools that do not
// exist — a typo fails dangerous in both directions (a misspelled
// allow is dead weight; a misspelled deny is a missing guard), so the
// file is rejected instead of partially honored. Nil = defaults.
func checkPolicyTools(root string, f *policy.File, known []string) {
	if f == nil {
		return
	}
	if unknown := f.UnknownTools(known); len(unknown) > 0 {
		fmt.Fprintf(os.Stderr, "tilde: %s: unknown tool %q (expected one of: %s) — refusing to start. Fix the policy file and try again.\n",
			filepath.Join(root, "policies.yaml"), strings.Join(unknown, ", "), strings.Join(known, ", "))
		os.Exit(2)
	}
}

func buildHarness(root string) harness {
	h := harness{
		reg:   tools.NewRegistry(),
		seen:  tools.NewSeenMap(root),
		tasks: &tools.TaskManager{},
		undo:  &tools.UndoManager{Root: root},
		todos: &tools.TodoManager{},
		ask:   &tools.Ask{},
		fetch: &tools.WebFetch{AllowNet: func() bool { return os.Getenv("TILDE_ALLOW_NET") == "1" }},
		search: &tools.WebSearch{AllowNet: func() bool {
			return os.Getenv("TILDE_ALLOW_NET") == "1"
		}},
		shot: &tools.WebShot{AllowNet: func() bool {
			return os.Getenv("TILDE_ALLOW_NET") == "1"
		}},
	}
	h.reg.Undo = h.undo
	h.seen.Tasks = h.tasks // stale checks see in-flight background work
	h.reg.Register(&tools.ReadFile{Root: root, Seen: h.seen})
	h.reg.Register(&tools.WriteFile{Root: root, Seen: h.seen})
	h.reg.Register(&tools.EditFile{Root: root, Seen: h.seen})
	h.reg.Register(&tools.Shell{Root: root, Sandbox: &sandbox.Config{Root: root}, Tasks: h.tasks})
	h.reg.Register(&tools.ShellPoll{Tasks: h.tasks})
	h.reg.Register(&tools.Grep{Root: root, Seen: h.seen})
	h.reg.Register(&tools.Glob{Root: root})
	h.reg.Register(&tools.GitStatus{Root: root})
	h.reg.Register(&tools.GitDiff{Root: root})
	h.reg.Register(&tools.GitWorktreeList{Root: root})
	h.reg.Register(&tools.GitWorktreeAdd{Root: root})
	h.reg.Register(&tools.GitWorktreeRemove{Root: root})
	h.reg.Register(&tools.TodoWrite{Mgr: h.todos})
	h.reg.Register(h.ask)
	h.reg.Register(h.fetch)
	h.reg.Register(h.search)
	h.reg.Register(h.shot)
	h.reg.Register(&tools.SymbolSearch{Root: root, Seen: h.seen})
	h.reg.Register(&tools.Memory{Root: root})
	h.reg.Register(&tools.Diagnose{Root: root, Seen: h.seen})
	h.reg.Register(&tools.Remember{Root: root, Seen: h.seen})
	h.reg.Register(&tools.SavePlan{Root: root})
	return h
}

// loadHooks merges user + project hook configs (missing = none).
// Project hooks (arrive with cloned repos, run as unsandboxed shell)
// need explicit opt-in — same trust rule as project MCP servers.
func loadHooks(root string, allowProject bool) *hooks.Config {
	home, _ := os.UserHomeDir()
	var proj hooks.Config
	if allowProject {
		var err error
		proj, err = hooks.LoadFile(filepath.Join(root, ".tilde", "hooks.yaml"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "tilde: warning: %v\n", err)
		}
	} else {
		if _, err := os.Stat(filepath.Join(root, ".tilde", "hooks.yaml")); err == nil {
			fmt.Fprintf(os.Stderr, "tilde: project hooks present but NOT loaded (untrusted source) — pass --hooks-project to opt in\n")
		}
	}
	user, err := hooks.LoadFile(filepath.Join(home, ".tilde", "hooks.yaml"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "tilde: warning: %v\n", err)
	}
	merged := hooks.Merge(proj, user)
	if merged.Empty() {
		return nil
	}
	merged.Root = root
	return &merged
}

// startMCP merges user + project MCP configs and starts live servers.
// Unconfigured → nil (no tools, no prompt cost). Partial failures warn
// and keep the servers that did start.
//
// Trust boundary: USER servers (~/.tilde/mcp.json, written by you)
// auto-start. PROJECT servers (.tilde/mcp.json, arrives with cloned repos)
// start only with explicit opt-in — a cloned repo must never execute code
// on launch day without your say-so.
func startMCP(root string, allowProject bool) *mcp.Manager {
	home, _ := os.UserHomeDir()
	userCfg, err := mcp.LoadFile(filepath.Join(home, ".tilde", "mcp.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "tilde: warning: %v\n", err)
	}
	projCfg, err := mcp.LoadFile(filepath.Join(root, ".tilde", "mcp.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "tilde: warning: %v\n", err)
	}
	if len(projCfg.Servers) > 0 && !allowProject {
		fmt.Fprintf(os.Stderr, "tilde: %d project MCP server(s) available but NOT started (untrusted source) — pass --mcp-project to opt in\n", len(projCfg.Servers))
		projCfg = mcp.FileConfig{}
	}
	merged := mcp.Merge(userCfg, projCfg)
	if len(merged) == 0 {
		return nil
	}
	mgr := mcp.NewManager(merged)
	if err := mgr.Start(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "tilde: warning: %v\n", err)
	}
	if len(mgr.Live()) == 0 {
		return nil
	}
	fmt.Fprintf(os.Stderr, "tilde: MCP live: %s\n", strings.Join(mgr.Live(), ", "))
	return mgr
}
