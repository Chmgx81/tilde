// Command tilde — terminal-native coding agent, Phase 0.
// Interactive TUI by default; headless single-goal via --prompt.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"tilde/internal/agent"
	"tilde/internal/audit"
	"tilde/internal/compact"
	"tilde/internal/creds"
	"tilde/internal/eval"
	"tilde/internal/export"
	"tilde/internal/ide"
	"tilde/internal/mcp"
	"tilde/internal/mode"
	"tilde/internal/plugin"
	"tilde/internal/policy"
	"tilde/internal/provider"
	"tilde/internal/sandbox"
	"tilde/internal/schedule"
	"tilde/internal/session"
	"tilde/internal/skills"
	"tilde/internal/spill"
	"tilde/internal/trust"
	"tilde/internal/tui"
	"tilde/internal/update"
)

func main() {
	prompt := flag.String("prompt", "", "Headless: run one goal non-interactively and exit")
	modeFlag := flag.String("mode", "plan", "Starting mode: plan|build|auto")
	modelFlag := flag.String("model", "", "Model name (default $TILDE_MODEL or qwen3.8-4b:16k)")
	yesFlag := flag.Bool("yes", false, "Headless: auto-approve read-only confirm-tier calls only; mutating/network confirm-tier calls still need a 'y' on stdin (deny-tier always blocks)")
	noSandbox := flag.Bool("no-sandbox", false, "Disable bwrap sandboxing (same as TILDE_NO_SANDBOX=1; not recommended)")
	budgetFlag := flag.Int("budget", 0, "Token budget before auto-compaction (default 32000, or $TILDE_BUDGET)")
	resumeFlag := flag.Bool("resume", false, "Pick a past session and resume it")
	exportFlag := flag.String("export", "", "Export a session brief to markdown and exit")
	exportOut := flag.String("out", "", "Output path for --export (default <id>-brief.md in cwd)")
	skillFlag := flag.String("skill", "", "Preload a skill by name at startup")
	evalFlag := flag.Bool("eval", false, "Run the trajectory consistency suite and exit")
	evalTask := flag.String("eval-task", "", "Comma-separated task names (default: all)")
	trialsFlag := flag.Int("trials", 3, "Trials per eval task")
	outputFlag := flag.String("output", "text", "Headless output: text | json (one object per line)")
	mcpProject := flag.Bool("mcp-project", false, "Start project .tilde/mcp.json servers (same as TILDE_MCP_PROJECT=1; user servers always start)")
	hooksProject := flag.Bool("hooks-project", false, "Run project .tilde/hooks.yaml scripts (same as TILDE_HOOKS_PROJECT=1; user hooks always run)")
	skillsProject := flag.Bool("skills-project", false, "Load project .tilde/skills (same as TILDE_SKILLS_PROJECT=1; user skills always load)")
	providerFlag := flag.String("provider", "ollama", "Model provider: ollama | openai | anthropic | openrouter | gemini | opencode")
	apiBase := flag.String("api-base", "", "OpenAI-compatible base URL (default $OPENAI_BASE_URL or https://api.openai.com/v1)")
	apiKey := flag.String("api-key", "", "API key (default $OPENAI_API_KEY)")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `tilde — security-first terminal coding agent

Usage:
  tilde [flags]                    start the interactive TUI
  tilde --prompt "goal" [flags]    run one headless goal and exit
  tilde <command> [args]           management commands (each has its own --help)

Commands:
  plugin  install|upgrade|enable|disable|remove|rollback|verify|list
  login   [provider|service]       store a credential in the sealed store
  logout  [provider|service]       remove a stored credential
  doctor  [--json]                 report install health (sandbox, policy, keys, git)
  trust|untrust <dir>              record or clear project trust
  ide-bridge                       stdio JSON bridge for IDE hosts (chat approvals deny)
  audit   [--since T] [--tool N] [--decision D] [--json]
                                   read the append-only audit log
  run-due [--yes]                  run due scheduled jobs now
  schedule [--json]                list scheduled jobs with due state + next run
  prune   [--sessions D|--audit D|--spill D] [--yes]
                                   delete old sessions / audit log / spilled output (dry run by default)
  fork <id> [--at RFC3339]         branch a session at a message
  models                           list the provider model catalog
  update                           pull, rebuild, and reinstall tilde

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	if *versionFlag {
		fmt.Println(update.BuildVersion())
		return
	}

	// --prompt runs one headless goal; combining it with --resume/--eval
	// would silently drop the latter — fail loud instead (exit 2).
	if conflictingFlags(*prompt, *resumeFlag, *evalFlag) {
		fmt.Fprintln(os.Stderr, "tilde: --prompt cannot be combined with --resume or --eval — run one at a time")
		os.Exit(2)
	}

	// --export runs one file write and exits; combining it with a
	// session-shaped flag would silently drop the latter — fail loud
	// instead (exit 2). --out without --export is likewise a usage
	// error, never a silent no-op.
	if *exportFlag != "" && (*prompt != "" || *resumeFlag || *evalFlag) {
		fmt.Fprintln(os.Stderr, "tilde: --export cannot be combined with --prompt, --resume or --eval — run one at a time")
		os.Exit(2)
	}
	if *exportOut != "" && *exportFlag == "" {
		fmt.Fprintln(os.Stderr, "tilde: --out needs --export <session-id> — nothing to write")
		os.Exit(2)
	}

	// `tilde --export <session-id> [--out path.md]` writes a portable
	// markdown brief (spec §2.22 + §4 row). Dispatched before anything
	// session-shaped: it needs no provider, no log, and no TUI — and
	// it must never create them.
	if *exportFlag != "" {
		if err := runExportCmd(*exportFlag, *exportOut); err != nil {
			fmt.Fprintln(os.Stderr, "tilde:", err)
			os.Exit(1)
		}
		markCleanExit()
		return
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

	// `tilde trust [dir]` / `tilde untrust [dir]` records project-folder
	// trust (Pi resolveProjectTrusted pattern). Headless never writes
	// trust without this explicit action; a miss always reads as denied.
	if flag.NArg() > 0 && (flag.Arg(0) == "trust" || flag.Arg(0) == "untrust") {
		if err := runTrustCmd(flag.Arg(0), flag.Args()); err != nil {
			fmt.Fprintln(os.Stderr, "tilde:", err)
			os.Exit(1)
		}
		return
	}

	// `tilde login [provider]` / `tilde logout [provider]` manage the
	// sealed credential store headlessly (the TUI's /login is the
	// interactive equivalent). Dispatched before anything session-shaped:
	// no provider, no log, no TUI needed.
	if flag.NArg() > 0 && (flag.Arg(0) == "login" || flag.Arg(0) == "logout") {
		if err := runAuthCmd(flag.Arg(0), flag.Args()); err != nil {
			fmt.Fprintln(os.Stderr, "tilde:", err)
			os.Exit(1)
		}
		markCleanExit()
		return
	}

	// `tilde doctor [--json]` reports install health (sandbox, policy,
	// credentials, writability, git, provider) without starting a session.
	// Exits 2 on a hard failure, so it is safe to gate a script on it.
	if flag.NArg() > 0 && flag.Arg(0) == "doctor" {
		fails, derr := runDoctorCmd(flag.Args(), doctorProviderConfig{
			Provider: *providerFlag, Model: *modelFlag, Base: *apiBase, Key: *apiKey,
		})
		if derr != nil {
			fmt.Fprintln(os.Stderr, "tilde:", derr)
			os.Exit(1)
		}
		if fails > 0 {
			os.Exit(2)
		}
		markCleanExit()
		return
	}

	// `tilde audit [--since ...] [--tool ...] [--decision ...] [--json]`
	// reads the governance trail. `tilde run-due [--yes]` executes due
	// schedule entries by re-invoking this binary headlessly per job.
	// `tilde schedule [--json]` lists schedule entries with due state.
	// `tilde plugin install <dir> [--upgrade] | upgrade <dir> |
	// enable|disable|remove|rollback <name> | verify <name> | list`
	// manages hash-pinned local plugins. All four dispatch before any
	// session-shaped setup: they need no provider, no log, no TUI.
	if flag.NArg() > 0 && flag.Arg(0) == "audit" {
		if err := runAuditCmd(flag.Args()); err != nil {
			fmt.Fprintln(os.Stderr, "tilde:", err)
			os.Exit(1)
		}
		return
	}
	if flag.NArg() > 0 && flag.Arg(0) == "run-due" {
		if err := runDueCmd(flag.Args()); err != nil {
			fmt.Fprintln(os.Stderr, "tilde:", err)
			os.Exit(1)
		}
		return
	}
	if flag.NArg() > 0 && flag.Arg(0) == "schedule" {
		if err := runScheduleCmd(flag.Args()); err != nil {
			fmt.Fprintln(os.Stderr, "tilde:", err)
			os.Exit(1)
		}
		return
	}
	if flag.NArg() > 0 && flag.Arg(0) == "plugin" {
		if err := runPluginCmd(flag.Args()); err != nil {
			fmt.Fprintln(os.Stderr, "tilde:", err)
			os.Exit(1)
		}
		return
	}
	// `tilde models [provider]` prints the model catalog (windows +
	// prices) with no network and no session. Unknown providers fail
	// loud (exit 2), never a silent fallback.
	if flag.NArg() > 0 && flag.Arg(0) == "models" {
		arg := ""
		if len(flag.Args()) > 1 {
			arg = flag.Args()[1]
		}
		out, err := provider.FormatCatalog(arg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tilde: "+err.Error())
			os.Exit(2)
		}
		fmt.Println(out)
		markCleanExit()
		return
	}

	// `tilde prune --sessions 30d --audit 90d --yes` enforces retention
	// windows on session logs and the audit trail. Without --yes it
	// prints the plan (dry run) and changes nothing. Destructive and
	// explicit: no other flag combination deletes.
	if flag.NArg() > 0 && flag.Arg(0) == "prune" {
		if err := runPruneCmd(flag.Args()); err != nil {
			fmt.Fprintln(os.Stderr, "tilde:", err)
			os.Exit(1)
		}
		return
	}

	// `tilde fork <id> [--at RFC3339]` branches a session at an earlier
	// point (or tip): the copy is byte-identical through the cutoff plus
	// one fork marker line, and the source is never modified. Prints the
	// new session id.
	if flag.NArg() > 0 && flag.Arg(0) == "fork" {
		if err := runForkCmd(flag.Args()); err != nil {
			fmt.Fprintln(os.Stderr, "tilde:", err)
			os.Exit(1)
		}
		return
	}

	// Crash hint, not a prompt: a previous run that never wrote its
	// clean-shutdown marker may have left a session behind to resume.
	unclosed := newestUnclosedSession(cleanMarkerTime())
	if unclosed != "" {
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
		os.Exit(2)
	}

	h := buildHarness(root)
	polFile := loadPolicies(root)
	// P1-G per-host net approval: the allow_net hostname list is checked
	// in webfetch via HostAllow; the session-wide TILDE_ALLOW_NET=1
	// opt-in in AllowNet above is untouched.
	h.fetch.HostAllow = func(host string) bool { return polFile.NetAllowed(host) }
	h.search.HostAllow = func(host string) bool { return polFile.NetAllowed(host) }
	h.shot.HostAllow = func(host string) bool { return polFile.NetAllowed(host) }
	// Audit trail: append-only ~/.tilde/audit/audit.jsonl, distinct from
	// the session transcript. Nil-safe by construction — a home-dir
	// failure degrades to unaudited rather than blocking startup.
	if auditLog := openAuditLog(); auditLog != nil {
		defer auditLog.Close()
		h.reg.Audit = &auditSink{log: auditLog}
	}
	reg := h.reg
	reg.Hooks = loadHooks(root, *hooksProject || os.Getenv("TILDE_HOOKS_PROJECT") == "1")
	allowSkills := *skillsProject || os.Getenv("TILDE_SKILLS_PROJECT") == "1"
	if !allowSkills && trust.IsTrusted(root) {
		allowSkills = true
	}
	skIx, skErr := skills.Scan(root, allowSkills) // progressive disclosure index
	if skErr != nil {
		fmt.Fprintf(os.Stderr, "tilde: warning: %v\n", skErr)
	}
	// Bundled skills are immutable, instruction-only capabilities shipped in
	// the binary. They are always available; user/project skills remain behind
	// their existing trust gates and cannot silently replace a bundled skill.
	if builtins, err := skills.Bundled(); err != nil {
		fmt.Fprintf(os.Stderr, "tilde: warning: bundled skills unavailable: %v\n", err)
	} else {
		for _, sk := range builtins {
			skIx.AddBuiltin(sk)
		}
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
	budgetExplicit := *budgetFlag > 0 || os.Getenv("TILDE_BUDGET") != ""
	if budget <= 0 {
		budget = 32000
		if env := os.Getenv("TILDE_BUDGET"); env != "" {
			var n int
			if _, err := fmt.Sscanf(env, "%d", &n); err == nil && n > 0 {
				budget = n
			}
		}
		if !budgetExplicit {
			if pid, mname, ok := provider.ParseModelRef(prov.Name()); ok {
				if b := provider.BudgetFor(pid, mname); b > 0 {
					budget = b
				}
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
			Mode: m, Pol: &policy.Policy{AlwaysAllow: *yesFlag || m == mode.Auto, Unattended: *yesFlag, File: polFile, Root: root},
			Compactor: &compact.Compactor{
				Budget:    budget,
				Summarize: compact.SummarizeWithProvider(prov),
			},
			Skills: skIx,
			// Project rules ride the project-skills trust gate:
			// explicit opt-in or a recorded `tilde trust`.
			RulesOK: allowSkills,
		},
	}
	if mcpMgr != nil {
		loop.Cfg.MCP = mcpMgr
	}
	h.ask.AskUser = func(tool string, args map[string]any) bool {
		if loop.Cfg.AskUser == nil {
			return false
		}
		return loop.Cfg.AskUser(tool, args)
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
	// Single-writer subagents: spawn/apply/discard share one WorkState so
	// a second spawn refuses while a session is pending. Wired beside
	// ExploreTool so checkPolicyTools below sees the full registry.
	workState := &agent.WorkState{Root: root}
	workState.NewChild = func(task, workRoot string) *agent.Loop {
		return agent.NewWorkChild(loop, task, workRoot)
	}
	reg.Register(&agent.SpawnWorkTool{State: workState})
	reg.Register(&agent.ApplyWorkTool{State: workState})
	reg.Register(&agent.DiscardWorkTool{State: workState})
	// Unknown tool names in policies.yaml refuse here, after every
	// tool (core + skills + MCP + subagents) is registered — earlier
	// would false-positive on tools registered below. MCP names are
	// always known: those tools register only when servers exist, but
	// policies may name them regardless.
	checkPolicyTools(root, polFile, append(reg.Names(), "mcp_list", "mcp_call"))

	// `tilde ide-bridge` serves line-delimited JSON over stdio for IDE
	// hosts (initialize/health/session.create/session.chat/history).
	// Stdin is the protocol stream, so approvals can never ask there:
	// AskUser denies by default (fail closed). --mode governs autonomy
	// (Plan default); --yes auto-approves ask-tier like headless.
	if flag.NArg() > 0 && flag.Arg(0) == "ide-bridge" {
		_ = sessLog.Append("meta", map[string]any{"root": root, "model": prov.Name(), "budget": budget})
		loop.Cfg.AskUser = func(tool string, args map[string]any) bool {
			return *yesFlag && policy.UnattendedAllowed(tool, args)
		}
		br := ide.New(reg.Names(), func(ctx context.Context, prompt string) (string, error) {
			return loop.Run(ctx, prompt, func(agent.Event) {})
		})
		br.Serve(os.Stdin, os.Stdout)
		markCleanExit()
		return
	}

	// Any leftover positional is a typo'd subcommand — the headless goal is
	// --prompt, never a bare argument. Refuse loudly (exit 2) instead of
	// silently falling through to the interactive TUI.
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "tilde: unknown command %q — run `tilde --help` for the command list (headless goals use --prompt).\n", flag.Arg(0))
		os.Exit(2)
	}

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
	if *resumeFlag || offerResume(unclosed, root) {
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

// harness wiring lives in harness.go (same package) — see buildHarness.

// auditSink + openAuditLog live in harness.go.

// selectProvider lives in harness.go.

// loadPolicies + checkPolicyTools live in harness.go.

// buildHarness + loadHooks + startMCP live in harness.go.

// runEval executes the consistency suite: same tasks, repeated trials,
// fresh dirs, trajectory scoring. Exit 1 if any task scores zero.
func runEval(root string, prov provider.Provider, filter string, trials int) {
	tasks := append(eval.DefaultTasks(), eval.ExtraTasks()...)
	tasks = append(tasks, eval.RepairTasks()...)
	tasks = append(tasks, eval.ComplexTasks()...)
	tasks = append(tasks, eval.InjectionTasks()...)
	tasks = append(tasks, eval.P3Tasks()...)
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
		h.fetch.HostAllow = func(host string) bool { return evalPol.NetAllowed(host) }
		h.search.HostAllow = func(host string) bool { return evalPol.NetAllowed(host) }
		h.shot.HostAllow = func(host string) bool { return evalPol.NetAllowed(host) }
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
				Mode: mode.Build, Pol: &policy.Policy{AlwaysAllow: true, File: evalPol, Root: dir},
				AskUser: func(string, map[string]any) bool { return true },
				Skills:  trialSkills,
			},
		}
		if trialMCP != nil && len(trialMCP.Live()) > 0 {
			loop.Cfg.MCP = trialMCP
		}
		h.ask.AskUser = loop.Cfg.AskUser
		reg.Gate = func(toolName string, args map[string]any) (bool, string) {
			if err := loop.GetMode().AllowedCall(toolName, args); err != nil {
				return false, err.Error()
			}
			return true, ""
		}
		reg.Register(&agent.ExploreTool{NewChild: func(task string) *agent.Loop {
			return agent.NewExploreChild(loop, task)
		}})
		// Same single-writer wiring as production above: eval parity so
		// the measured harness never drifts from the shipped one.
		// Audit is intentionally off here: trial traffic is synthetic
		// and AlwaysAllow — it must not pollute the governance trail.
		evalWork := &agent.WorkState{Root: dir}
		evalWork.NewChild = func(task, workRoot string) *agent.Loop {
			return agent.NewWorkChild(loop, task, workRoot)
		}
		reg.Register(&agent.SpawnWorkTool{State: evalWork})
		reg.Register(&agent.ApplyWorkTool{State: evalWork})
		reg.Register(&agent.DiscardWorkTool{State: evalWork})
		// Core tools (search/memory/symbols/diagnose/remember/fetch)
		// already ride along via buildHarness above — registering them
		// again would panic on duplicates. Only subagents wire here.
		return loop
	}
	r := &eval.Runner{Tasks: tasks, Trials: trials, NewLoop: newLoop}
	if pid, mname, ok := provider.ParseModelRef(prov.Name()); ok {
		r.ProviderID, r.Model = pid, mname
	}
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
	fmt.Printf("\n%-22s %6s %6s %8s %8s %8s %8s %8s %10s %s\n", "TASK", "PASS", "OF", "MEDCALLS", "P90CALL", "MED_MS", "MED_IN", "MED_OUT", "MED_COST", "NOTES")
	zeros := 0
	for _, rep := range reports {
		notes := strings.Join(rep.Reasons, " | ")
		if rep.ZeroTokens {
			notes = strings.TrimSpace(notes + " [tokens unreported]")
		}
		fmt.Printf("%-22s %6d %6d %8d %8d %8d %8d %8d $%9.4f %s\n", rep.Name, rep.Passes, rep.Trials, rep.MedianCall, rep.P90Call, rep.MedianMS, rep.MedianIn, rep.MedianOut, rep.MedianCostUSD, notes)
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	data, _ := json.MarshalIndent(map[string]any{
		"ts":      time.Now().UTC().Format(time.RFC3339),
		"reports": reports,
	}, "", "  ")
	path := filepath.Join(dir, fmt.Sprintf("eval-%d.json", time.Now().Unix()))
	// 0600: reports embed model output, which may echo secrets.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "tilde: warning: cannot save eval report: %v\n", err)
		return
	}
	fmt.Println("report saved:", path)
}

