// Command tilde — terminal-native coding agent, Phase 0.
// Interactive TUI by default; headless single-goal via --prompt.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"tilde/internal/agent"
	"tilde/internal/compact"
	"tilde/internal/creds"
	"tilde/internal/eval"
	"tilde/internal/hooks"
	"tilde/internal/mcp"
	"tilde/internal/mode"
	"tilde/internal/policy"
	"tilde/internal/provider"
	"tilde/internal/sandbox"
	"tilde/internal/session"
	"tilde/internal/skills"
	"tilde/internal/tools"
	"tilde/internal/tui"
	"tilde/internal/update"
)

func main() {
	prompt := flag.String("prompt", "", "Headless: run one goal non-interactively and exit")
	modeFlag := flag.String("mode", "plan", "Starting mode: plan|build|auto")
	modelFlag := flag.String("model", "", "Model name (default $TILDE_MODEL or qwen3.8-4b:16k)")
	yesFlag := flag.Bool("yes", false, "Auto-approve confirm-tier calls (deny-tier still blocks)")
	noSandbox := flag.Bool("no-sandbox", false, "Disable bwrap sandboxing (same as TILDE_NO_SANDBOX=1; not recommended)")
	budgetFlag := flag.Int("budget", 0, "Token budget before auto-compaction (default 32000, or $TILDE_BUDGET)")
	resumeFlag := flag.Bool("resume", false, "Pick a past session and resume it")
	skillFlag := flag.String("skill", "", "Preload a skill by name at startup")
	evalFlag := flag.Bool("eval", false, "Run the trajectory consistency suite and exit")
	evalTask := flag.String("eval-task", "", "Comma-separated task names (default: all)")
	trialsFlag := flag.Int("trials", 3, "Trials per eval task")
	outputFlag := flag.String("output", "text", "Headless output: text | json (one object per line)")
	mcpProject := flag.Bool("mcp-project", false, "Start project .tilde/mcp.json servers (same as TILDE_MCP_PROJECT=1; user servers always start)")
	hooksProject := flag.Bool("hooks-project", false, "Run project .tilde/hooks.yaml scripts (same as TILDE_HOOKS_PROJECT=1; user hooks always run)")
	providerFlag := flag.String("provider", "ollama", "Model provider: ollama | openai | anthropic | openrouter | gemini | opencode")
	apiBase := flag.String("api-base", "", "OpenAI-compatible base URL (default $OPENAI_BASE_URL or https://api.openai.com/v1)")
	apiKey := flag.String("api-key", "", "API key (default $OPENAI_API_KEY)")
	flag.Parse()

	// --prompt runs one headless goal; combining it with --resume/--eval
	// would silently drop the latter — fail loud instead (exit 2).
	if conflictingFlags(*prompt, *resumeFlag, *evalFlag) {
		fmt.Fprintln(os.Stderr, "tilde: --prompt cannot be combined with --resume or --eval — run one at a time")
		os.Exit(2)
	}

	// `tilde update` pulls, rebuilds, and reinstalls from the install
	// source. Dispatched before anything session-shaped: it needs no
	// provider, no log, and no TUI — and it must never create them.
	if flag.NArg() > 0 && flag.Arg(0) == "update" {
		if err := update.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "tilde: update:", err)
			os.Exit(1)
		}
		return
	}

	// Crash hint, not a prompt: a previous run that never wrote its
	// clean-shutdown marker may have left a session behind to resume.
	if unclosed := newestUnclosedSession(cleanMarkerTime()); unclosed != "" {
		fmt.Fprintf(os.Stderr, "tilde: unclosed session %s found — resume with `tilde --resume`\n", unclosed)
	}

	if *noSandbox {
		os.Setenv("TILDE_NO_SANDBOX", "1")
	}

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tilde: cannot determine working dir:", err)
		os.Exit(1)
	}

	m := mode.Plan
	switch strings.ToLower(*modeFlag) {
	case "plan":
		m = mode.Plan
	case "build":
		m = mode.Build
	case "auto":
		m = mode.Auto
	default:
		fmt.Fprintf(os.Stderr, "tilde: unknown --mode %q (use plan|build|auto)\n", *modeFlag)
		os.Exit(1)
	}

	h := buildHarness(root)
	polFile := loadPolicies(root)
	reg := h.reg
	reg.Hooks = loadHooks(root, *hooksProject || os.Getenv("TILDE_HOOKS_PROJECT") == "1")
	skIx, skErr := skills.Scan(root) // progressive disclosure index
	if skErr != nil {
		fmt.Fprintf(os.Stderr, "tilde: warning: %v\n", skErr)
	}
	reg.Register(&skills.LoadTool{Index: skIx})
	mcpMgr := startMCP(root, *mcpProject || os.Getenv("TILDE_MCP_PROJECT") == "1") // nil when unconfigured; lazy gateway
	if mcpMgr != nil {
		defer mcpMgr.Close()
		reg.Register(&mcp.ListTool{Mgr: mcpMgr})
		reg.Register(&mcp.CallTool{Mgr: mcpMgr})
	}

	// Sandbox is fail-closed pre-TUI: missing bwrap refuses to start.
	// The explicit opt-out (--no-sandbox / TILDE_NO_SANDBOX=1) keeps
	// working with its warning; anything else exits 2, never degraded.
	if !sandbox.Enforced() {
		if sandbox.Disabled() {
			fmt.Fprintf(os.Stderr, "tilde: warning: %s\n", sandbox.StatusLine())
		} else {
			fmt.Fprintf(os.Stderr, "tilde: %s — refusing to start (exit 2). Install bubblewrap or pass --no-sandbox to run unsandboxed (not recommended).\n", sandbox.StatusLine())
			os.Exit(2)
		}
	}

	// Provider/model misconfiguration fails fast pre-TUI (exit 2), from
	// construction alone — no dial. Keys resolve through the credential
	// ladder (§2.24): --api-key, then the store, then env. A cloud
	// provider with no key anywhere is a usage error before the TUI;
	// /login can also arm a key mid-session.
	over := map[string]string{}
	if id, _, ok := provider.ParseModelRef(*providerFlag); ok && *apiKey != "" {
		over[id] = *apiKey
	}
	// Credential store (§2.24): ~/.tilde/credentials.json. A home-dir
	// failure degrades to env-only resolution rather than blocking
	// startup — /login reports the write error if it is ever hit.
	var credStore *creds.Store
	if p, err := creds.DefaultPath(); err == nil {
		credStore = creds.New(p)
	}
	prov, keyErr := selectProvider(*providerFlag, *modelFlag, *apiBase, *apiKey, credStore)
	if keyErr != "" {
		fmt.Fprintln(os.Stderr, "tilde: "+keyErr)
		os.Exit(2)
	}

	budget := *budgetFlag
	if budget <= 0 {
		budget = 32000
		if env := os.Getenv("TILDE_BUDGET"); env != "" {
			var n int
			if _, err := fmt.Sscanf(env, "%d", &n); err == nil && n > 0 {
				budget = n
			}
		}
	}
	// The session log opens lazily, only on branches that run an agent
	// turn — flag-dispatch exits (--eval, --resume list, bad flags above)
	// must not create and leak empty session files.
	var sessLog *session.Log
	logOpen := false

	// --eval runs no interactive session and owns no session log: trial
	// loops log nothing. Dispatched before Open so it creates no file.
	if *evalFlag {
		runEval(root, prov, *evalTask, *trialsFlag)
		markCleanExit()
		return
	}

	// --resume without a screen lists sessions plainly and exits: no log.
	if *resumeFlag && !isTTY() {
		items, err := tui.ListSessions()
		if err != nil {
			fmt.Fprintln(os.Stderr, "tilde:", err)
			os.Exit(1)
		}
		for _, it := range items {
			fmt.Printf("%s\t%s\t%s\n", it.ID, it.Dir, it.First)
		}
		markCleanExit()
		return
	}

	sessLog, err = session.Open(session.NewID())
	if err != nil {
		fmt.Fprintln(os.Stderr, "tilde: session log:", err)
		os.Exit(1)
	}
	// Deferred close: resume may swap this log for the resumed file.
	// A caught panic appends one honest crash line first (guarded: only
	// when a log is actually open), so a resumed session shows the crash
	// instead of a silent gap. Close errors surface, never swallowed.
	logOpen = true
	defer func() {
		if r := recover(); r != nil {
			if logOpen && sessLog != nil {
				_ = sessLog.Append("system", map[string]any{"crash": fmt.Sprint(r)})
			}
			if logOpen && sessLog != nil {
				_ = sessLog.Close()
				logOpen = false
			}
			fmt.Fprintln(os.Stderr, "tilde: crashed:", r)
			os.Exit(1)
		}
		if logOpen {
			if cerr := sessLog.Close(); cerr != nil {
				fmt.Fprintln(os.Stderr, "tilde: session log close:", cerr)
			}
		}
	}()

	loop := &agent.Loop{
		Prov: prov,
		Reg:  reg,
		Log:  sessLog,
		Cfg: agent.Config{
			MaxIters: 25, DoomRepeats: 3, Root: root,
			Mode: m, Pol: &policy.Policy{AlwaysAllow: *yesFlag || m == mode.Auto, File: polFile},
			Compactor: &compact.Compactor{
				Budget:    budget,
				Summarize: compact.SummarizeWithProvider(prov),
			},
			Skills: skIx,
		},
	}
	if mcpMgr != nil {
		loop.Cfg.MCP = mcpMgr
	}

	// --skill preloads one skill body before anything else runs.
	if *skillFlag != "" {
		sk, ok := skIx.Get(*skillFlag)
		if !ok {
			fmt.Fprintf(os.Stderr, "tilde: unknown skill %q. Available: %s\n", *skillFlag, strings.Join(skIx.Names(), ", "))
			os.Exit(1)
		}
		loop.Msgs = append(loop.Msgs, provider.Message{Role: "system", Content: "[Skill preloaded: " + sk.Name + "]\n" + sk.Body})
		_ = sessLog.Append("system", map[string]any{"skill_preloaded": sk.Name})
	}

	// Mode gate at the registry level: Plan blocks mutating tools even if
	// the model calls them — a code-level gate, not a prompt instruction.
	// It reads loop.Cfg.Mode live so TUI Tab-cycling takes effect immediately.
	reg.Gate = func(toolName string, args map[string]any) (bool, string) {
		if err := loop.GetMode().AllowedCall(toolName, args); err != nil {
			return false, err.Error()
		}
		return true, ""
	}
	reg.Register(&agent.ExploreTool{NewChild: func(task string) *agent.Loop {
		return agent.NewExploreChild(loop, task)
	}})

	if *prompt != "" {
		_ = sessLog.Append("meta", map[string]any{"root": root, "model": prov.Name(), "budget": budget})
		runHeadless(loop, *prompt, *yesFlag, *outputFlag)
		markCleanExit()
		return
	}

	tm := tui.New(loop, mode.Plan, root, prov.Name(), budget)
	// Spec §2.1: interactive sessions always start in Plan, regardless of
	// the --mode flag (deliberate safety default). The flag still governs
	// headless and eval runs above.
	loop.SetMode(mode.Plan)
	if *resumeFlag {
		// Non-TTY list already returned before the log opened; reaching
		// here means a screen exists for the picker.
		// Fresh log for now; selecting a session swaps it for that file.
		_ = sessLog.Append("meta", map[string]any{"root": root, "model": prov.Name(), "budget": budget})
		tm.OpenResume()
	} else {
		_ = sessLog.Append("meta", map[string]any{"root": root, "model": prov.Name(), "budget": budget})
	}
	tm.BindCreds(credStore)
	tm.BindKeys(over)
	// Catalog budget auto-size (§2.24) applies only when the user never
	// set a budget: an explicit --budget or $TILDE_BUDGET is never
	// second-guessed by a model switch.
	tm.SetBudgetExplicit(*budgetFlag > 0 || os.Getenv("TILDE_BUDGET") != "")
	// Update notice (cached, free) + background refresh (one API read
	// per day, silent). Interactive path only — headless runs returned
	// above and never phone home.
	if note := update.Notice(); note != "" {
		tm.SetUpdateNotice(note)
	}
	go update.RefreshAsync()
	var prog *tea.Program
	tm.BindProgram(&prog)
	// Cell-motion mouse tracking is on: wheel motion (touchpad two-finger
	// scroll included) scrolls the transcript natively instead of reaching
	// the app as bare ↑/↓ through alternate-scroll, where it recalled
	// prompt history from the live tail. Motion is only reported while a
	// button is held and the composer consumes no mouse events, so text
	// selection stays the terminal's own (Shift+drag to copy) — drag and
	// release events are decoded but deliberately inert.
	// Belt-and-braces heal: the clears below run before bubbletea enables
	// its own tracking, and a tab poisoned by an older crashed build stays
	// tracked until told otherwise, so switch every mode off up front:
	// X10 (9), normal (1000), highlight (1001), cell/all motion
	// (1002/1003), focus reports (1004), UTF-8 (1005), SGR (1006), URXVT
	// (1015). bubbletea re-raises what it needs (1002/1006) at program
	// start. No-op on a clean terminal; skipped off-TTY so pipes never see
	// escape bytes.
	if term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Print("\x1b[?9l\x1b[?1000l\x1b[?1001l\x1b[?1002l\x1b[?1003l\x1b[?1004l\x1b[?1005l\x1b[?1006l\x1b[?1015l")
	}
	// Cell-motion tracking: wheel events (touchpad two-finger scroll
	// included) arrive as MouseMsg and scroll the transcript natively,
	// instead of surfacing as ↑/↓ through alternate-scroll mode.
	prog = tea.NewProgram(tm, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tilde:", err)
		os.Exit(1)
	}
	// If resume swapped the log, the TUI owns closing it now.
	if loop.Log != sessLog {
		logOpen = false
	}
	markCleanExit()
}

