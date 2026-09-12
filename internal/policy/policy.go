// Package policy — graduated autonomy tiers (docs/Plan.md §6).
// deny → blocked always; ask → prompt (auto-approved only with --yes/Auto,
// never for destructive shapes); allow → runs free. Deny beats ask beats
// allow, always. Shell commands are judged per pipeline segment on parsed
// argv — never substrings — so `echo curl` passes and `echo hi; rm -rf /`
// does not. The sandbox stays the hard backstop behind all of this.
package policy

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"tilde/internal/slopsquatting"
)

// Decision is what to do with a tool call.
type Decision int

const (
	Allow Decision = iota
	Ask
	Deny
)

// Policy is the rule set: an optional policies.yaml overlay plus the
// hardcoded destructive-command analysis below.
type Policy struct {
	// AlwaysAllow, if true, auto-approves Ask-tier calls (Auto mode / --yes
	// for confirm-tier only; deny-tier still blocks — see Check).
	AlwaysAllow bool
	// Unattended narrows AlwaysAllow for --yes/headless runs: only the
	// explicit read-only allowlist may be approved without an operator.
	// Interactive Auto mode leaves this false and keeps its existing scope.
	Unattended bool
	// File is the loaded policies.yaml overlay (nil = defaults).
	// deny wins over everything, including AlwaysAllow.
	File *File
	// Root is the project root used to match an absolute spelling of a
	// contained path against deny_paths (a relative glob like `*.key`
	// must still deny `/root/of/project/id.key`). Empty disables the
	// absolute-path form; relative targets always match as before.
	Root string

	// mu guards sessionAllow: Check runs on the agent goroutine
	// (Loop.Run via runAgentCmd) while ApproveSession runs on the TUI
	// render thread (updateConfirm) — and shellEscape Checks from the
	// render thread too. Construct with keyed literals only, and never
	// copy a Policy after first use (go vet copylocks enforces this).
	mu sync.Mutex
	// sessionAllow is the in-memory exact-command allowlist ([a] key):
	// literal shell_command strings approved for this process only.
	// Never persisted, never pattern-matched — Check compares the
	// exact args["command"] string, after deny evaluation, so a
	// deny-tier shape stays denied even when listed.
	sessionAllow map[string]struct{}
}

// File is the policies.yaml shape: tool-name lists per tier, plus the
// per-host network allowlist below (P1-G).
type File struct {
	Deny  []string `yaml:"deny"`
	Ask   []string `yaml:"ask"`
	Allow []string `yaml:"allow"`
	// AllowNet lists hostnames web_fetch may retrieve without the
	// session-wide TILDE_ALLOW_NET=1 opt-in. Exact hostname match,
	// case-insensitive; SSRF guards (private/loopback/link-local) still
	// apply after the gate.
	AllowNet []string `yaml:"allow_net"`
	// DenyPaths is the P7-A path-scoped deny list (yaml `deny_paths`):
	// ordered globs evaluated in Check BEFORE tier lookup — a match
	// denies with the same supremacy as the deny tier (beats ask, allow,
	// and AlwaysAllow/--yes). Applies only to file-path args of the
	// path tools (read_file/write_file/edit_file `path`, grep `dir`,
	// glob `pattern`); every other tool — shell_command included —
	// ignores the list. Matching is pure string on the slash-normalized,
	// path.Clean-ed value as given (no filesystem access for the lexical
	// form); an absolute path arg is ALSO tested root-relative against
	// the pattern (and via a best-effort symlink resolve), so a relative
	// glob cannot be dodged by spelling a contained path absolutely.
	// Containment still owns `..` escapes at exec time.
	// Shell cwd scoping is deliberately OUT: shell stays argv-judged
	// per segment (shellDeny), and its cwd is already containment-bound.
	// NOTE (owner wiring, outside internal/policy): main.go loadPolicies
	// rejects unknown top-level keys — it must allowlist `deny_paths`
	// alongside deny/ask/allow/allow_net, or a file using this key
	// refuses to start.
	DenyPaths []string `yaml:"deny_paths"`
}