// runHeadless executes one goal without a TTY. Confirm-tier calls are
// approved inline via stdin; --yes narrows that to the read-only allowlist
// (policy.UnattendedAllowed), so mutating and network confirm-tier calls
// still need an explicit 'y' — deny-tier always blocks. Format "json"
// prints one JSON object per line (events, then a final result with usage)
// for CI pipelines; anything else prints human text.
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
			return policy.UnattendedAllowed(tool, args)
		}
		if asJSON {
			emitJSON(map[string]any{"type": "approval", "tool": tool, "args": args})
		} else {
			// stderr: stdout may be piped as data (assistant text /
			// final answer); the interactive prompt is a diagnostic.
			fmt.Fprintf(os.Stderr, "approve %s? [y/n] ", policy.Describe(tool, args))
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
		code := headlessExitCode(err, text, denyHit)
		class := provider.Classify(err)
		if asJSON {
			emitJSON(map[string]any{"type": "result", "ok": false, "error": err.Error(), "class": class,
				"tokens_in": loop.TotPrompt.Load(), "tokens_out": loop.TotCompletion.Load()})
		} else {
			fmt.Fprintf(os.Stderr, "tilde [%s]: %s\n", class, err)
		}
		os.Exit(code)
	}
	if asJSON {
		emitJSON(map[string]any{"type": "result", "ok": true, "text": text,
			"tokens_in": loop.TotPrompt.Load(), "tokens_out": loop.TotCompletion.Load()})
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

// offerResume reports whether startup should open the session picker
// unprompted: an unclosed session exists, it belongs to this project,
// and there is a screen to show the picker on. Anything else keeps
// the stderr hint above as the whole offer — a picker with no screen,
// or for another project's session, would be a wrong turn.
func offerResume(unclosed, root string) bool {
	if unclosed == "" || root == "" || !isTTY() {
		return false
	}
	return sessionMetaRoot(unclosed) == root
}

// sessionMetaRoot reads a session file's meta root ("" when the file
// is missing, corrupt, or never recorded one — all read as "not
// mine"). Bounded: only the first 100 lines are scanned. A scanner
// error (e.g. an over-long line aborting the scan) is reported on
// stderr rather than silently suppressing a legitimate resume offer.
func sessionMetaRoot(id string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	f, err := os.Open(filepath.Join(home, ".tilde", "sessions", id+".jsonl"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 256*1024)
	for n := 0; n < 100 && sc.Scan(); n++ {
		var e struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Type != "meta" {
			continue
		}
		if r, _ := e.Data["root"].(string); r != "" {
			return r
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "tilde: session %q unreadable past the scanned prefix (%v) — not offered for resume; inspect the file directly\n", id, err)
	}
	return ""
}

// runTrustCmd implements `tilde trust [dir]` / `tilde untrust [dir]`.
// Explicit user action only; headless callers must invoke it directly.
func runTrustCmd(verb string, args []string) error {
	dir := ""
	if len(args) > 1 {
		dir = args[1]
	}
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("cannot determine working dir: %w", err)
		}
	}
	if strings.HasPrefix(dir, "-") {
		return fmt.Errorf("usage: tilde %s [dir] — got flag-like %q", verb, dir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", dir, err)
	}
	if verb == "trust" {
		if err := trust.SetTrusted(abs, true); err != nil {
			return err
		}
		fmt.Printf("trusted %s\n", abs)
		return nil
	}
	if err := trust.SetTrusted(abs, false); err != nil {
		return err
	}
	fmt.Printf("untrusted %s\n", abs)
	return nil
}

// runAuditCmd implements `tilde audit [--since ...] [--tool ...]
// [--decision ...] [--json]`: read-only rendering of the governance
// trail. --since accepts RFC3339 or a Go duration ("24h" = last day).
func runAuditCmd(args []string) error {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	since := fs.String("since", "", "RFC3339 time or Go duration (e.g. 24h)")
	tool := fs.String("tool", "", "exact tool name filter")
	decision := fs.String("decision", "", "exact decision filter")
	asJSON := fs.Bool("json", false, "one JSON object per line")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot locate home: %w", err)
	}
	events, skipped, err := audit.ReadAllWithSkipped(filepath.Join(home, ".tilde", "audit", "audit.jsonl"))
	if err != nil {
		return err
	}
	opts := audit.FilterOpts{Tool: *tool, Decision: *decision}
	if *since != "" {
		ts, err := time.Parse(time.RFC3339, *since)
		if err != nil {
			d, derr := time.ParseDuration(*since)
			if derr != nil {
				return fmt.Errorf("bad --since %q: want RFC3339 or Go duration: %w", *since, derr)
			}
			ts = time.Now().Add(-d)
		}
		opts.Since = ts
	}
	events = audit.Filter(events, opts)
	if *asJSON {
		fmt.Print(audit.RenderJSON(events))
	} else {
		fmt.Print(audit.RenderText(events))
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "tilde: warning: skipped %d corrupt audit lines\n", skipped)
	}
	return nil
}