// harness is one fully-wired core tool registry and its shared stores.
// Skills and MCP ride on top (main only); eval uses the same core so the
// measured harness never drifts from the shipped one.
type harness struct {
	reg   *tools.Registry
	seen  *tools.SeenMap
	tasks *tools.TaskManager
	undo  *tools.UndoManager
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
					if k != "deny" && k != "ask" && k != "allow" {
						fmt.Fprintf(os.Stderr, "tilde: %s: invalid tier %q (expected deny/ask/allow) — refusing to start. Fix the policy file and try again.\n", path, k)
						os.Exit(2)
					}
				}
			}
		}
	}
	return f
}

func buildHarness(root string) harness {
	h := harness{
		reg:   tools.NewRegistry(),
		seen:  tools.NewSeenMap(root),
		tasks: &tools.TaskManager{},
		undo:  &tools.UndoManager{Root: root},
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

// runEval executes the consistency suite: same tasks, repeated trials,
// fresh dirs, trajectory scoring. Exit 1 if any task scores zero.
func runEval(root string, prov provider.Provider, filter string, trials int) {
	tasks := append(eval.DefaultTasks(), eval.ExtraTasks()...)
	tasks = append(tasks, eval.RepairTasks()...)
	tasks = append(tasks, eval.ComplexTasks()...)
	tasks = append(tasks, eval.InjectionTasks()...)
	if filter != "" {
		want := map[string]bool{}
		for _, n := range strings.Split(filter, ",") {
			want[strings.TrimSpace(n)] = true
		}
		var kept []eval.Task
		for _, t := range tasks {
			if want[t.Name] {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			fmt.Fprintf(os.Stderr, "tilde: no tasks match %q. Available:", filter)
			for _, t := range tasks {
				fmt.Fprintf(os.Stderr, " %s", t.Name)
			}
			fmt.Fprintln(os.Stderr)
			os.Exit(2)
		}
		tasks = kept
	}
	evalPol := loadPolicies(root)
	// Trial MCP servers are child processes: the eval Runner owns trial
	// sequencing (internal/eval is read-only here), so the factory cannot
	// return the manager for per-trial Close. Track them instead: each new
	// trial closes the previous trial's servers, and the tail is closed
	// after the run — mirroring production's defer mcpMgr.Close().
	var trialMu sync.Mutex
	var trialLive []*mcp.Manager
	closeTrialLive := func() {
		trialMu.Lock()
		defer trialMu.Unlock()
		for _, mm := range trialLive {
			mm.Close()
		}
		trialLive = nil
	}
	newLoop := func(dir string) *agent.Loop {
		closeTrialLive() // reap the previous trial before starting the next
		h := buildHarness(dir)
		reg := h.reg
		// Trial-hermetic extras: trial skills only (never the user's real
		// ones) and MCP only from the trial's own config.
		trialSkills, _ := skills.ScanDirs(dir, filepath.Join(dir, ".tilde-user-skills"))
		reg.Register(&skills.LoadTool{Index: trialSkills})
		var trialMCP *mcp.Manager
		if cfg, err := mcp.LoadFile(filepath.Join(dir, ".tilde", "mcp.json")); err == nil && len(cfg.Servers) > 0 {
			trialMCP = mcp.NewManager(cfg.Servers)
			if err := trialMCP.Start(context.Background()); err != nil {
				fmt.Fprintf(os.Stderr, "tilde: eval trial MCP warning: %v\n", err)
			}
			if len(trialMCP.Live()) > 0 {
				reg.Register(&mcp.ListTool{Mgr: trialMCP})
				reg.Register(&mcp.CallTool{Mgr: trialMCP})
			}
			trialMu.Lock()
			trialLive = append(trialLive, trialMCP)
			trialMu.Unlock()
		}
		loop := &agent.Loop{
			Prov: prov,
			Reg:  reg,
			Cfg: agent.Config{
				MaxIters: 25, DoomRepeats: 3, Root: dir,
				Mode: mode.Build, Pol: &policy.Policy{AlwaysAllow: true, File: evalPol},
				AskUser: func(string, map[string]any) bool { return true },
				Skills:  trialSkills,
			},
		}
		if trialMCP != nil && len(trialMCP.Live()) > 0 {
			loop.Cfg.MCP = trialMCP
		}
		reg.Gate = func(toolName string, args map[string]any) (bool, string) {
			if err := loop.GetMode().AllowedCall(toolName, args); err != nil {
				return false, err.Error()
			}
			return true, ""
		}
		reg.Register(&agent.ExploreTool{NewChild: func(task string) *agent.Loop {
			return agent.NewExploreChild(loop, task)
		}})
		return loop
	}
	r := &eval.Runner{Tasks: tasks, Trials: trials, NewLoop: newLoop}
	if r.Trials <= 0 {
		r.Trials = 3
	}
	fmt.Printf("tilde eval: %d task(s) × %d trial(s) — model %s\n", len(tasks), r.Trials, prov.Name())
	reports, err := r.Run(context.Background(), func(s string) { fmt.Println(s) })
	closeTrialLive() // tail trial's servers, after the run
	if err != nil {
		fmt.Fprintln(os.Stderr, "tilde: eval error:", err)
		os.Exit(1)
	}
	if r.Trials < 5 {
		fmt.Printf("tilde eval: note: --trials %d — rates move in 1/%d steps; use --trials 5+ for published numbers\n", r.Trials, r.Trials)
	}
	fmt.Printf("\n%-22s %6s %6s %8s %8s %8s %8s %8s %s\n", "TASK", "PASS", "OF", "MEDCALLS", "P90CALL", "MED_MS", "MED_IN", "MED_OUT", "NOTES")
	zeros := 0
	for _, rep := range reports {
		notes := strings.Join(rep.Reasons, " | ")
		if rep.ZeroTokens {
			notes = strings.TrimSpace(notes + " [tokens unreported]")
		}
		fmt.Printf("%-22s %6d %6d %8d %8d %8d %8d %8d %s\n", rep.Name, rep.Passes, rep.Trials, rep.MedianCall, rep.P90Call, rep.MedianMS, rep.MedianIn, rep.MedianOut, notes)
		if rep.Passes == 0 {
			zeros++
		}
	}
	saveEvalReport(reports)
	if zeros > 0 {
		os.Exit(1)
	}
}

// saveEvalReport archives the run for trend tracking.
func saveEvalReport(reports []eval.TaskReport) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".tilde", "evals")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, _ := json.MarshalIndent(map[string]any{
		"ts":      time.Now().UTC().Format(time.RFC3339),
		"reports": reports,
	}, "", "  ")
	path := filepath.Join(dir, fmt.Sprintf("eval-%d.json", time.Now().Unix()))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "tilde: warning: cannot save eval report: %v\n", err)
		return
	}
	fmt.Println("report saved:", path)
}