// Load reads a policies.yaml overlay. Missing file = defaults, not an
// error; malformed YAML names the fix.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	// Fail LOUD at startup: an unparseable deny_paths glob must refuse
	// the file (same posture as malformed YAML above), never silently
	// narrow the guard toward defaults.
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// NetAllowed reports whether host is allowlisted for web_fetch without
// the session-wide egress opt-in. Comparison is exact on the normalized
// hostname (lowercased, trailing dot stripped, :port tolerated); nil
// file allows nothing.
func (f *File) NetAllowed(host string) bool {
	if f == nil {
		return false
	}
	h := normalizeNetHost(host)
	if h == "" {
		return false
	}
	for _, a := range f.AllowNet {
		if normalizeNetHost(a) == h {
			return true
		}
	}
	return false
}

// normalizeNetHost lowercases, trims space/trailing dot, and tolerates
// entries carrying a path or a single :port (IPv6 literals with multiple
// colons are left intact).
func normalizeNetHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if i := strings.IndexByte(h, '/'); i >= 0 {
		h = h[:i]
	}
	if strings.Count(h, ":") == 1 {
		h = h[:strings.IndexByte(h, ':')]
	}
	return strings.TrimSuffix(h, ".")
}

func listed(list []string, tool string) bool {
	for _, t := range list {
		if t == tool {
			return true
		}
	}
	return false
}