// runScheduleCmd implements `tilde schedule [--json]`: read-only listing
// of .tilde/schedule.yaml jobs with enabled/due/next-run columns. Bad
// config fails loud (exit 1 via the caller); the OS still owns waking.
func runScheduleCmd(args []string) error {
	fs := flag.NewFlagSet("schedule", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "one JSON object per line")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: tilde schedule [--json] — got extra args %q", fs.Args())
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot determine working dir: %w", err)
	}
	jobs, err := schedule.LoadFile(filepath.Join(root, ".tilde", "schedule.yaml"))
	if err != nil {
		return err
	}
	st, err := schedule.LoadState(filepath.Join(root, ".tilde", "schedule-state.json"))
	if err != nil {
		return err
	}
	now := time.Now()
	dueSet := map[string]bool{}
	for _, j := range schedule.Due(now, jobs, st) {
		dueSet[j.ID] = true
	}
	if *asJSON {
		for _, j := range jobs {
			next, ok := schedule.NextRun(now, j, st)
			obj := map[string]any{
				"id":      j.ID,
				"enabled": j.Enabled,
				"due":     dueSet[j.ID],
				"every":   j.Every.String(),
				"at":      j.At,
			}
			if last, has := st[j.ID]; has && !last.IsZero() {
				obj["last_run"] = last.UTC().Format(time.RFC3339)
			} else {
				obj["last_run"] = nil
			}
			if ok {
				obj["next_run"] = next.UTC().Format(time.RFC3339)
			} else {
				obj["next_run"] = nil
			}
			b, err := json.Marshal(obj)
			if err != nil {
				return fmt.Errorf("schedule: marshal row: %w", err)
			}
			fmt.Println(string(b))
		}
		return nil
	}
	if len(jobs) == 0 {
		fmt.Println("tilde: schedule: no jobs (.tilde/schedule.yaml missing or empty)")
		return nil
	}
	for _, j := range jobs {
		due := "no"
		if dueSet[j.ID] {
			due = "yes"
		}
		next := "-"
		if n, ok := schedule.NextRun(now, j, st); ok {
			next = n.UTC().Format(time.RFC3339)
		}
		at := j.At
		if at == "" {
			at = "-"
		}
		fmt.Printf("%s enabled=%t due=%s every=%s at=%s next=%s\n", j.ID, j.Enabled, due, j.Every, at, next)
	}
	return nil
}