// runHeadless executes one goal without a TTY. Confirm-tier calls are
// approved inline via stdin unless --yes; deny-tier always blocks.
// Format "json" prints one JSON object per line (events, then a final
// result with usage) for CI pipelines; anything else prints human text.
func runHeadless(loop *agent.Loop, goal string, yes bool, format string) {
	asJSON := strings.ToLower(format) == "json"
	emitJSON := func(obj map[string]any) {
		b, _ := json.Marshal(obj)
		fmt.Println(string(b))
	}
	in := bufio.NewReader(os.Stdin)
	// Single long-lived stdin reader: one goroutine owns os.Stdin for
	// the whole run and queues lines; each approval consumes one line
	// with a timeout. A per-prompt reader goroutine would stay blocked
	// on stdin after a timeout and later steal input meant for the next
	// prompt (stale-reader consumption) — this shape cannot do that.
	lineCh := make(chan string)
	go func() {
		defer close(lineCh)
		for {
			line, err := in.ReadString('\n')
			lineCh <- line
			if err != nil {
				return
			}
		}
	}()
	loop.Cfg.AskUser = func(tool string, args map[string]any) bool {
		if yes {
			return true
		}
		if asJSON {
			emitJSON(map[string]any{"type": "approval", "tool": tool, "args": args})
		} else {
			fmt.Printf("approve %s? [y/n] ", policy.Describe(tool, args))
		}
		// Bounded stdin: a headless run with no one at the keyboard must
		// deny instead of hanging forever. /dev/null EOF reads return
		// immediately as empty (deny) — unchanged behavior. The closed
		// channel after EOF reads the same way (empty = deny).
		select {
		case line, ok := <-lineCh:
			if !ok {
				return false
			}
			line = strings.TrimSpace(strings.ToLower(line))
			return line == "y" || line == "yes"
		case <-time.After(60 * time.Second):
			fmt.Fprintln(os.Stderr, "tilde: approval timed out after 60s — denied")
			return false
		}
	}
	plain := headlessPlain() // degraded glyphs when piped or NO_COLOR (spec §4)
	denyHit := false         // deny-tier outcomes surface as tool_result text, not errors
	text, err := loop.Run(context.Background(), goal, func(e agent.Event) {
		if e.Kind == "tool_result" && strings.Contains(e.Text, "denied by policy") {
			denyHit = true
		}
		if asJSON {
			emitJSON(map[string]any{"type": "event", "kind": e.Kind, "text": e.Text, "pct": e.Pct})
			return
		}
		switch e.Kind {
		case "assistant":
			fmt.Println(e.Text)
		case "thinking":
			// Reasoning trace, printed whole: headless transparency
			// means CI logs show how a decision was reached, not just
			// what ran. (JSON mode already carries every event.)
			if plain {
				fmt.Printf("[thinking] %s\n", e.Text)
			} else {
				fmt.Printf("◇ %s\n", e.Text)
			}
		case "tool_call":
			if plain {
				fmt.Printf("[action] %s\n", e.Text)
			} else {
				fmt.Printf("● %s\n", e.Text)
			}
		case "tool_result":
			if plain {
				fmt.Printf("  %s\n", firstLine(e.Text))
			} else {
				fmt.Printf("  ⎿ %s\n", firstLine(e.Text))
			}
		case "handoff":
			fmt.Printf("ERROR (handoff to Plan): %s\n", e.Text)
		case "compacted":
			if !plain {
				fmt.Printf("● %s\n", e.Text) // session log keeps the full summary
			}
		case "usage":
			// Headless stays quiet; the log carries usage per turn.
			_ = e
		case "system":
			fmt.Printf("[system] %s\n", e.Text)
		case "done":
			if plain {
				fmt.Printf("[done] %s\n", e.Text)
			} else {
				fmt.Printf("✓ %s\n", e.Text)
			}
		}
	})
	if err != nil {
		code := headlessExitCode(err, text, false)
		if asJSON {
			emitJSON(map[string]any{"type": "result", "ok": false, "error": err.Error(),
				"tokens_in": loop.TotPrompt, "tokens_out": loop.TotCompletion})
		} else {
			fmt.Fprintln(os.Stderr, "tilde:", err)
		}
		os.Exit(code)
	}
	if asJSON {
		emitJSON(map[string]any{"type": "result", "ok": true, "text": text,
			"tokens_in": loop.TotPrompt, "tokens_out": loop.TotCompletion})
		if denyHit {
			os.Exit(5)
		}
		return
	}
	if text != "" {
		fmt.Println(text)
	}
	if denyHit {
		os.Exit(5)
	}
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// conflictingFlags reports --prompt combined with a mode it would
// silently swallow (--resume or --eval): fail loud at parse time.
func conflictingFlags(prompt string, resume, eval bool) bool {
	return prompt != "" && (resume || eval)
}

// headlessExitCode maps a loop outcome to the spec §4 table: 3 = provider
// error exhausted, 4 = doom-loop / iteration-cap handoff, 5 = deny-tier
// hit, 1 = uncategorized. Config/startup is 2, wired at the call sites.
func headlessExitCode(err error, text string, denyHit bool) int {
	if err == nil {
		if denyHit {
			return 5
		}
		return 0
	}
	msg := err.Error()
	if strings.Contains(text, "[HANDOFF TO PLAN") ||
		strings.Contains(msg, "reverting to Plan") ||
		strings.Contains(msg, "Iteration cap") {
		return 4
	}
	if strings.HasPrefix(msg, "ollama:") || strings.HasPrefix(msg, "openai:") ||
		strings.HasPrefix(msg, "anthropic:") ||
		strings.Contains(msg, "model error") {
		return 3
	}
	return 1
}

// cleanMarkerPath is the tiny shutdown receipt for crash detection: it is
// written only on normal exit, so a session log newer than it may belong
// to a run that died mid-turn.
func cleanMarkerPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".tilde", "sessions", ".clean")
}