// UnknownTools reports tier entries naming no known tool — a typo here
// fails dangerous in opposite directions (a misspelled allow is dead
// weight; a misspelled deny is a missing guard), so callers refuse the
// file instead of guessing. Nil file = defaults = nothing unknown.
func (f *File) UnknownTools(known []string) []string {
	if f == nil {
		return nil
	}
	have := map[string]bool{}
	for _, k := range known {
		have[k] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, tier := range [][]string{f.Deny, f.Ask, f.Allow} {
		for _, t := range tier {
			if !have[t] && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// Validate rejects unparseable deny_paths entries — malformed globs
// and empty entries alike — naming the offending pattern and the fix,
// so Load fails LOUD at startup instead of running a silently narrowed
// guard. Nil/empty list is valid (the list is off). Tier-name typos
// stay the caller's job (UnknownTools); this covers only the globs.
func (f *File) Validate() error {
	if f == nil {
		return nil
	}
	for _, pat := range f.DenyPaths {
		if strings.TrimSpace(pat) == "" {
			return fmt.Errorf("invalid deny_paths entry %q (expected a glob like \"**/secrets/**\") — remove it or fix the pattern", pat)
		}
		if _, err := compileDenyPattern(normDenyPath(pat)); err != nil {
			return fmt.Errorf("invalid deny_paths pattern %q — fix the pattern or remove it", pat)
		}
	}
	return nil
}

// PathDenyReason reports why a tool call is path-denied: the matched
// (or broken) deny_paths pattern plus the fix. Empty string = not
// path-denied. This is the single choke point Check uses, and future
// deny surfacing in main.go should print it verbatim — it always names
// the pattern. Non-path tools yield "" without looking at args.
// A broken pattern denies path-tool calls fail-CLOSED here (Load
// already refuses such files at startup; this covers Files built
// without Load, e.g. in tests or embeddings of defaults).
func (f *File) PathDenyReason(tool string, args map[string]any, root string) string {
	if f == nil || len(f.DenyPaths) == 0 {
		return ""
	}
	cand, ok := pathArgForTool(tool, args)
	if !ok {
		return ""
	}
	// Compile every pattern first so a broken pattern reports itself
	// regardless of which candidate would have matched.
	matchers := make([]func(string) bool, 0, len(f.DenyPaths))
	for _, pat := range f.DenyPaths {
		if strings.TrimSpace(pat) == "" {
			return fmt.Sprintf("invalid deny_paths entry %q — remove it or fix the pattern", pat)
		}
		m, err := compileDenyPattern(normDenyPath(pat))
		if err != nil {
			return fmt.Sprintf("invalid deny_paths pattern %q — fix the pattern or remove it", pat)
		}
		matchers = append(matchers, m)
	}
	for _, target := range denyCandidates(cand, root) {
		for i, m := range matchers {
			if m(target) {
				return fmt.Sprintf("deny_paths pattern %q matched — narrow the path or amend deny_paths", f.DenyPaths[i])
			}
		}
	}
	return ""
}

// denyCandidates lists the normalized spellings of a path arg that
// deny_paths should be tested against. A relative arg yields one form;
// an absolute arg additionally yields its root-relative form (so `*.key`
// matches `/root/id.key`) and the same for a best-effort symlink-resolved
// form — closing the "spell the contained path absolutely" bypass. Forms
// that escape root are dropped, so a `../` spelling can never manufacture
// a false match.
func denyCandidates(cand, root string) []string {
	out := []string{normDenyPath(cand)}
	if root == "" || !filepath.IsAbs(cand) {
		return out
	}
	cleanRoot := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		cleanRoot = r
	}
	add := func(p string) {
		if p == "" {
			return
		}
		n := normDenyPath(p)
		for _, e := range out {
			if e == n {
				return
			}
		}
		out = append(out, n)
	}
	spellings := []string{cand}
	if real, err := filepath.EvalSymlinks(cand); err == nil {
		spellings = append(spellings, real)
	}
	for _, p := range spellings {
		rel, err := filepath.Rel(cleanRoot, p)
		if err != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue // outside root: no root-relative form to match
		}
		add(rel)
	}
	return out
}

// pathArgForTool returns the file-path argument a path-scoped deny
// applies to, per tool. read/write/edit take a concrete `path`; grep's
// `pattern` is a content substring (never a path — matching it would
// deny on file CONTENTS), so grep scopes on its `dir` subdir instead;
// glob's `pattern` is itself a file glob, matched as a string (a broad
// `**/*.go` still lists — per-file reads stay gated). Absent or
// non-string args yield false (nothing to judge). Shell is explicitly
// OUT: argv-judged per segment, cwd containment-bound (see File).
func pathArgForTool(tool string, args map[string]any) (string, bool) {
	var key string
	switch tool {
	case "read_file", "write_file", "edit_file":
		key = "path"
	case "grep":
		key = "dir"
	case "glob":
		key = "pattern"
	default:
		return "", false
	}
	s, _ := args[key].(string)
	if s == "" {
		return "", false
	}
	return s, true
}

// normDenyPath slash-normalizes and Cleans a path or pattern for
// comparison: `a/../b` judges as `b`, `./x` as `x`. Pure string — no
// filesystem access, no symlink resolution; containment owns escapes.
func normDenyPath(s string) string {
	return path.Clean(filepath.ToSlash(s))
}

// compileDenyPattern builds a whole-target matcher for one normalized
// deny glob: minimal doublestar — `**/` spans any depth including
// zero, a trailing `/**` also matches the dir itself (policy targets
// include bare dirs like grep's `dir`, where tools.walkGlob only ever
// sees file paths — the one deliberate difference from that helper),
// bare `**` spans everything, and single `*`/`?` stay inside one path
// segment via path.Match (so `*.key` never matches `sub/id.key`).
func compileDenyPattern(pat string) (func(string) bool, error) {
	if strings.Contains(pat, "**") {
		rx, err := denyGlobRegex(pat)
		if err != nil {
			return nil, err
		}
		return rx.MatchString, nil
	}
	if _, err := path.Match(pat, ""); err != nil {
		return nil, err
	}
	return func(t string) bool {
		ok, _ := path.Match(pat, t)
		return ok
	}, nil
}

// denyGlobRegex translates a doublestar glob to a regex, mirroring
// tools.globRegex (`**/` = any depth incl. zero, `*`/`?` stay within
// one segment) plus the trailing-`/**`-matches-the-dir-itself rule
// documented on compileDenyPattern.
func denyGlobRegex(pat string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pat); {
		switch {
		case strings.HasPrefix(pat[i:], "**/"):
			b.WriteString("(.*/)?")
			i += 3
		case strings.HasPrefix(pat[i:], "/**") && i+3 == len(pat):
			// Trailing /** consumed WITH its slash, so `secrets/**`
			// matches the dir itself as well as everything under it
			// (emitting `/` + `(/.*)?` instead would require the slash
			// and miss the bare dir).
			b.WriteString("(/.*)?")
			i += 3
		case strings.HasPrefix(pat[i:], "**"):
			b.WriteString(".*")
			i += 2
		case pat[i] == '*':
			b.WriteString("[^/]*")
			i++
		case pat[i] == '?':
			b.WriteString("[^/]")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(pat[i])))
			i++
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// ApproveSession records one literal shell command string as approved
// for the rest of this process ([a] key in the confirm panel). It
// returns false — recording nothing — for empty strings and for
// deny-tier shapes: those must never enter the list, and Check consults
// the list only after deny evaluation anyway, so deny always wins even
// for a listed command. Session-scoped by construction: the map lives
// on the Policy and dies with the process; nothing is written to disk.
func (p *Policy) ApproveSession(cmd string) bool {
	if p == nil || cmd == "" || shellDeny(cmd) {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessionAllow == nil {
		p.sessionAllow = map[string]struct{}{}
	}
	p.sessionAllow[cmd] = struct{}{}
	return true
}

// sessionAllowed reports whether cmd was approved for this session via
// ApproveSession. Exact string equality only — different args, flags,
// or whitespace still prompt.
func (p *Policy) sessionAllowed(cmd string) bool {
	if p == nil || cmd == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.sessionAllow[cmd]
	return ok
}

// argOp normalizes a string arg for allowlist checks: missing/non-string
// reads as "", otherwise lower-cased and trimmed. Centralizes the
// args["x"].(string) + ToLower + TrimSpace pattern.
func argOp(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return strings.ToLower(strings.TrimSpace(s))
}

// UnattendedAllowed reports whether a call is safe to approve without an
// operator. The list is deliberately closed: shell commands and MCP calls
// stay prompt-gated because their arguments can mutate or exfiltrate even
// when their tool names look familiar.
func UnattendedAllowed(tool string, args map[string]any) bool {
	switch tool {
	case "read_file", "grep", "glob", "git_status", "git_diff",
		"git_worktree_list", "load_skill", "mcp_list", "symbol_search",
		"diagnose":
		return true
	case "shell_poll":
		action := argOp(args, "action")
		return action == "status" || action == "log"
	case "memory":
		return strings.EqualFold(strings.TrimSpace(argOp(args, "op")), "recall")
	case "remember":
		op := argOp(args, "op")
		return op == "recall" || op == "status"
	default:
		return false
	}
}

func (p *Policy) autoAllowed(tool string, args map[string]any) bool {
	return p != nil && p.AlwaysAllow && (!p.Unattended || UnattendedAllowed(tool, args))
}

// Check returns the decision for a tool call.
func (p *Policy) Check(tool string, args map[string]any) Decision {
	if p != nil && p.File != nil {
		if reason := p.File.PathDenyReason(tool, args, p.Root); reason != "" {
			return Deny // path-scoped deny beats everything, including --yes
		}
	}
	var f File
	if p != nil && p.File != nil {
		f = *p.File
	}
	if listed(f.Deny, tool) {
		return Deny // deny beats everything, including --yes
	}
	switch tool {
	case "write_file", "edit_file", "git_worktree_add", "git_worktree_remove", "mcp_call",
		"spawn_work", "discard_work":
		// spawn/discard create/remove git worktrees — same class as
		// git_worktree_add/remove, so Ask even when policies.yaml is
		// missing (apply_work is read-only review: yaml-listed instead).
		if p.autoAllowed(tool, args) {
			return Allow
		}
		return Ask
	case "shell_command":
		cmd, _ := args["command"].(string)
		if shellDeny(cmd) {
			return Deny // destructive shape: run it in your own shell
		}
		// TILDE_STRICT_INSTALL: install commands always prompt, even in
		// Auto mode. The slopsquatting surface is too high-stakes to
		// auto-approve — a hallucinated package name installs silently.
		if isInstallShape(cmd) && os.Getenv("TILDE_STRICT_INSTALL") == "1" {
			if p.sessionAllowed(cmd) {
				return Allow // user explicitly approved this exact command
			}
			return Ask // never auto-approve installs in strict mode
		}
		if p.sessionAllowed(cmd) {
			return Allow // exact literal approved via [a] this session
		}
		if p.autoAllowed(tool, args) {
			return Allow
		}
		return Ask
	default:
		if listed(f.Ask, tool) || builtinAsk(tool) {
			return Ask
		}
		return Allow // read-only tools run free
	}
}

// builtinAsk reports tools that are ask-tier by design even when no
// policies.yaml lists them: they perform outbound network egress (and
// web_shot spawns an unsandboxed browser), which the bwrap sandbox does
// not gate. A missing policy file must not silently free them; Auto mode
// still upgrades Ask→Allow in the loop, and --yes stays narrowed by its
// unattended allowlist.
func builtinAsk(tool string) bool {
	switch tool {
	case "web_fetch", "web_search", "web_shot":
		return true
	}
	return false
}

// bareFetchers fetch remote code under any invocation (npx/uvx run it,
// pip-family needs a fetch verb — see below).
var bareFetchers = map[string]bool{"npx": true, "uvx": true}

// fetchVerbs are the pip-family subcommands that download.
var fetchVerbs = map[string]bool{"install": true, "download": true, "wheel": true}

// installVerbs maps a package manager to the subcommands that fetch.
var installVerbs = map[string][]string{
	"uv": {"install", "add"}, "npm": {"install", "i", "add"},
	"pnpm": {"add", "install", "i"}, "yarn": {"add"},
	"bun": {"add", "install", "i"}, "go": {"get", "install"},
	"cargo": {"add", "install"}, "gem": {"install"},
	"bundle": {"install", "add"}, "composer": {"require"},
	"apt": {"install"}, "apt-get": {"install"},
	"dnf": {"install"}, "yum": {"install"},
	"apk": {"add"}, "pacman": {"-S"}, "pipx": {"install", "run"},
}

// isInstallShape reports whether any pipeline segment fetches a remote
// package — the slopsquatting surface (models hallucinate plausible
// names; attackers pre-register them). Same segment/argv parsing as the
// deny judge so wrappers can't hide it; unparseable segments are skipped
// here because deny already fails those closed. Matched commands stay
// Ask-tier (installing real dependencies is normal agent work) — the
// mitigation is an annotated confirm prompt, not a block.
func isInstallShape(cmd string) bool {
	has := func(rest []string, want map[string]bool) bool {
		for _, a := range rest {
			if want[strings.ToLower(a)] {
				return true
			}
		}
		return false
	}
	for _, seg := range splitSegments(cmd) {
		argv, ok := stripPrefix(splitArgs(seg))
		if !ok || len(argv) == 0 {
			continue
		}
		first := path.Base(strings.ToLower(argv[0]))
		rest := argv[1:]
		if bareFetchers[first] {
			return true
		}
		if first == "pip" || first == "pip3" {
			if has(rest, fetchVerbs) {
				return true
			}
			continue
		}
		if (first == "python" || first == "python3") && has(rest, map[string]bool{"pip": true}) && has(rest, fetchVerbs) {
			return true // `python -m pip install …` hides pip behind the interpreter
		}
		for _, v := range installVerbs[first] {
			for _, a := range rest {
				if strings.EqualFold(a, v) {
					return true
				}
			}
		}
	}
	return false
}

// shellDeny reports whether a shell command matches a destructive shape.
// Every pipeline segment (split on ; && || | & and newlines) is judged;
// the worst wins, so `echo hi; rm -rf /` cannot smuggle the second half
// past. Wrapper/expansion tokens fail closed: a segment the parser cannot
// see through (command substitution, indirect invocation) is denied.
func shellDeny(cmd string) bool {
	for _, seg := range splitSegments(cmd) {
		if segmentDeny(splitArgs(seg)) {
			return true
		}
	}
	return false
}

// segmentDeny and its helpers live in shell_deny.go.

// isSensitiveTarget reports absolute redirect targets under /dev, /proc
// or /sys. Anchored at the root: a relative `docs/proc/notes.txt` is an
// ordinary project write, not a host-device write.
func isSensitiveTarget(target string) bool {
	s := strings.TrimLeft(target, ">")
	if s == "/dev" || s == "/proc" || s == "/sys" {
		return true
	}
	return strings.HasPrefix(s, "/dev/") || strings.HasPrefix(s, "/proc/") || strings.HasPrefix(s, "/sys/")
}

// devAllowed names the only device writes an agent ever needs.
func devAllowed(target string) bool {
	t := target
	for strings.HasPrefix(t, ">") {
		t = t[1:]
	}
	switch t {
	case "/dev/null", "/dev/stdout", "/dev/stderr":
		return true
	}
	return false
}

// stripPrefix drops leading `VAR=name` assignments and shell
// keywords/control tokens (`time`, `!`, `(`, `{`, `if`/`then`/`fi`,
// `while`/`until`/`for`/`do`/`done`, `case`, `[[`) to a fixpoint, so the
// caller judges the real command. It reports false when the head is
// attached-form syntax it cannot see through (`!rm`, `[[x`) — fail closed.
func stripPrefix(argv []string) ([]string, bool) {
	for len(argv) > 0 {
		head := argv[0]
		if name, ok := envAssign(head); ok {
			switch strings.ToUpper(name) {
			case "GIT_DIR", "GIT_WORK_TREE", "GIT_PREFIX":
				return argv, false // git redirected outside the project: deny
			}
			argv = argv[1:]
			continue
		}
		base := path.Base(strings.ToLower(head))
		switch base {
		case "time", "!", "(", "{", "[[",
			"if", "then", "fi", "else", "elif",
			"while", "until", "for", "do", "done",
			"case", "in", "function", "select", "coproc",
			"}", ")", "]]":
			argv = argv[1:]
			// `time` takes its own flags (`-p`): drop those too so
			// `time -p go test` is still judged as `go test`.
			if base == "time" {
				for len(argv) > 0 && len(argv[0]) > 1 && argv[0][0] == '-' {
					argv = argv[1:]
				}
			}
			continue
		}
		if strings.HasPrefix(head, "!") || strings.HasPrefix(head, "[[") {
			return argv, false // attached control syntax: unjudgeable
		}
		return argv, true
	}
	return argv, true
}

// envAssign reports whether s is a `NAME=value` assignment with a valid
// shell name (`^[A-Za-z_][A-Za-z0-9_]*=`), returning the raw NAME.
func envAssign(s string) (string, bool) {
	i := strings.Index(s, "=")
	if i <= 0 {
		return "", false
	}
	name := s[:i]
	for j := 0; j < len(name); j++ {
		c := name[j]
		if c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (j > 0 && c >= '0' && c <= '9') {
			continue
		}
		return "", false
	}
	return name, true
}

// stripEnv drops env-modifier tokens, returning the real command.
// Allowlist-based: only known-harmless modifiers go (VAR=x assignments,
// -i, -0, --ignore-environment, -u/--unset + value, --chdir/-C + value).
// Anything else flag-shaped stays — and a remainder that still starts
// with `-` is unjudgeable, so the caller fails it closed.
func stripEnv(argv []string) []string {
	var out []string
	skipNext := false
	for _, a := range argv {
		if skipNext {
			skipNext = false
			continue
		}
		switch {
		case a == "-u" || a == "--unset" || a == "--chdir" || a == "-C":
			skipNext = true
		case a == "-i" || a == "-0" || a == "--ignore-environment":
			// harmless modifiers
		case !strings.HasPrefix(a, "-") && strings.Contains(a, "="):
			// VAR=x assignment
		default:
			out = append(out, a)
		}
	}
	return out
}

// isDuration reports NUMBER[suffix] shapes (`10`, `5s`, `2m`, `1h`).
func isDuration(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return false
	}
	rest := s[i:]
	return rest == "" || rest == "s" || rest == "m" || rest == "h" || rest == "d"
}

// stripFlags drops leading -flags and their values. Value-takers (-t/-s
// durations, -k keep-alive, -n niceness, -u user, -C dir, -o file,
// --unset/--chdir/--directory/--cwd/--workdir dirs) consume
// the following token; --flag=value forms are self-contained (strip one
// token); --preserve-status/--recursive take no value and strip as plain
// flags. Anything else flag-shaped left standing fails
// closed downstream by the caller checking the remainder.
func stripFlags(argv []string) []string {
	i := 0
	for i < len(argv) && len(argv[i]) > 0 && argv[i][0] == '-' {
		a := argv[i]
		// --flag=value is self-contained: never consume the next token.
		if strings.Contains(a, "=") {
			i++
			continue
		}
		// Value-taking flags consume the next token too.
		if (a == "-t" || a == "-s" || a == "-k" ||
			a == "-n" || a == "-u" || a == "-C" ||
			a == "-o" ||
			a == "--unset" || a == "--chdir" || a == "--directory" ||
			a == "--cwd" || a == "--workdir") && i+1 < len(argv) {
			i++
		}
		switch a {
		case "--preserve-status", "--recursive":
			// No value: strip one token via the generic i++ below.
			// Listed so the long-form audit is explicit and greppable.
		}
		i++
	}
	return argv[i:]
}

// hasFlag reports a flag's presence: exact match, --flag=value prefix
// form, or combined short flags (-fdx matches -f). Mirrors the local
// `has` closure in segmentDeny so long-flag spellings cannot bypass.
func hasFlag(argv []string, flags ...string) bool {
	for _, a := range argv {
		for _, f := range flags {
			if a == f || strings.HasPrefix(a, f+"=") {
				return true
			}
			// Combined short flags: -fdx matches -f and -d.
			if len(f) == 2 && f[0] == '-' && len(a) > 2 && a[0] == '-' && a[1] != '-' &&
				strings.Contains(a[1:], f[1:]) {
				return true
			}
		}
	}
	return false
}

// splitSegments cuts a command line on ; && || | & and newlines outside
// quotes. Newlines matter: `echo hi\nrm -rf /` is two commands, and the
// second must not ride the first's verdict. Heredoc bodies (`<<EOF … EOF`)
// are stdin DATA, not commands: the body lines are dropped and only the
// outer line (e.g. `python3 <<'PY'`) is judged — that visible line is what
// an Ask approval covers.
func splitSegments(cmd string) []string {
	var segs []string
	var cur strings.Builder
	var quote byte
	var pending []string // heredoc delimiters awaiting bodies after newline
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, s)
		}
		cur.Reset()
	}
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if quote != 0 {
			cur.WriteByte(c)
			if c == quote {
				quote = 0
			} else if c == '\\' && i+1 < len(cmd) {
				i++
				cur.WriteByte(cmd[i])
			}
			continue
		}
		// Heredoc opener outside quotes: `<<`, `<<-`, `<< EOF`, `<<'EOF'`.
		// `<<<` is a herestring (no body lines follow) — not this.
		if c == '<' && i+1 < len(cmd) && cmd[i+1] == '<' &&
			(i+2 >= len(cmd) || cmd[i+2] != '<') {
			if d, ok := heredocDelim(cmd, i+2); ok {
				pending = append(pending, d)
			}
			cur.WriteByte(c)
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			cur.WriteByte(c)
		case ';', '&', '|', '\n':
			flush()
			// Consume && and || as one separator.
			if (c == '&' || c == '|') && i+1 < len(cmd) && cmd[i+1] == c {
				i++
			}
			if c == '\n' && len(pending) > 0 {
				i = skipHeredocBody(cmd, i+1, pending)
				pending = nil
				// Lands on the delimiter line's last byte (or EOF);
				// the loop's i++ steps past it.
			}
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return segs
}