// runDueCmd implements `tilde run-due [--yes]`: load
// .tilde/schedule.yaml in the project root, and for each due entry
// re-invoke this binary headlessly (`--prompt ... --mode build`) as a
// child process. Self-reexec keeps every headless semantic (session
// logs, exit codes, approvals) in exactly one place; the scheduler
// stays a thin due-checker because the OS owns waking (cron/systemd
// calls run-due). Failed jobs are not marked (retry next tick).
func runDueCmd(args []string) error {
	yes := false
	rest := args[1:]
	if len(rest) > 0 && rest[0] == "--yes" {
		yes = true
		rest = rest[1:]
	}
	root := ""
	if len(rest) > 0 {
		root = rest[0]
	}
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("cannot determine working dir: %w", err)
		}
	}
	if strings.HasPrefix(root, "-") {
		return fmt.Errorf("usage: tilde run-due [--yes] [dir] — got flag-like %q", root)
	}
	var err error
	root, err = filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("cannot resolve scheduler root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("cannot resolve scheduler root %q: %w", root, err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil || !rootInfo.IsDir() {
		return fmt.Errorf("scheduler root %q is not a directory", root)
	}
	jobs, err := schedule.LoadFile(filepath.Join(root, ".tilde", "schedule.yaml"))
	if err != nil {
		return err
	}
	statePath := filepath.Join(root, ".tilde", "schedule-state.json")
	// Hold the lock through due evaluation, child execution, and state
	// writes. A second cron/systemd wakeup must not run the same job while
	// the first invocation is still in flight.
	release, err := schedule.AcquireLock(statePath + ".lock")
	if err != nil {
		if errors.Is(err, schedule.ErrLocked) {
			return fmt.Errorf("run-due already running for %s — skipping overlapping invocation", root)
		}
		return err
	}
	defer release()
	st, err := schedule.LoadState(statePath)
	if err != nil {
		return err
	}
	now := time.Now()
	due := schedule.Due(now, jobs, st)
	if len(due) == 0 {
		fmt.Println("tilde: run-due: nothing due")
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate own binary: %w", err)
	}
	failed := 0
	for _, j := range due {
		fmt.Fprintf(os.Stderr, "tilde: run-due: job %q ...\n", j.ID)
		cmdArgs := []string{"--prompt", j.Prompt, "--mode", "build"}
		if yes {
			cmdArgs = append(cmdArgs, "--yes")
		}
		// Bound each child: the scheduler lock is held for the whole
		// loop, so one hung job must not wedge every future tick into
		// "already running". Progress goes to stderr — the child
		// inherits this process's stdout, which may be piped as data.
		jobCtx, jobCancel := context.WithTimeout(context.Background(), 30*time.Minute)
		cmd := exec.CommandContext(jobCtx, self, cmdArgs...)
		cmd.Dir = root
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		runErr := cmd.Run()
		jobCancel()
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "tilde: run-due: job %q failed (%v) — not marked, retries next tick\n", j.ID, runErr)
			failed++
			continue
		}
		// Stamp completion time, not loop-start time: a long job must
		// not become due again sooner than `every` after it finished.
		st = schedule.MarkRun(st, j.ID, time.Now())
		if err := schedule.SaveState(statePath, st); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "tilde: run-due: job %q done\n", j.ID)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d jobs failed", failed, len(due))
	}
	return nil
}