// markCleanExit records a normal shutdown. Error exits (os.Exit paths,
// panics) skip it — that absence is exactly the unclosed signal.
func markCleanExit() {
	p := cleanMarkerPath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte(time.Now().UTC().Format(time.RFC3339)), 0o600)
}

// cleanMarkerTime reads the last clean shutdown, or zero when unknown
// (first run: no notice, nothing to compare against).
func cleanMarkerTime() time.Time {
	p := cleanMarkerPath()
	if p == "" {
		return time.Time{}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}
	}
	return ts
}

// newestUnclosedSession names the newest session log written after the
// last clean shutdown ("" when none). Best-effort launch hint only.
func newestUnclosedSession(clean time.Time) string {
	if clean.IsZero() {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	entries, err := os.ReadDir(filepath.Join(home, ".tilde", "sessions"))
	if err != nil {
		return ""
	}
	newest, newestMod := "", clean
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if st, serr := e.Info(); serr == nil && st.ModTime().After(newestMod) {
			newest, newestMod = strings.TrimSuffix(e.Name(), ".jsonl"), st.ModTime()
		}
	}
	return newest
}

// headlessPlain reports whether headless output must avoid TTY glyphs
// (spec §4): stdout is not a terminal, or NO_COLOR is set.
func headlessPlain() bool {
	if os.Getenv("NO_COLOR") != "" {
		return true
	}
	return !term.IsTerminal(int(os.Stdout.Fd()))
}

// isTTY reports whether we have a screen worth opening a picker on.
func isTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}