// heredocDelim parses the delimiter word after a `<<` opener at cmd[j:].
// It skips an optional `-` (tab-stripping form) and blanks, then reads an
// optionally quoted word. ok=false when no word follows.
func heredocDelim(cmd string, j int) (delim string, ok bool) {
	for j < len(cmd) && (cmd[j] == '-' || cmd[j] == ' ' || cmd[j] == '\t') {
		j++
	}
	if j >= len(cmd) {
		return "", false
	}
	if cmd[j] == '\'' || cmd[j] == '"' {
		q := cmd[j]
		j++
		start := j
		for j < len(cmd) && cmd[j] != q {
			j++
		}
		if j >= len(cmd) {
			return "", false
		}
		return cmd[start:j], true
	}
	start := j
	for j < len(cmd) && cmd[j] != ' ' && cmd[j] != '\t' && cmd[j] != '\n' &&
		cmd[j] != ';' && cmd[j] != '&' && cmd[j] != '|' {
		j++
	}
	if j == start {
		return "", false
	}
	return cmd[start:j], true
}

// skipHeredocBody advances from body start (index j, just past the outer
// line's newline) past every body line up to and including each pending
// delimiter line. Returns the index of the last delimiter line's final
// byte (or end of input when unterminated).
func skipHeredocBody(cmd string, j int, pending []string) int {
	for len(pending) > 0 && j <= len(cmd) {
		eol := strings.IndexByte(cmd[j:], '\n')
		var line string
		var next int
		if eol < 0 {
			line, next = cmd[j:], len(cmd)+1
		} else {
			line, next = cmd[j:j+eol], j+eol+1
		}
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == pending[0] {
			pending = pending[1:]
		}
		if len(pending) == 0 {
			if eol < 0 {
				return len(cmd)
			}
			return j + eol - 1
		}
		j = next
	}
	return len(cmd)
}