// pluginHome returns the user plugin dir (~/.tilde/plugins).
func pluginHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate home: %w", err)
	}
	return filepath.Join(home, ".tilde", "plugins"), nil
}

// runPruneCmd implements `tilde prune`: retention windows for session
// logs (default 30d, newest 5 always kept), the audit trail (default
// 90d), and spilled tool output (default 7d). Dry run without --yes.
// Cron-friendly: missing dirs are no-ops.
func runPruneCmd(args []string) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	sessionsAge := fs.String("sessions", "30d", "delete session logs older than this (Go duration)")
	auditAge := fs.String("audit", "90d", "drop audit events older than this (Go duration)")
	spillAge := fs.String("spill", "7d", "delete spilled tool output older than this (Go duration)")
	yes := fs.Bool("yes", false, "actually delete (without it: dry-run plan only)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	sAge, err := parseRetention(sessionsAge, "sessions")
	if err != nil {
		return err
	}
	spAge, err := parseRetention(spillAge, "spill")
	if err != nil {
		return err
	}
	aAge, err := parseRetention(auditAge, "audit")
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot locate home: %w", err)
	}
	sessDir := filepath.Join(home, ".tilde", "sessions")
	auditPath := filepath.Join(home, ".tilde", "audit", "audit.jsonl")
	spillDir := spill.Dir()
	if !*yes {
		fmt.Printf("tilde: prune plan (dry run — pass --yes to delete):\n")
		fmt.Printf("  sessions older than %s in %s (newest 5 kept)\n", sAge, sessDir)
		fmt.Printf("  audit events older than %s in %s\n", aAge, auditPath)
		fmt.Printf("  spilled tool output older than %s in %s\n", spAge, spillDir)
		return nil
	}
	deleted, err := session.Prune(sessDir, sAge, 5)
	if err != nil {
		return err
	}
	fmt.Printf("tilde: pruned %d session(s)\n", len(deleted))
	for _, n := range deleted {
		fmt.Printf("  deleted %s\n", n)
	}
	kept, dropped, err := audit.Trim(auditPath, aAge)
	if err != nil {
		return err
	}
	fmt.Printf("tilde: audit: kept %d, dropped %d\n", kept, dropped)
	spilled, err := spill.Prune(spillDir, spAge)
	if err != nil {
		return err
	}
	fmt.Printf("tilde: spill: removed %d file(s)\n", spilled)
	return nil
}

// parseRetention parses a retention window: a Go duration ("720h")
// or a day count ("30d" = 720h). Negative values refuse.
func parseRetention(raw *string, flag string) (time.Duration, error) {
	s := strings.TrimSpace(*raw)
	if n, ok := strings.CutSuffix(s, "d"); ok {
		var days float64
		// %g accepts NaN/Inf and NaN < 0 is false, so without the
		// finiteness check "NaNd" would validate and convert to a
		// destructive duration (session wipe). Reject non-finite input.
		if _, err := fmt.Sscanf(n, "%g", &days); err != nil || days < 0 || math.IsNaN(days) || math.IsInf(days, 0) {
			return 0, fmt.Errorf("bad --%s %q: want a day count like 30d or a Go duration like 720h", flag, *raw)
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("bad --%s %q: want a day count like 30d or a Go duration like 720h", flag, *raw)
	}
	return d, nil
}

// runForkCmd implements `tilde fork <id> [--at RFC3339]`.
func runForkCmd(args []string) error {
	fs := flag.NewFlagSet("fork", flag.ContinueOnError)
	at := fs.String("at", "", "RFC3339 cutoff (inclusive); empty = branch from tip")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 || strings.HasPrefix(rest[0], "-") {
		return fmt.Errorf("usage: tilde fork <id> [--at RFC3339]")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot locate home: %w", err)
	}
	newID, err := session.Fork(filepath.Join(home, ".tilde", "sessions"), rest[0], *at)
	if err != nil {
		return err
	}
	fmt.Println(newID)
	return nil
}

// runExportCmd implements `tilde --export <session-id> [--out path.md]`:
// one distilled, scrubbed brief on disk (0600), one line on stdout in
// the timeline's `● Exported` shape rendered as text.
func runExportCmd(sessionID, out string) error {
	path, n, err := export.WriteBriefFile(sessionID, out)
	if err != nil {
		return err
	}
	fmt.Printf("Exported %s → %s (%d bytes)\n", sessionID, path, n)
	return nil
}

// runPluginCmd implements explicit, hash-pinned local plugin management.
// Install and upgrade sources are local dirs only (no network, no clone).
// Upgrade stages and verifies a new tree before activation; lifecycle state
// is persisted outside plugin content so Verify remains meaningful.
func runPluginCmd(args []string) error {
	if len(args) < 2 {
		return pluginUsageError()
	}
	home, err := pluginHome()
	if err != nil {
		return err
	}
	switch args[1] {
	case "list":
		entries, err := os.ReadDir(home)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Println("tilde: no plugins installed")
				return nil
			}
			return err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") || !e.IsDir() {
				continue
			}
			status, err := plugin.Inspect(home, e.Name())
			if err != nil {
				fmt.Printf("%s\t(unreadable: %v)\n", e.Name(), err)
				continue
			}
			fmt.Printf("%s\t%s\tenabled=%v\tverified=%v\n", status.Name, status.Version, status.Enabled, status.Verified)
		}
		return nil
	case "verify":
		if len(args) != 3 {
			return fmt.Errorf("usage: tilde plugin verify <name>")
		}
		if err := plugin.ValidateName(args[2]); err != nil {
			return err
		}
		dir := filepath.Join(home, args[2])
		if !plugin.Verify(dir) {
			return fmt.Errorf("plugin %q failed verification (drifted or unlocked) — reinstall from its source dir", args[2])
		}
		fmt.Printf("tilde: plugin %q verified\n", args[2])
		return nil
	case "install":
		if len(args) < 3 || len(args) > 4 {
			return fmt.Errorf("usage: tilde plugin install <dir> [--upgrade|--dry-run]")
		}
		upgrade := len(args) > 3 && args[3] == "--upgrade"
		dry := len(args) > 3 && args[3] == "--dry-run"
		if len(args) == 4 && !upgrade && !dry {
			return fmt.Errorf("usage: tilde plugin install <dir> [--upgrade|--dry-run]")
		}
		if dry {
			name, version, files, err := plugin.DryRun(args[2])
			if err != nil {
				return err
			}
			fmt.Printf("tilde: plugin %q %s would install %d files (nothing written):\n", name, version, len(files))
			for _, f := range files {
				fmt.Printf("  %s\n", f)
			}
			return nil
		}
		if upgrade {
			if _, err := plugin.Upgrade(args[2], home); err != nil {
				return err
			}
		} else if _, err := plugin.Install(args[2], home); err != nil {
			return err
		}
		fmt.Printf("tilde: plugin installed from %s\n", args[2])
		return nil
	case "upgrade":
		if len(args) != 3 {
			return fmt.Errorf("usage: tilde plugin upgrade <dir>")
		}
		if _, err := plugin.Upgrade(args[2], home); err != nil {
			return err
		}
		fmt.Printf("tilde: plugin upgraded from %s\n", args[2])
		return nil
	case "enable", "disable":
		if len(args) != 3 {
			return fmt.Errorf("usage: tilde plugin %s <name>", args[1])
		}
		if err := plugin.ValidateName(args[2]); err != nil {
			return err
		}
		var err error
		if args[1] == "enable" {
			err = plugin.Enable(home, args[2])
		} else {
			err = plugin.Disable(home, args[2])
		}
		if err != nil {
			return err
		}
		fmt.Printf("tilde: plugin %q %sd\n", args[2], args[1])
		return nil
	case "remove":
		if len(args) != 3 {
			return fmt.Errorf("usage: tilde plugin remove <name>")
		}
		if err := plugin.Remove(home, args[2]); err != nil {
			return err
		}
		fmt.Printf("tilde: plugin %q removed\n", args[2])
		return nil
	case "rollback":
		if len(args) != 3 {
			return fmt.Errorf("usage: tilde plugin rollback <name>")
		}
		if err := plugin.Rollback(home, args[2]); err != nil {
			return err
		}
		fmt.Printf("tilde: plugin %q rolled back\n", args[2])
		return nil
	default:
		return pluginUsageError()
	}
}