// splitArgs tokenizes one segment, honoring single/double quotes and
// backslash escapes. Best-effort: unbalanced quotes consume to end.
func splitArgs(seg string) []string {
	var argv []string
	var cur strings.Builder
	var quote byte
	inTok := false
	flush := func() {
		if inTok {
			argv = append(argv, cur.String())
			cur.Reset()
			inTok = false
		}
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				if c == '\\' && quote == '"' && i+1 < len(seg) {
					i++
					cur.WriteByte(seg[i])
				} else {
					cur.WriteByte(c)
				}
			}
			inTok = true
			continue
		}
		switch {
		case c == '\'' || c == '"':
			quote = c
			inTok = true
		case c == '\\' && i+1 < len(seg):
			i++
			cur.WriteByte(seg[i])
			inTok = true
		case c == ' ' || c == '\t':
			flush()
		default:
			cur.WriteByte(c)
			inTok = true
		}
	}
	flush()
	return argv
}

// Describe renders a confirm prompt line (literal command, never paraphrase).
// For install commands, it adds slopsquatting warnings when a package name
// looks like a known hallucination or common typo.
func Describe(tool string, args map[string]any) string {
	switch tool {
	case "shell_command":
		if c, _ := args["command"].(string); c != "" {
			if isInstallShape(c) {
				// Extract package names and check for hallucinations.
				warn := slopsquattingWarning(c)
				if warn != "" {
					return "Run: " + c + " — " + warn
				}
				return "Run: " + c + " — unverified package name, check spelling/registry before approving"
			}
			return "Run: " + c
		}
	case "mcp_call":
		srv, _ := args["server"].(string)
		tl, _ := args["tool"].(string)
		if srv != "" || tl != "" {
			return "mcp " + srv + "." + tl
		}
	case "write_file", "edit_file", "git_worktree_add", "git_worktree_remove":
		if pth, _ := args["path"].(string); pth != "" {
			return tool + " " + pth
		}
	}
	return tool
}

// slopsquattingWarning scans an install command for hallucinated package
// names. It returns a warning string if any are found, or "" if the
// command looks clean. Only called when isInstallShape already matched.
func slopsquattingWarning(cmd string) string {
	names := extractPackageNames(cmd)
	var flagged []string
	for _, name := range names {
		if ok, real := slopsquatting.LikelyHallucinated(name); ok {
			flagged = append(flagged, name+" (did you mean "+real+"?)")
		} else if slopsquatting.IsCommonTypo(name) {
			flagged = append(flagged, name+" (possible typo)")
		}
	}
	if len(flagged) == 0 {
		return ""
	}
	if len(flagged) == 1 {
		return "⚠ SLOPSQUATTING WARNING — " + flagged[0] + " — verify on the registry before approving"
	}
	return "⚠ SLOPSQUATTING WARNING — multiple suspicious packages: " + strings.Join(flagged, "; ") + " — verify on the registry before approving"
}

// extractPackageNames pulls package names out of install commands. It
// handles the common shapes: `pip install foo bar`, `npm i foo bar`,
// `uv add foo`, etc. Version specs (>=1.0, @1.0.0) are stripped.
// Package manager verbs (install, add, get, ...) are skipped.
func extractPackageNames(cmd string) []string {
	// Verbs that package managers take — skip these, they're not package names.
	verbs := map[string]bool{
		"install": true, "i": true, "add": true, "get": true,
		"download": true, "wheel": true, "require": true, "run": true,
	}
	var names []string
	seen := map[string]bool{}
	for _, seg := range splitSegments(cmd) {
		argv, ok := stripPrefix(splitArgs(seg))
		if !ok || len(argv) < 2 {
			continue
		}
		first := path.Base(strings.ToLower(argv[0]))
		// Skip the manager and its verb; everything after is a package.
		isMgr := first == "pip" || first == "pip3" || first == "npm" || first == "pnpm" ||
			first == "yarn" || first == "bun" || first == "go" || first == "cargo" ||
			first == "gem" || first == "bundle" || first == "uv" || first == "pipx"
		if !isMgr && first != "npx" && first != "uvx" {
			continue
		}
		for _, a := range argv[1:] {
			a = strings.TrimSpace(a)
			if a == "" || strings.HasPrefix(a, "-") {
				continue
			}
			// Skip package manager verbs (install, add, get, ...).
			if verbs[strings.ToLower(a)] {
				continue
			}
			// Strip version specs.
			if idx := strings.IndexAny(a, "@>"); idx > 0 {
				a = a[:idx]
			}
			a = strings.TrimLeft(a, "@")
			if a == "" || seen[a] {
				continue
			}
			seen[a] = true
			names = append(names, a)
		}
	}
	return names
}