func pluginUsageError() error {
	return fmt.Errorf("usage: tilde plugin install <dir> [--upgrade|--dry-run] | upgrade <dir> | enable|disable|remove|rollback <name> | verify <name> | list")
}

// headlessExitCode maps a loop outcome to the spec §4 table: 3 = provider
// error exhausted, 4 = doom-loop / iteration-cap handoff, 5 = deny-tier
// hit, 1 = uncategorized. Config/startup is 2, wired at the call sites.
func headlessExitCode(err error, text string, denyHit bool) int {
	// A deny-tier outcome is sticky: even when the run later errors
	// (e.g. handoff), callers must still see exit 5, not a generic 1.
	if denyHit {
		return 5
	}
	if err == nil {
		return 0
	}
	msg := err.Error()
	if strings.Contains(text, "[HANDOFF TO PLAN") ||
		strings.Contains(msg, "reverting to Plan") ||
		strings.Contains(msg, "Iteration cap") {
		return 4
	}
	if strings.HasPrefix(msg, "ollama:") || strings.HasPrefix(msg, "openai:") ||
		strings.HasPrefix(msg, "anthropic:") || strings.HasPrefix(msg, "gemini:") ||
		strings.HasPrefix(msg, "openrouter:") || strings.HasPrefix(msg, "opencode:") ||
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
